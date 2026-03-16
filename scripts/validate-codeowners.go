//go:build ignore

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"

	"scripts/internal/cli"
	"scripts/internal/fsutil"
	"scripts/internal/ghcli"
)

const (
	syncConfigPath = ".github/sync.yml"
	issueRepo      = "qws941/.github"
)

var codeownersPaths = []string{
	".github/CODEOWNERS",
	"CODEOWNERS",
	"docs/CODEOWNERS",
}

var (
	ownerUserPattern = regexp.MustCompile(`^@[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	ownerTeamPattern = regexp.MustCompile(`^@([A-Za-z0-9][A-Za-z0-9-]*)/([A-Za-z0-9_.-]+)$`)
	emailPattern     = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)
	repoBlockPattern = regexp.MustCompile(`repos:\s*\|\n((?:\s{6}.+\n?)*)`)
)

type repoResult struct {
	Repo           string
	CodeownersPath string
	Missing        bool
	Findings       []finding
}

type finding struct {
	Line   int
	Kind   string
	Detail string
}

type codeownersEntry struct {
	Line    int
	Pattern string
	Owners  []string
	Matcher *regexp.Regexp
}

type repoMetadata struct {
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
}

type treeResponse struct {
	Tree []struct {
		Path string `json:"path"`
		Type string `json:"type"`
	} `json:"tree"`
}

type codeownersErrorsResponse struct {
	Errors []struct {
		Line    int    `json:"line"`
		Column  int    `json:"column"`
		Kind    string `json:"kind"`
		Message string `json:"message"`
		Path    string `json:"path"`
	} `json:"errors"`
}

type issueRecord struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
}

type ownerValidator struct {
	mu                sync.Mutex
	userExists        map[string]bool
	userAccess        map[string]bool
	teamExists        map[string]bool
	teamRepoAccess    map[string]bool
	repoFiles         map[string][]string
	repoFilesErr      map[string]error
	repoMetadata      map[string]repoMetadata
	repoMetadataErr   map[string]error
	codeownersIssueMu sync.Mutex
}

func main() {
	dryRun := flag.Bool("dry-run", false, "Validate without creating, updating, or closing issues")
	singleRepo := flag.String("repo", "", "Validate a single repo (owner/repo)")
	workers := flag.Int("workers", 4, "Concurrent repo validations")
	flag.Parse()

	ctx := context.Background()
	if _, err := ghcli.EnsureBudget(ctx, 200); err != nil {
		cli.Fatal("rate limit: %v", err)
	}

	repos, err := targetRepos(*singleRepo)
	if err != nil {
		cli.Fatal("load repos: %v", err)
	}

	mode := "EXECUTE"
	if *dryRun {
		mode = "DRY-RUN"
	}

	fmt.Printf("Validate CODEOWNERS [%s]\n", mode)
	fmt.Printf("Targets: %d repo(s)\n\n", len(repos))

	validator := &ownerValidator{
		userExists:      make(map[string]bool),
		userAccess:      make(map[string]bool),
		teamExists:      make(map[string]bool),
		teamRepoAccess:  make(map[string]bool),
		repoFiles:       make(map[string][]string),
		repoFilesErr:    make(map[string]error),
		repoMetadata:    make(map[string]repoMetadata),
		repoMetadataErr: make(map[string]error),
	}

	results := validateRepos(ctx, repos, validator, *workers)
	printSummary(results)

	if err := syncIssues(ctx, results, *dryRun); err != nil {
		cli.Fatal("sync issues: %v", err)
	}
}

func validateRepos(ctx context.Context, repos []string, validator *ownerValidator, workers int) []repoResult {
	queue := make(chan string, len(repos))
	for _, repo := range repos {
		queue <- repo
	}
	close(queue)

	var mu sync.Mutex
	results := make([]repoResult, 0, len(repos))
	var wg sync.WaitGroup

	if workers < 1 {
		workers = 1
	}
	if workers > len(repos) && len(repos) > 0 {
		workers = len(repos)
	}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for repo := range queue {
				result := validateRepo(ctx, repo, validator)
				mu.Lock()
				results = append(results, result)
				mu.Unlock()
			}
		}()
	}

	wg.Wait()
	sort.Slice(results, func(i, j int) bool {
		return results[i].Repo < results[j].Repo
	})
	return results
}

func validateRepo(ctx context.Context, repo string, validator *ownerValidator) repoResult {
	result := repoResult{Repo: repo}

	meta, err := validator.metadata(ctx, repo)
	if err != nil {
		result.Findings = append(result.Findings, finding{Kind: "repo-api-error", Detail: err.Error()})
		return result
	}

	codeownersPath, content, found, err := fetchCodeowners(ctx, repo, meta.DefaultBranch)
	if err != nil {
		result.Findings = append(result.Findings, finding{Kind: "codeowners-read-error", Detail: err.Error()})
		return result
	}
	if !found {
		result.Missing = true
		return result
	}
	result.CodeownersPath = codeownersPath

	findings := make([]finding, 0)
	entries, parseFindings := parseCodeowners(content)
	findings = append(findings, parseFindings...)

	syntaxFindings, err := fetchCodeownersErrors(ctx, repo)
	if err != nil {
		findings = append(findings, finding{Kind: "syntax-api-error", Detail: err.Error()})
	} else {
		findings = append(findings, syntaxFindings...)
	}

	files, err := validator.files(ctx, repo, meta.DefaultBranch)
	if err != nil {
		findings = append(findings, finding{Kind: "tree-read-error", Detail: err.Error()})
	} else {
		for _, entry := range entries {
			if entry.Matcher != nil && !matchesAny(entry.Matcher, files) {
				findings = append(findings, finding{
					Line:   entry.Line,
					Kind:   "unreachable-pattern",
					Detail: fmt.Sprintf("Pattern %q does not match any tracked file.", entry.Pattern),
				})
			}
		}
	}

	for _, entry := range entries {
		for _, owner := range entry.Owners {
			ownerFindings := validator.validateOwner(ctx, repo, meta, owner, entry.Line)
			findings = append(findings, ownerFindings...)
		}
	}

	result.Findings = dedupeFindings(findings)
	return result
}

func parseCodeowners(content string) ([]codeownersEntry, []finding) {
	var entries []codeownersEntry
	var findings []finding

	for index, raw := range strings.Split(content, "\n") {
		lineNumber := index + 1
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		fields := strings.Fields(raw)
		fields = fieldsBeforeComment(fields)
		if len(fields) < 2 {
			findings = append(findings, finding{
				Line:   lineNumber,
				Kind:   "missing-owner",
				Detail: "Each CODEOWNERS entry must include a pattern and at least one owner.",
			})
			continue
		}

		pattern := fields[0]
		matcher, err := compilePattern(pattern)
		if err != nil {
			findings = append(findings, finding{Line: lineNumber, Kind: "invalid-pattern", Detail: err.Error()})
		} else {
			entries = append(entries, codeownersEntry{
				Line:    lineNumber,
				Pattern: pattern,
				Owners:  fields[1:],
				Matcher: matcher,
			})
		}

		for _, owner := range fields[1:] {
			if ownerKind(owner) == "invalid" {
				findings = append(findings, finding{
					Line:   lineNumber,
					Kind:   "invalid-owner-format",
					Detail: fmt.Sprintf("Owner %q must be @username, @org/team, or email@domain.com.", owner),
				})
			}
		}
	}

	return entries, findings
}

func fieldsBeforeComment(fields []string) []string {
	for i, field := range fields {
		if strings.HasPrefix(field, "#") {
			return fields[:i]
		}
	}
	return fields
}

func compilePattern(pattern string) (*regexp.Regexp, error) {
	negated := false
	if strings.HasPrefix(pattern, "!") {
		negated = true
		pattern = strings.TrimPrefix(pattern, "!")
	}
	if pattern == "" {
		return nil, fmt.Errorf("Pattern cannot be empty.")
	}
	if negated && pattern == "" {
		return nil, fmt.Errorf("Negation pattern must include a path after '!'.")
	}
	if strings.ContainsAny(pattern, "[]{}") {
		return nil, fmt.Errorf("Pattern %q uses unsupported syntax; supported forms are paths, *, **, and leading !.", pattern)
	}
	if strings.Contains(pattern, "!") {
		return nil, fmt.Errorf("Pattern %q contains '!' outside the leading negation position.", pattern)
	}

	anchored := strings.HasPrefix(pattern, "/")
	if anchored {
		pattern = strings.TrimPrefix(pattern, "/")
	}

	directoryOnly := strings.HasSuffix(pattern, "/")
	if directoryOnly {
		pattern = strings.TrimSuffix(pattern, "/")
	}
	if pattern == "" {
		return nil, fmt.Errorf("Pattern cannot resolve to the repository root only.")
	}

	var builder strings.Builder
	if anchored {
		builder.WriteString("^")
	} else {
		builder.WriteString(`(?:^|.*/)`)
	}

	for i := 0; i < len(pattern); i++ {
		if pattern[i] == '*' {
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				builder.WriteString(`.*`)
				i++
				continue
			}
			builder.WriteString(`[^/]*`)
			continue
		}
		if pattern[i] == '?' {
			builder.WriteString(`[^/]`)
			continue
		}
		builder.WriteString(regexp.QuoteMeta(string(pattern[i])))
	}

	if directoryOnly {
		builder.WriteString(`(?:/.*)?$`)
	} else {
		builder.WriteString("$")
	}

	compiled, err := regexp.Compile(builder.String())
	if err != nil {
		return nil, fmt.Errorf("Pattern %q could not be compiled: %v", pattern, err)
	}
	return compiled, nil
}

func ownerKind(owner string) string {
	switch {
	case ownerUserPattern.MatchString(owner):
		return "user"
	case ownerTeamPattern.MatchString(owner):
		return "team"
	case emailPattern.MatchString(owner):
		return "email"
	default:
		return "invalid"
	}
}

func (validator *ownerValidator) validateOwner(ctx context.Context, repo string, meta repoMetadata, owner string, line int) []finding {
	switch ownerKind(owner) {
	case "user":
		username := strings.TrimPrefix(owner, "@")
		return validator.validateUserOwner(ctx, repo, username, line)
	case "team":
		match := ownerTeamPattern.FindStringSubmatch(owner)
		if len(match) != 3 {
			return nil
		}
		return validator.validateTeamOwner(ctx, repo, meta, owner, match[1], match[2], line)
	default:
		return nil
	}
}

func (validator *ownerValidator) validateUserOwner(ctx context.Context, repo, username string, line int) []finding {
	findings := make([]finding, 0)

	exists, err := validator.cachedUserExists(ctx, username)
	if err != nil {
		return []finding{{Line: line, Kind: "user-check-error", Detail: fmt.Sprintf("Failed to verify user @%s: %v", username, err)}}
	}
	if !exists {
		return []finding{{Line: line, Kind: "missing-user", Detail: fmt.Sprintf("User @%s does not exist.", username)}}
	}

	access, err := validator.cachedUserAccess(ctx, repo, username)
	if err != nil {
		findings = append(findings, finding{Line: line, Kind: "user-access-check-error", Detail: fmt.Sprintf("Failed to verify repository access for @%s: %v", username, err)})
		return findings
	}
	if !access {
		findings = append(findings, finding{Line: line, Kind: "user-no-access", Detail: fmt.Sprintf("User @%s does not have repository access to %s.", username, repo)})
	}

	return findings
}

func (validator *ownerValidator) validateTeamOwner(ctx context.Context, repo string, meta repoMetadata, owner, teamOrg, teamSlug string, line int) []finding {
	if meta.Owner.Type != "Organization" {
		return []finding{{
			Line:   line,
			Kind:   "team-on-non-org-repo",
			Detail: fmt.Sprintf("Owner %s cannot be resolved because %s is not organization-owned.", owner, repo),
		}}
	}
	if teamOrg != meta.Owner.Login {
		return []finding{{
			Line:   line,
			Kind:   "team-org-mismatch",
			Detail: fmt.Sprintf("Owner %s belongs to %s, but %s is owned by %s.", owner, teamOrg, repo, meta.Owner.Login),
		}}
	}

	exists, err := validator.cachedTeamExists(ctx, teamOrg, teamSlug)
	if err != nil {
		return []finding{{Line: line, Kind: "team-check-error", Detail: fmt.Sprintf("Failed to verify team %s: %v", owner, err)}}
	}
	if !exists {
		return []finding{{Line: line, Kind: "missing-team", Detail: fmt.Sprintf("Team %s does not exist.", owner)}}
	}

	access, err := validator.cachedTeamRepoAccess(ctx, teamOrg, teamSlug, repo)
	if err != nil {
		return []finding{{Line: line, Kind: "team-access-check-error", Detail: fmt.Sprintf("Failed to verify repository access for %s: %v", owner, err)}}
	}
	if !access {
		return []finding{{Line: line, Kind: "team-no-access", Detail: fmt.Sprintf("Team %s does not have repository access to %s.", owner, repo)}}
	}

	return nil
}

func (validator *ownerValidator) metadata(ctx context.Context, repo string) (repoMetadata, error) {
	validator.mu.Lock()
	meta, ok := validator.repoMetadata[repo]
	err, hasErr := validator.repoMetadataErr[repo]
	validator.mu.Unlock()
	if ok || hasErr {
		return meta, err
	}

	var fetched repoMetadata
	out, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s", repo))
	if err == nil && out != "" {
		_ = json.Unmarshal([]byte(out), &fetched)
	}

	validator.mu.Lock()
	defer validator.mu.Unlock()
	validator.repoMetadata[repo] = fetched
	validator.repoMetadataErr[repo] = err
	return fetched, err
}

func (validator *ownerValidator) files(ctx context.Context, repo, branch string) ([]string, error) {
	validator.mu.Lock()
	files, ok := validator.repoFiles[repo]
	err, hasErr := validator.repoFilesErr[repo]
	validator.mu.Unlock()
	if ok || hasErr {
		return files, err
	}

	var tree treeResponse
	out, fetchErr := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/git/trees/%s?recursive=1", repo, branch))
	if fetchErr == nil && out != "" {
		_ = json.Unmarshal([]byte(out), &tree)
	}
	err = fetchErr
	if err == nil {
		files = make([]string, 0, len(tree.Tree))
		for _, node := range tree.Tree {
			if node.Type == "blob" {
				files = append(files, node.Path)
			}
		}
		sort.Strings(files)
	}

	validator.mu.Lock()
	defer validator.mu.Unlock()
	validator.repoFiles[repo] = files
	validator.repoFilesErr[repo] = err
	return files, err
}

func (validator *ownerValidator) cachedUserExists(ctx context.Context, username string) (bool, error) {
	validator.mu.Lock()
	value, ok := validator.userExists[username]
	validator.mu.Unlock()
	if ok {
		return value, nil
	}

	_, err := ghcli.Output(ctx, "api", fmt.Sprintf("users/%s", username))
	if err != nil {
		if isNotFound(err) {
			validator.mu.Lock()
			validator.userExists[username] = false
			validator.mu.Unlock()
			return false, nil
		}
		return false, err
	}

	validator.mu.Lock()
	validator.userExists[username] = true
	validator.mu.Unlock()
	return true, nil
}

func (validator *ownerValidator) cachedUserAccess(ctx context.Context, repo, username string) (bool, error) {
	key := repo + "|" + username
	validator.mu.Lock()
	value, ok := validator.userAccess[key]
	validator.mu.Unlock()
	if ok {
		return value, nil
	}

	_, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/collaborators/%s/permission", repo, username))
	if err != nil {
		if isNotFound(err) {
			validator.mu.Lock()
			validator.userAccess[key] = false
			validator.mu.Unlock()
			return false, nil
		}
		return false, err
	}

	validator.mu.Lock()
	validator.userAccess[key] = true
	validator.mu.Unlock()
	return true, nil
}

func (validator *ownerValidator) cachedTeamExists(ctx context.Context, org, slug string) (bool, error) {
	key := org + "/" + slug
	validator.mu.Lock()
	value, ok := validator.teamExists[key]
	validator.mu.Unlock()
	if ok {
		return value, nil
	}

	_, err := ghcli.Output(ctx, "api", fmt.Sprintf("orgs/%s/teams/%s", org, slug))
	if err != nil {
		if isNotFound(err) {
			validator.mu.Lock()
			validator.teamExists[key] = false
			validator.mu.Unlock()
			return false, nil
		}
		return false, err
	}

	validator.mu.Lock()
	validator.teamExists[key] = true
	validator.mu.Unlock()
	return true, nil
}

func (validator *ownerValidator) cachedTeamRepoAccess(ctx context.Context, org, slug, repo string) (bool, error) {
	key := org + "/" + slug + "|" + repo
	validator.mu.Lock()
	value, ok := validator.teamRepoAccess[key]
	validator.mu.Unlock()
	if ok {
		return value, nil
	}

	_, err := ghcli.Output(ctx, "api", fmt.Sprintf("orgs/%s/teams/%s/repos/%s", org, slug, repo))
	if err != nil {
		if isNotFound(err) {
			validator.mu.Lock()
			validator.teamRepoAccess[key] = false
			validator.mu.Unlock()
			return false, nil
		}
		return false, err
	}

	validator.mu.Lock()
	validator.teamRepoAccess[key] = true
	validator.mu.Unlock()
	return true, nil
}

func fetchCodeowners(ctx context.Context, repo, branch string) (string, string, bool, error) {
	for _, path := range codeownersPaths {
		out, err := ghcli.Output(ctx, "api", "-H", "Accept: application/vnd.github.raw+json", fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo, url.PathEscape(path), url.QueryEscape(branch)))
		if err == nil {
			return path, out, true, nil
		}
		if isNotFound(err) {
			continue
		}
		return "", "", false, err
	}
	return "", "", false, nil
}

func fetchCodeownersErrors(ctx context.Context, repo string) ([]finding, error) {
	var response codeownersErrorsResponse
	out, err := ghcli.Output(ctx, "api", fmt.Sprintf("repos/%s/codeowners/errors", repo))
	if err != nil {
		if isNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if out != "" {
		if jsonErr := json.Unmarshal([]byte(out), &response); jsonErr != nil {
			return nil, fmt.Errorf("parse JSON: %w", jsonErr)
		}
	}

	findings := make([]finding, 0, len(response.Errors))
	for _, item := range response.Errors {
		detail := item.Message
		if item.Path != "" {
			detail = fmt.Sprintf("%s (%s)", item.Message, item.Path)
		}
		kind := item.Kind
		if kind == "" {
			kind = "syntax-error"
		}
		findings = append(findings, finding{Line: item.Line, Kind: kind, Detail: detail})
	}
	return findings, nil
}

func targetRepos(single string) ([]string, error) {
	if single != "" {
		if !strings.Contains(single, "/") {
			single = "qws941/" + single
		}
		return []string{single}, nil
	}

	syncPath, err := fsutil.ResolveFromRoot(syncConfigPath)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(syncPath)
	if err != nil {
		return nil, err
	}

	matches := repoBlockPattern.FindAllStringSubmatch(string(data), -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf("could not parse downstream repos from %s", syncConfigPath)
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

func syncIssues(ctx context.Context, results []repoResult, dryRun bool) error {
	openIssues, err := listOpenIssues(ctx)
	if err != nil {
		return err
	}

	issueByRepo := make(map[string]issueRecord)
	for _, issue := range openIssues {
		repo := issueRepoMarker(issue.Body)
		if repo != "" {
			issueByRepo[repo] = issue
		}
	}

	for _, result := range results {
		existing, hasIssue := issueByRepo[result.Repo]
		if len(result.Findings) == 0 {
			if !hasIssue {
				continue
			}
			if dryRun {
				fmt.Printf("[dry-run] Would close issue #%d for %s\n", existing.Number, result.Repo)
				continue
			}
			comment := fmt.Sprintf("CODEOWNERS validation recovered for `%s`. No violations remain.", result.Repo)
			if _, err := ghcli.Output(ctx, "issue", "close", fmt.Sprintf("%d", existing.Number), "--repo", issueRepo, "--comment", comment); err != nil {
				return err
			}
			fmt.Printf("[closed] %s -> issue #%d\n", result.Repo, existing.Number)
			continue
		}

		title := fmt.Sprintf("CODEOWNERS Validation Failed: %s", result.Repo)
		body, fingerprint := buildIssueBody(result)
		if hasIssue && issueFingerprint(existing.Body) == fingerprint {
			fmt.Printf("[unchanged] %s -> issue #%d\n", result.Repo, existing.Number)
			continue
		}

		if dryRun {
			if hasIssue {
				fmt.Printf("[dry-run] Would update issue #%d for %s\n", existing.Number, result.Repo)
			} else {
				fmt.Printf("[dry-run] Would create issue for %s\n", result.Repo)
			}
			continue
		}

		if hasIssue {
			if _, err := ghcli.Output(ctx, "issue", "edit", fmt.Sprintf("%d", existing.Number), "--repo", issueRepo, "--title", title, "--body", body); err != nil {
				return err
			}
			fmt.Printf("[updated] %s -> issue #%d\n", result.Repo, existing.Number)
			continue
		}

		if _, err := ghcli.Output(ctx, "issue", "create", "--repo", issueRepo, "--title", title, "--body", body); err != nil {
			return err
		}
		fmt.Printf("[created] %s\n", result.Repo)
	}

	return nil
}

func listOpenIssues(ctx context.Context) ([]issueRecord, error) {
	out, err := ghcli.Output(ctx, "issue", "list", "--repo", issueRepo, "--state", "open", "--limit", "100", "--json", "number,title,body")
	if err != nil {
		return nil, err
	}
	var issues []issueRecord
	if out != "" {
		if jsonErr := json.Unmarshal([]byte(out), &issues); jsonErr != nil {
			return nil, fmt.Errorf("parse JSON: %w", jsonErr)
		}
	}
	return issues, nil
}

func buildIssueBody(result repoResult) (string, string) {
	fingerprint := findingsFingerprint(result.Findings)
	lines := []string{
		fmt.Sprintf("<!-- codeowners-validation:repo=%s -->", result.Repo),
		fmt.Sprintf("<!-- codeowners-validation:fingerprint=%s -->", fingerprint),
		"## CODEOWNERS Validation",
		"",
		fmt.Sprintf("Repo: `%s`", result.Repo),
	}
	if result.CodeownersPath != "" {
		lines = append(lines, fmt.Sprintf("Path: `%s`", result.CodeownersPath))
	}
	lines = append(lines, "", "### Violations")
	for _, item := range result.Findings {
		location := "Repo"
		if item.Line > 0 {
			location = fmt.Sprintf("Line %d", item.Line)
		}
		lines = append(lines, fmt.Sprintf("- %s: %s - %s", location, item.Kind, item.Detail))
	}
	lines = append(lines, "", "---", "*Auto-generated by Validate CODEOWNERS*")
	return strings.Join(lines, "\n"), fingerprint
}

func findingsFingerprint(findings []finding) string {
	parts := make([]string, 0, len(findings))
	for _, item := range findings {
		parts = append(parts, fmt.Sprintf("%d|%s|%s", item.Line, item.Kind, item.Detail))
	}
	sort.Strings(parts)
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}

func issueRepoMarker(body string) string {
	match := regexp.MustCompile(`<!-- codeowners-validation:repo=([^ ]+) -->`).FindStringSubmatch(body)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(match[1])
}

func issueFingerprint(body string) string {
	match := regexp.MustCompile(`<!-- codeowners-validation:fingerprint=([a-f0-9]{16}) -->`).FindStringSubmatch(body)
	if len(match) != 2 {
		return ""
	}
	return match[1]
}

func dedupeFindings(findings []finding) []finding {
	seen := make(map[string]bool)
	result := make([]finding, 0, len(findings))
	for _, item := range findings {
		key := fmt.Sprintf("%d|%s|%s", item.Line, item.Kind, item.Detail)
		if seen[key] {
			continue
		}
		seen[key] = true
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Line != result[j].Line {
			return result[i].Line < result[j].Line
		}
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}
		return result[i].Detail < result[j].Detail
	})
	return result
}

func matchesAny(pattern *regexp.Regexp, files []string) bool {
	for _, file := range files {
		if pattern.MatchString(file) {
			return true
		}
	}
	return false
}

func printSummary(results []repoResult) {
	cleanCount := 0
	missingCount := 0
	violationCount := 0
	for _, result := range results {
		switch {
		case result.Missing:
			missingCount++
			fmt.Printf("- %s: no CODEOWNERS file\n", result.Repo)
		case len(result.Findings) == 0:
			cleanCount++
			fmt.Printf("- %s: valid (%s)\n", result.Repo, result.CodeownersPath)
		default:
			violationCount++
			fmt.Printf("- %s: %d violation(s) (%s)\n", result.Repo, len(result.Findings), result.CodeownersPath)
			for _, item := range result.Findings {
				prefix := "  * Repo"
				if item.Line > 0 {
					prefix = fmt.Sprintf("  * Line %d", item.Line)
				}
				fmt.Printf("%s: %s - %s\n", prefix, item.Kind, item.Detail)
			}
		}
	}

	fmt.Println()
	fmt.Printf("Summary: %d valid, %d missing, %d with violations\n", cleanCount, missingCount, violationCount)
}

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "HTTP 404") || strings.Contains(message, "Not Found")
}
