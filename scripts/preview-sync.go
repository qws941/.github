package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"

	"scripts/internal/cli"
	"scripts/internal/fsutil"
	"scripts/internal/ghcli"
)

const syncConfigRel = ".github/sync.yml"

type syncFile struct {
	Source string `json:"source"`
	Dest   string `json:"dest"`
}

type syncGroup struct {
	Files []syncFile `json:"files"`
	Repos []string   `json:"repos"`
}

type syncConfig struct {
	Groups []syncGroup
}

type downstreamContent struct {
	SHA  string `json:"sha"`
	URL  string `json:"html_url"`
	Git  string `json:"git_url"`
	API  string `json:"url"`
	Path string `json:"path"`

	Message string `json:"message"`
}

type fileChange struct {
	Source      string `json:"source"`
	Path        string `json:"path"`
	CurrentSHA  string `json:"current_sha,omitempty"`
	NewSHA      string `json:"new_sha"`
	Status      string `json:"status"`
	CurrentPath string `json:"current_path,omitempty"`
	Message     string `json:"message,omitempty"`
}

type repoPreview struct {
	Repo    string       `json:"repo"`
	Changes []fileChange `json:"changes,omitempty"`
	Error   string       `json:"error,omitempty"`
}

type previewReport struct {
	TargetRepositories int           `json:"target_repositories"`
	FilesToSync        int           `json:"files_to_sync"`
	UpdatedRepos       int           `json:"updated_repos"`
	ChangedFiles       int           `json:"changed_files"`
	Repositories       []repoPreview `json:"repositories"`
}

func main() {
	jsonOutput := flag.Bool("json", false, "Print report as JSON")
	workers := flag.Int("workers", 6, "Parallel workers")
	flag.Parse()

	ctx := context.Background()
	if _, err := ghcli.EnsureBudget(ctx, 50); err != nil {
		cli.Fatal("rate limit: %v", err)
	}

	syncPath, err := fsutil.ResolveFromRoot(syncConfigRel)
	if err != nil {
		cli.Fatal("sync config: %v", err)
	}

	config, err := parseSyncConfig(syncPath)
	if err != nil {
		cli.Fatal("parse sync config: %v", err)
	}

	plan, err := buildPlan(config)
	if err != nil {
		cli.Fatal("build sync plan: %v", err)
	}

	report := previewReport{
		TargetRepositories: len(plan.repos),
		FilesToSync:        len(plan.files),
	}

	results := previewRepos(ctx, plan, max(1, *workers))
	for _, repo := range results {
		if len(repo.Changes) > 0 {
			report.UpdatedRepos++
			report.ChangedFiles += len(repo.Changes)
		}
		report.Repositories = append(report.Repositories, repo)
	}

	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			cli.Fatal("encode json: %v", err)
		}
		return
	}

	printReport(report)

	for _, repo := range report.Repositories {
		if repo.Error != "" {
			os.Exit(1)
		}
	}
	if report.UpdatedRepos == 0 {
		fmt.Println("\nSummary: 0 repos would receive updates (0 files total)")
	}
}

type previewPlan struct {
	files []plannedFile
	repos []string
}

type plannedFile struct {
	SourcePath string
	DestPath   string
	BlobSHA    string
	Bytes      []byte
}

func buildPlan(config syncConfig) (previewPlan, error) {
	fileMap := make(map[string]plannedFile)
	repoSet := make(map[string]bool)

	for _, group := range config.Groups {
		for _, repo := range group.Repos {
			repoSet[repo] = true
		}
		for _, file := range group.Files {
			if _, exists := fileMap[file.Dest]; exists {
				continue
			}
			data, err := os.ReadFile(file.Source)
			if err != nil {
				return previewPlan{}, fmt.Errorf("read %s: %w", file.Source, err)
			}
			fileMap[file.Dest] = plannedFile{
				SourcePath: file.Source,
				DestPath:   file.Dest,
				BlobSHA:    gitBlobSHA(data),
				Bytes:      data,
			}
		}
	}

	var files []plannedFile
	for _, file := range fileMap {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].DestPath < files[j].DestPath
	})

	var repos []string
	for repo := range repoSet {
		repos = append(repos, repo)
	}
	sort.Strings(repos)

	return previewPlan{files: files, repos: repos}, nil
}

func previewRepos(ctx context.Context, plan previewPlan, workers int) []repoPreview {
	jobs := make(chan string, len(plan.repos))
	for _, repo := range plan.repos {
		jobs <- repo
	}
	close(jobs)

	results := make(chan repoPreview, len(plan.repos))
	var wg sync.WaitGroup

	for i := 0; i < workers && i < len(plan.repos); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range jobs {
				results <- previewRepo(ctx, repo, plan.files)
			}
		}()
	}

	wg.Wait()
	close(results)

	var previews []repoPreview
	for result := range results {
		previews = append(previews, result)
	}
	sort.Slice(previews, func(i, j int) bool {
		return previews[i].Repo < previews[j].Repo
	})
	return previews
}

func previewRepo(ctx context.Context, repo string, files []plannedFile) repoPreview {
	result := repoPreview{Repo: repo}
	for _, file := range files {
		content, err := downstreamFile(ctx, repo, file.DestPath)
		if err != nil {
			result.Error = err.Error()
			return result
		}
		if content == nil {
			result.Changes = append(result.Changes, fileChange{
				Source:  file.SourcePath,
				Path:    file.DestPath,
				NewSHA:  file.BlobSHA,
				Status:  "new",
				Message: "new file",
			})
			continue
		}
		if content.SHA == file.BlobSHA {
			continue
		}
		result.Changes = append(result.Changes, fileChange{
			Source:      file.SourcePath,
			Path:        file.DestPath,
			CurrentSHA:  content.SHA,
			NewSHA:      file.BlobSHA,
			Status:      "update",
			CurrentPath: content.Path,
		})
	}
	return result
}

func downstreamFile(ctx context.Context, repo, path string) (*downstreamContent, error) {
	output, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/contents/%s", repo, path))
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, nil
		}
		return nil, fmt.Errorf("%s %s: %w", repo, path, err)
	}

	var content downstreamContent
	if err := json.Unmarshal([]byte(output), &content); err != nil {
		return nil, fmt.Errorf("%s %s: parse response: %w", repo, path, err)
	}
	return &content, nil
}

func parseSyncConfig(path string) (syncConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return syncConfig{}, err
	}

	var config syncConfig
	var currentGroup *syncGroup
	var currentFile *syncFile
	inRepos := false

	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimRight(rawLine, "\r")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		if trimmed == "- files:" {
			config.Groups = append(config.Groups, syncGroup{})
			currentGroup = &config.Groups[len(config.Groups)-1]
			currentFile = nil
			inRepos = false
			continue
		}

		if currentGroup == nil {
			continue
		}

		if trimmed == "repos: |" {
			inRepos = true
			currentFile = nil
			continue
		}

		if inRepos {
			if strings.HasPrefix(line, "      ") {
				currentGroup.Repos = append(currentGroup.Repos, trimmed)
				continue
			}
			inRepos = false
		}

		if strings.HasPrefix(trimmed, "- source:") {
			currentGroup.Files = append(currentGroup.Files, syncFile{Source: strings.TrimSpace(strings.TrimPrefix(trimmed, "- source:"))})
			currentFile = &currentGroup.Files[len(currentGroup.Files)-1]
			continue
		}

		if strings.HasPrefix(trimmed, "dest:") && currentFile != nil {
			currentFile.Dest = strings.TrimSpace(strings.TrimPrefix(trimmed, "dest:"))
		}
	}

	for i, group := range config.Groups {
		if len(group.Repos) == 0 {
			return syncConfig{}, fmt.Errorf("group %d has no repos", i+1)
		}
		for _, file := range group.Files {
			if file.Source == "" || file.Dest == "" {
				return syncConfig{}, fmt.Errorf("group %d has incomplete file mapping", i+1)
			}
		}
	}

	return config, nil
}

func printReport(report previewReport) {
	fmt.Println("SYNC PREVIEW")
	fmt.Println("============")
	fmt.Printf("\nTarget Repositories: %d\n", report.TargetRepositories)
	fmt.Printf("Files to Sync: %d\n", report.FilesToSync)

	printedHeader := false
	for _, repo := range report.Repositories {
		if repo.Error != "" || len(repo.Changes) > 0 {
			if !printedHeader {
				fmt.Println("\nRepositories with changes:")
				printedHeader = true
			}
		}
		if repo.Error != "" {
			fmt.Printf("  %s\n", repo.Repo)
			fmt.Printf("    - error: %s\n", repo.Error)
			continue
		}
		if len(repo.Changes) == 0 {
			continue
		}
		fmt.Printf("  %s\n", repo.Repo)
		for _, change := range repo.Changes {
			switch change.Status {
			case "new":
				fmt.Printf("    - %s (new file, SHA: %s)\n", change.Path, shortSHA(change.NewSHA))
			default:
				fmt.Printf("    - %s (SHA: %s -> %s)\n", change.Path, shortSHA(change.CurrentSHA), shortSHA(change.NewSHA))
			}
		}
	}

	if !printedHeader {
		fmt.Println("\nRepositories with changes:\n  none")
	}

	if report.UpdatedRepos > 0 {
		fmt.Printf("\nSummary: %d repos would receive updates (%d files total)\n", report.UpdatedRepos, report.ChangedFiles)
	}
}

func gitBlobSHA(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
