//go:build ignore
// +build ignore

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"scripts/internal/cli"
)

const (
	apiBaseURL     = "https://api.github.com"
	deleteMessage  = "chore: remove legacy codex workflow callers"
	defaultTimeout = 30 * time.Second
)

var legacyFiles = []string{
	".github/workflows/codex-auto-issue.yml",
	".github/workflows/codex-issue-timeout.yml",
	".github/workflows/codex-pr-normalize.yml",
	".github/workflows/codex-pr-review.yml",
	".github/workflows/codex-triage.yml",
}

var downstreamRepos = []string{
	"qws941/blacklist",
	"qws941/hycu",
	"qws941/hycu_fsds",
	"qws941/opencode",
	"qws941/propose",
	"qws941/qws941",
	"qws941/resume",
	"qws941/safetywallet",
	"qws941/splunk",
	"qws941/terraform",
	"qws941/tmux",
	"qws941/youtube",
}

type apiError struct {
	StatusCode int
	Method     string
	URL        string
	Body       string
}

func (e *apiError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		body = "(empty body)"
	}
	return fmt.Sprintf("GitHub API %s %s failed: status=%d body=%s", e.Method, e.URL, e.StatusCode, body)
}

type getContentResponse struct {
	SHA string `json:"sha"`
}

type deleteContentRequest struct {
	Message string `json:"message"`
	SHA     string `json:"sha"`
}

type deleteSummary struct {
	Deleted int
	Skipped int
	Errors  int
}

func main() {
	dryRun := flag.Bool("dry-run", true, "Preview deletions without applying")
	singleRepo := flag.String("repo", "", "Target single repo (owner/repo)")
	flag.Parse()

	targetRepos, err := resolveTargetRepos(*singleRepo)
	if err != nil {
		cli.Fatal("invalid repo target: %v", err)
	}

	token := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GH_PAT"))
	}
	if token == "" && !*dryRun {
		cli.Fatal("missing token: set GITHUB_TOKEN or GH_PAT")
	}

	mode := "DRY-RUN"
	if !*dryRun {
		mode = "EXECUTE"
	}

	totalPotential := len(targetRepos) * len(legacyFiles)
	fmt.Printf("Mode: %s\n", mode)
	fmt.Printf("Target repos: %d\n", len(targetRepos))
	fmt.Printf("Legacy files: %d\n", len(legacyFiles))
	fmt.Printf("Potential deletions: %d\n\n", totalPotential)

	client := &http.Client{Timeout: defaultTimeout}
	summary := deleteSummary{}

	for _, repo := range targetRepos {
		fmt.Printf("[%s]\n", repo)
		for _, filePath := range legacyFiles {
			if *dryRun {
				fmt.Printf("  [dry-run] would delete %s\n", filePath)
				continue
			}

			sha, err := getFileSHA(client, token, repo, filePath)
			if err != nil {
				var apiErr *apiError
				if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
					fmt.Printf("  [skip] %s (not found)\n", filePath)
					summary.Skipped++
					continue
				}
				fmt.Printf("  [error] %s (%v)\n", filePath, err)
				summary.Errors++
				continue
			}

			if err := deleteFile(client, token, repo, filePath, sha); err != nil {
				var apiErr *apiError
				if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
					fmt.Printf("  [skip] %s (not found)\n", filePath)
					summary.Skipped++
					continue
				}
				fmt.Printf("  [error] %s (%v)\n", filePath, err)
				summary.Errors++
				continue
			}

			fmt.Printf("  [deleted] %s\n", filePath)
			summary.Deleted++
		}
		fmt.Println()
	}

	if *dryRun {
		fmt.Printf("Summary: deleted %d files across %d repos, skipped %d (not found)\n", 0, len(targetRepos), 0)
		fmt.Printf("Dry-run potential deletions: %d\n", totalPotential)
		return
	}

	fmt.Printf("Summary: deleted %d files across %d repos, skipped %d (not found)\n", summary.Deleted, len(targetRepos), summary.Skipped)
	if summary.Errors > 0 {
		os.Exit(1)
	}
}

func resolveTargetRepos(singleRepo string) ([]string, error) {
	if singleRepo == "" {
		out := make([]string, len(downstreamRepos))
		copy(out, downstreamRepos)
		return out, nil
	}

	if !strings.Contains(singleRepo, "/") {
		return nil, fmt.Errorf("expected owner/repo, got %q", singleRepo)
	}

	for _, repo := range downstreamRepos {
		if repo == singleRepo {
			return []string{singleRepo}, nil
		}
	}

	return nil, fmt.Errorf("repo %q is not in downstream sync targets", singleRepo)
}

func getFileSHA(client *http.Client, token, repo, filePath string) (string, error) {
	apiURL := fmt.Sprintf("%s/repos/%s/contents/%s", apiBaseURL, repo, encodeContentPath(filePath))
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return "", err
	}
	setCommonHeaders(req, token)

	body, statusCode, err := doRequest(client, req)
	if err != nil {
		return "", err
	}
	if statusCode != http.StatusOK {
		return "", &apiError{StatusCode: statusCode, Method: http.MethodGet, URL: apiURL, Body: string(body)}
	}

	var parsed getContentResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("decode GET response: %w", err)
	}
	if parsed.SHA == "" {
		return "", fmt.Errorf("GET response missing sha for %s in %s", filePath, repo)
	}

	return parsed.SHA, nil
}

func deleteFile(client *http.Client, token, repo, filePath, sha string) error {
	apiURL := fmt.Sprintf("%s/repos/%s/contents/%s", apiBaseURL, repo, encodeContentPath(filePath))
	payload := deleteContentRequest{
		Message: deleteMessage,
		SHA:     sha,
	}

	raw, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode DELETE payload: %w", err)
	}

	req, err := http.NewRequest(http.MethodDelete, apiURL, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	setCommonHeaders(req, token)
	req.Header.Set("Content-Type", "application/json")

	body, statusCode, err := doRequest(client, req)
	if err != nil {
		return err
	}
	if statusCode != http.StatusOK {
		return &apiError{StatusCode: statusCode, Method: http.MethodDelete, URL: apiURL, Body: string(body)}
	}

	return nil
}

func doRequest(client *http.Client, req *http.Request) ([]byte, int, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}

	return body, resp.StatusCode, nil
}

func setCommonHeaders(req *http.Request, token string) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "remove-legacy-synced-files")
}

func encodeContentPath(filePath string) string {
	parts := strings.Split(filePath, "/")
	for i := range parts {
		parts[i] = urlPathEscape(parts[i])
	}
	return path.Join(parts...)
}

func urlPathEscape(s string) string {
	replacer := strings.NewReplacer(
		"%", "%25",
		" ", "%20",
		"#", "%23",
		"?", "%3F",
	)
	return replacer.Replace(s)
}
