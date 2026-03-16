//go:build ignore

package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"scripts/internal/cli"
	"scripts/internal/fsutil"
	"scripts/internal/ghcli"
)

const (
	syncFile             = ".github/sync.yml"
	defaultOwner         = "qws941"
	issueMarker          = "<!-- action-pin-audit -->"
	issueTitlePrefix     = "Action Pinning Audit:"
	defaultWorkerCount   = 4
	workflowDirPath      = ".github/workflows"
	reportHeading        = "ACTION PIN AUDIT"
	fingerprintMarkerFmt = "<!-- action-pin-audit:fingerprint=%s -->"
)

var (
	usesLineRe    = regexp.MustCompile(`^\s*(?:-\s*)?uses:\s*(?:"([^"]+)"|'([^']+)'|([^#\s]+))(?:\s+(#.*))?\s*$`)
	floatingTagRe = regexp.MustCompile(`^(?:[vV]?\d+(?:\.\d+)*|main|master|latest)$`)
	shaRe         = regexp.MustCompile(`^[a-f0-9]{40}$`)
	reposBlockRe  = regexp.MustCompile(`repos:\s*\|\n((?:\s{6}.+\n?)*)`)
)

type PinViolation struct {
	Repo       string
	Workflow   string
	Line       int
	Action     string
	CurrentRef string
	Violation  string
}

type repoWorkflowEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

type repoFileContent struct {
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Path     string `json:"path"`
}

type issueInfo struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	State  string `json:"state"`
	URL    string `json:"html_url"`
}

type repoScanResult struct {
	Repo          string
	WorkflowCount int
	Violations    []PinViolation
	Errors        []string
}

func main() {
	dryRun := flag.Bool("dry-run", false, "Preview issue output without creating or updating an issue")
	singleRepo := flag.String("repo", "", "Scan a single repo from the sync target set")
	workers := flag.Int("workers", defaultWorkerCount, "Parallel workers")
	flag.Parse()

	ctx := context.Background()
	if _, err := ghcli.EnsureBudget(ctx, 100); err != nil {
		cli.Fatal("rate limit: %v", err)
	}

	if *workers < 1 {
		cli.Fatal("workers must be >= 1")
	}

	upstreamRepo, err := currentRepo(ctx)
	if err != nil {
		cli.Fatal("resolve current repo: %v", err)
	}

	repos, err := targetRepos(*singleRepo)
	if err != nil {
		cli.Fatal("load target repos: %v", err)
	}

	results := scanRepos(ctx, repos, *workers)
	violations, workflowsScanned, scanErrs := summarizeResults(results)
	report := buildReport(len(repos), workflowsScanned, violations)

	fmt.Print(report)

	if len(scanErrs) > 0 {
		fmt.Println("Scan Errors:")
		for _, errMsg := range scanErrs {
			fmt.Printf("  - %s\n", errMsg)
		}
		os.Exit(1)
	}

	if *dryRun {
		fmt.Println("Dry-run: issue creation skipped.")
		return
	}

	issueURL, err := reconcileIssue(ctx, upstreamRepo, violations, report)
	if err != nil {
		cli.Fatal("reconcile issue: %v", err)
	}

	if issueURL != "" {
		fmt.Printf("Issue: %s\n", issueURL)
	} else {
		fmt.Println("Issue: none")
	}
}

func currentRepo(ctx context.Context) (string, error) {
	out, err := ghcli.Output(ctx, "repo", "view", "--json", "nameWithOwner", "-q", ".nameWithOwner")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func targetRepos(single string) ([]string, error) {
	if single != "" {
		if !strings.Contains(single, "/") {
			single = defaultOwner + "/" + single
		}
		return []string{single}, nil
	}

	syncPath, err := fsutil.ResolveFromRoot(syncFile)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(syncPath)
	if err != nil {
		return nil, err
	}

	matches := reposBlockRe.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("could not find repos block in %s", syncFile)
	}

	seen := make(map[string]bool)
	var repos []string
	for _, match := range matches {
		for _, line := range strings.Split(match[1], "\n") {
			repo := strings.TrimSpace(line)
			if repo == "" || seen[repo] {
				continue
			}
			seen[repo] = true
			repos = append(repos, repo)
		}
	}

	sort.Strings(repos)
	return repos, nil
}

func scanRepos(ctx context.Context, repos []string, workers int) []repoScanResult {
	jobs := make(chan string, len(repos))
	results := make(chan repoScanResult, len(repos))

	for _, repo := range repos {
		jobs <- repo
	}
	close(jobs)

	var wg sync.WaitGroup
	for i := 0; i < workers && i < len(repos); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range jobs {
				results <- scanRepo(ctx, repo)
			}
		}()
	}

	wg.Wait()
	close(results)

	var collected []repoScanResult
	for result := range results {
		collected = append(collected, result)
	}

	sort.Slice(collected, func(i, j int) bool {
		return collected[i].Repo < collected[j].Repo
	})
	return collected
}

func scanRepo(ctx context.Context, repo string) repoScanResult {
	result := repoScanResult{Repo: repo}

	workflowPaths, err := listWorkflowPaths(ctx, repo)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Sprintf("%s: list workflows: %v", repo, err))
		return result
	}

	result.WorkflowCount = len(workflowPaths)
	for _, workflowPath := range workflowPaths {
		content, err := fetchFileContent(ctx, repo, workflowPath)
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s/%s: fetch content: %v", repo, workflowPath, err))
			continue
		}
		result.Violations = append(result.Violations, scanWorkflow(repo, workflowPath, content)...)
	}

	return result
}

func listWorkflowPaths(ctx context.Context, repo string) ([]string, error) {
	out, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/contents/%s", repo, workflowDirPath))
	if err != nil {
		if strings.Contains(err.Error(), "404") {
			return nil, nil
		}
		return nil, err
	}

	var entries []repoWorkflowEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		return nil, err
	}

	var paths []string
	for _, entry := range entries {
		if entry.Type != "file" {
			continue
		}
		if strings.HasSuffix(entry.Name, ".yml") {
			paths = append(paths, entry.Path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func fetchFileContent(ctx context.Context, repo, path string) (string, error) {
	out, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/contents/%s", repo, path))
	if err != nil {
		return "", err
	}

	var payload repoFileContent
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return "", err
	}

	if payload.Encoding != "base64" {
		return "", fmt.Errorf("unsupported encoding %q", payload.Encoding)
	}

	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(payload.Content, "\n", ""))
	if err != nil {
		return "", err
	}
	return string(decoded), nil
}

func scanWorkflow(repo, workflowPath, content string) []PinViolation {
	var violations []PinViolation
	lines := strings.Split(content, "\n")

	for i, line := range lines {
		match := usesLineRe.FindStringSubmatch(line)
		if match == nil {
			continue
		}

		ref := firstNonEmpty(match[1], match[2], match[3])
		comment := strings.TrimSpace(match[4])
		at := strings.LastIndex(ref, "@")
		if at == -1 || at == len(ref)-1 {
			continue
		}

		action := ref[:at]
		currentRef := ref[at+1:]
		switch {
		case floatingTagRe.MatchString(currentRef):
			violations = append(violations, PinViolation{
				Repo:       repo,
				Workflow:   workflowPath,
				Line:       i + 1,
				Action:     action,
				CurrentRef: currentRef,
				Violation:  "floating_tag",
			})
		case shaRe.MatchString(currentRef) && comment == "":
			violations = append(violations, PinViolation{
				Repo:       repo,
				Workflow:   workflowPath,
				Line:       i + 1,
				Action:     action,
				CurrentRef: currentRef,
				Violation:  "missing_comment",
			})
		}
	}

	return violations
}

func summarizeResults(results []repoScanResult) ([]PinViolation, int, []string) {
	var violations []PinViolation
	var workflowsScanned int
	var errs []string

	for _, result := range results {
		workflowsScanned += result.WorkflowCount
		violations = append(violations, result.Violations...)
		errs = append(errs, result.Errors...)
	}

	sort.Slice(violations, func(i, j int) bool {
		left := fmt.Sprintf("%s/%s:%09d:%s", violations[i].Repo, violations[i].Workflow, violations[i].Line, violations[i].Action)
		right := fmt.Sprintf("%s/%s:%09d:%s", violations[j].Repo, violations[j].Workflow, violations[j].Line, violations[j].Action)
		return left < right
	})

	sort.Strings(errs)
	return violations, workflowsScanned, errs
}

func buildReport(repoCount, workflowsScanned int, violations []PinViolation) string {
	var buf bytes.Buffer
	buf.WriteString(reportHeading)
	buf.WriteString("\n================\n\n")
	fmt.Fprintf(&buf, "Repos Scanned: %d\n", repoCount)
	fmt.Fprintf(&buf, "Workflows Scanned: %d\n", workflowsScanned)
	fmt.Fprintf(&buf, "Violations Found: %d\n\n", len(violations))

	if len(violations) == 0 {
		buf.WriteString("Violations:\n  none\n\n")
		return buf.String()
	}

	buf.WriteString("Violations:\n")
	for _, violation := range violations {
		fmt.Fprintf(&buf, "  %s/%s:%d\n", violation.Repo, violation.Workflow, violation.Line)
		fmt.Fprintf(&buf, "    Action: %s@%s\n", violation.Action, violation.CurrentRef)
		fmt.Fprintf(&buf, "    Violation: %s\n\n", violation.Violation)
	}

	return buf.String()
}

func reconcileIssue(ctx context.Context, repo string, violations []PinViolation, report string) (string, error) {
	existing, err := findOpenAuditIssue(ctx, repo)
	if err != nil {
		return "", err
	}

	if len(violations) == 0 {
		if existing == nil {
			return "", nil
		}
		body := strings.Join([]string{
			issueMarker,
			"## Action Pinning Audit Resolved",
			"",
			"No action pinning violations were found in the latest audit run.",
			"",
			"_Auto-closed by `scripts/audit-action-pins.go`._",
		}, "\n")
		_, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/issues/%d", repo, existing.Number), "--method", "PATCH", "-f", "state=closed", "-f", "body="+body)
		return "", err
	}

	title := fmt.Sprintf("Action Pinning Audit: %d violations found", len(violations))
	fingerprint := buildFingerprint(violations)
	body := strings.Join([]string{
		issueMarker,
		fmt.Sprintf(fingerprintMarkerFmt, fingerprint),
		fmt.Sprintf("Generated: %s UTC", time.Now().UTC().Format("2006-01-02 15:04:05")),
		"",
		"```text",
		strings.TrimSpace(report),
		"```",
	}, "\n")

	if existing == nil {
		out, err := ghcli.Output(ctx,
			"api", fmt.Sprintf("repos/%s/issues", repo),
			"--method", "POST",
			"-f", "title="+title,
			"-f", "body="+body,
		)
		if err != nil {
			return "", err
		}
		var created issueInfo
		if err := json.Unmarshal([]byte(out), &created); err != nil {
			return "", err
		}
		return created.URL, nil
	}

	if strings.Contains(existing.Body, fmt.Sprintf(fingerprintMarkerFmt, fingerprint)) && existing.Title == title {
		return existing.URL, nil
	}

	_, err = ghcli.Output(ctx,
		"api", fmt.Sprintf("repos/%s/issues/%d", repo, existing.Number),
		"--method", "PATCH",
		"-f", "title="+title,
		"-f", "body="+body,
		"-f", "state=open",
	)
	if err != nil {
		return "", err
	}
	return existing.URL, nil
}

func findOpenAuditIssue(ctx context.Context, repo string) (*issueInfo, error) {
	out, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/issues?state=open&per_page=100", repo))
	if err != nil {
		return nil, err
	}

	var issues []issueInfo
	if err := json.Unmarshal([]byte(out), &issues); err != nil {
		return nil, err
	}

	for _, issue := range issues {
		if strings.Contains(issue.Body, issueMarker) || strings.HasPrefix(issue.Title, issueTitlePrefix) {
			copy := issue
			return &copy, nil
		}
	}
	return nil, nil
}

func buildFingerprint(violations []PinViolation) string {
	parts := make([]string, 0, len(violations))
	for _, violation := range violations {
		parts = append(parts, fmt.Sprintf("%s|%s|%d|%s|%s", violation.Repo, violation.Workflow, violation.Line, violation.Action, violation.Violation))
	}
	sort.Strings(parts)
	sum := 0
	joined := strings.Join(parts, "\n")
	for _, b := range []byte(joined) {
		sum = (sum*131 + int(b)) % 1000000007
	}
	return fmt.Sprintf("%08x", sum)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
