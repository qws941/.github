// scripts/sync-labels.go — Sync standard labels from labels.yml to GitHub repos
//
// Replaces scripts/sync-labels.sh (bash+python3). Stdlib-only, no external deps.
//
// Usage:
//
//	go run scripts/sync-labels.go [flags]
//	go run scripts/sync-labels.go --repo qws941/opencode
//	go run scripts/sync-labels.go --dry-run --delete
//
// Flags:
//
//	--dry-run   Preview changes without applying
//	--repo      Sync a single repo (default: all qws941 repos)
//	--delete    Remove labels not defined in labels.yml
//	--workers   Parallel workers (default: 4)
//	--verbose   Enable verbose logging
package main

import (
	"context"
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
	"scripts/internal/labels"
)

const (
	labelsFile = "scripts/labels.yml"
	org        = "qws941"
)

type repoResult struct {
	Repo    string
	Created int
	Updated int
	Deleted int
	Skipped int
	Total   int
	Errors  []string
}

func main() {
	dryRun := flag.Bool("dry-run", false, "Preview changes without applying")
	singleRepo := flag.String("repo", "", "Sync single repo (owner/repo)")
	deleteStale := flag.Bool("delete", false, "Remove labels not in labels.yml")
	workers := flag.Int("workers", 4, "Parallel workers")
	verbose := flag.Bool("verbose", false, "Enable verbose logging")
	flag.Parse()

	ctx := context.Background()
	if _, err := ghcli.EnsureBudget(ctx, 200); err != nil {
		cli.Fatal("rate limit: %v", err)
	}

	labelsPath, err := fsutil.ResolveFromRoot(labelsFile)
	if err != nil {
		cli.Fatal("labels.yml: %v", err)
	}
	standard, err := labels.ParseFile(labelsPath)
	if err != nil {
		cli.Fatal("parse labels.yml: %v", err)
	}
	logVerbose(*verbose, "Loaded %d labels from %s", len(standard), labelsPath)
	fmt.Printf("Loaded %d standard labels from %s\n", len(standard), labelsFile)

	repos, err := targetRepos(ctx, *singleRepo)
	if err != nil {
		cli.Fatal("list repos: %v", err)
	}
	logVerbose(*verbose, "Found %d repositories", len(repos))
	fmt.Printf("Targets: %d repo(s)\n\n", len(repos))

	mode := "EXECUTE"
	if *dryRun {
		mode = "DRY-RUN"
	}
	fmt.Printf("Mode: %s", mode)
	if *deleteStale {
		fmt.Print(" + DELETE-STALE")
	}
	fmt.Println()

	// Fan-out with worker pool.
	ch := make(chan string, len(repos))
	for _, r := range repos {
		ch <- r
	}
	close(ch)

	var mu sync.Mutex
	var results []repoResult
	var wg sync.WaitGroup

	for i := 0; i < *workers && i < len(repos); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range ch {
				logVerbose(*verbose, "Processing repo: %s", repo)
				res := syncRepo(ctx, repo, standard, *dryRun, *deleteStale, *verbose)
				mu.Lock()
				results = append(results, res)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	// Sort results by repo name for deterministic output.
	sort.Slice(results, func(i, j int) bool {
		return results[i].Repo < results[j].Repo
	})

	printSummary(results)

	hasErrors := false
	for _, r := range results {
		if len(r.Errors) > 0 {
			hasErrors = true
			break
		}
	}
	if hasErrors {
		os.Exit(1)
	}
}

// syncRepo syncs labels for a single repo and returns the result.
func syncRepo(ctx context.Context, repo string, standard []labels.Label, dryRun, deleteStale, verbose bool) repoResult {
	res := repoResult{Repo: repo, Total: len(standard)}

	logVerbose(verbose, "Syncing labels for %s", repo)

	// Fetch existing labels.
	out, err := ghcli.Output(ctx, "label", "list", "--repo", repo, "--json", "name,color,description", "--limit", "200")
	if err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("list: %v", err))
		return res
	}

	var existing []struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(out), &existing); err != nil {
		res.Errors = append(res.Errors, fmt.Sprintf("parse: %v", err))
		return res
	}

	type labelInfo struct{ Color, Desc string }
	exMap := make(map[string]labelInfo)
	for _, l := range existing {
		exMap[l.Name] = labelInfo{l.Color, l.Description}
	}

	stdNames := make(map[string]bool)

	// Create/update standard labels.
	for _, l := range standard {
		stdNames[l.Name] = true

		ex, exists := exMap[l.Name]
		if exists && strings.EqualFold(ex.Color, l.Color) && ex.Desc == l.Description {
			res.Skipped++
			continue
		}

		var ghArgs []string
		if exists {
			ghArgs = []string{"label", "edit", l.Name, "--repo", repo, "--color", l.Color, "--description", l.Description}
		} else {
			ghArgs = []string{"label", "create", l.Name, "--repo", repo, "--color", l.Color, "--description", l.Description}
		}

		if dryRun {
			if exists {
				res.Updated++
			} else {
				res.Created++
			}
			continue
		}

		if _, err := ghcli.Output(ctx, ghArgs...); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", l.Name, err))
			continue
		}

		action := "Created"
		if exists {
			action = "Updated"
			res.Updated++
		} else {
			res.Created++
		}
		logVerbose(verbose, "%s label '%s' in %s", action, l.Name, repo)
	}

	// Delete stale labels (not in labels.yml).
	if deleteStale {
		for _, l := range existing {
			if stdNames[l.Name] {
				continue
			}
			if dryRun {
				res.Deleted++
				continue
			}
			if _, err := ghcli.Output(ctx, "label", "delete", l.Name, "--repo", repo, "--yes"); err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("delete %s: %v", l.Name, err))
				continue
			}
			logVerbose(verbose, "Deleted label '%s' from %s", l.Name, repo)
			res.Deleted++
		}
	}

	return res
}

func targetRepos(ctx context.Context, single string) ([]string, error) {
	if single != "" {
		if !strings.Contains(single, "/") {
			single = org + "/" + single
		}
		return []string{single}, nil
	}

	out, err := ghcli.Output(ctx, "repo", "list", org, "--json", "nameWithOwner", "--limit", "100", "-q", ".[].nameWithOwner")
	if err != nil {
		return nil, err
	}

	var repos []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			repos = append(repos, line)
		}
	}
	sort.Strings(repos)
	return repos, nil
}

func printSummary(results []repoResult) {
	fmt.Println("\n╔══════════════════════════════════════╤═════════╤═════════╤═════════╤═════════╤════════╗")
	fmt.Println("║ Repository                           │ Created │ Updated │ Deleted │ Skipped │ Errors ║")
	fmt.Println("╠══════════════════════════════════════╪═════════╪═════════╪═════════╪═════════╪════════╣")

	totalC, totalU, totalD, totalS, totalE := 0, 0, 0, 0, 0
	for _, r := range results {
		repo := r.Repo
		if len(repo) > 36 {
			repo = repo[:36]
		}
		errCount := len(r.Errors)
		errStr := fmt.Sprintf("%d", errCount)
		if errCount > 0 {
			errStr = fmt.Sprintf("%d ✗", errCount)
		}
		fmt.Printf("║ %-36s │ %7d │ %7d │ %7d │ %7d │ %6s ║\n",
			repo, r.Created, r.Updated, r.Deleted, r.Skipped, errStr)
		totalC += r.Created
		totalU += r.Updated
		totalD += r.Deleted
		totalS += r.Skipped
		totalE += errCount
	}

	fmt.Println("╠══════════════════════════════════════╪═════════╪═════════╪═════════╪═════════╪════════╣")
	totalEStr := fmt.Sprintf("%d", totalE)
	if totalE > 0 {
		totalEStr = fmt.Sprintf("%d ✗", totalE)
	}
	fmt.Printf("║ %-36s │ %7d │ %7d │ %7d │ %7d │ %6s ║\n",
		fmt.Sprintf("TOTAL (%d repos)", len(results)), totalC, totalU, totalD, totalS, totalEStr)
	fmt.Println("╚══════════════════════════════════════╧═════════╧═════════╧═════════╧═════════╧════════╝")

	// Print per-repo errors.
	for _, r := range results {
		if len(r.Errors) > 0 {
			fmt.Printf("\n  %s errors:\n", r.Repo)
			for _, e := range r.Errors {
				fmt.Printf("    ✗ %s\n", e)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// logVerbose prints verbose log messages to stderr when verbose mode is enabled.
func logVerbose(verbose bool, format string, v ...interface{}) {
	if verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] "+format+"\n", v...)
	}
}
