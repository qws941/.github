// scripts/onboard-repo.go — Onboard a repository into the qws941 ecosystem
//
// Usage:
//
//	go run scripts/onboard-repo.go [flags] <owner/repo>
//	go run scripts/onboard-repo.go [flags] <repo>          # defaults to qws941/<repo>
//
// Steps:
//
//  1. Add repo to .github/sync.yml (alphabetically) → triggers file sync on push
//  2. Sync 27 standard labels from scripts/labels.yml
//  3. Create 5 webhooks (4 MCPhub + 1 n8n)
//  4. Generate .github/dependabot.yml based on detected language ecosystems
//  5. Verify onboarding status
//
// Flags:
//
//	--dry-run          Preview changes without applying
//	--skip-sync        Skip sync.yml update
//	--skip-labels      Skip label sync
//	--skip-webhooks    Skip webhook creation
//	--skip-dependabot  Skip dependabot.yml generation
//
// Environment:
//
//	WEBHOOK_SECRET  Shared secret for GitHub webhooks (optional)
//	GH_TOKEN        GitHub token (used by gh CLI)
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Infrastructure endpoints.
const (
	mcphubBase   = "https://mcphub.jclee.me/webhook"
	n8nBase      = "https://n8n.jclee.me/webhook"
	syncYmlRel   = ".github/sync.yml"
	labelsYmlRel = "scripts/labels.yml"
	agentsMdRel  = "AGENTS.md"
)

type label struct {
	Name        string
	Color       string
	Description string
}

type webhookDef struct {
	Name   string
	URL    string
	Events []string
}

// Standard webhooks for every repo.
var standardWebhooks = []webhookDef{
	{"github-pr", mcphubBase + "/github-pr", []string{"pull_request", "pull_request_review", "push"}},
	{"github-issue", mcphubBase + "/github-issue", []string{"issues", "issue_comment"}},
	{"grafana-alert", mcphubBase + "/grafana-alert", []string{"check_suite", "workflow_run"}},
	{"n8n-pr-auto-approve", n8nBase + "/pr-auto-approve", []string{"pull_request"}},
}

// Language → dependabot ecosystem.
var langEcosystem = map[string]string{
	"Go":         "gomod",
	"Python":     "pip",
	"JavaScript": "npm",
	"TypeScript": "npm",
	"Ruby":       "bundler",
	"Rust":       "cargo",
	"PHP":        "composer",
	"Java":       "maven",
	"Kotlin":     "maven",
	"Swift":      "swift",
	"Elixir":     "mix",
}

func main() {
	dryRun := flag.Bool("dry-run", false, "Preview changes without applying")
	skipSync := flag.Bool("skip-sync", false, "Skip sync.yml update")
	skipLabels := flag.Bool("skip-labels", false, "Skip label sync")
	skipWebhooks := flag.Bool("skip-webhooks", false, "Skip webhook creation")
	skipDependabot := flag.Bool("skip-dependabot", true, "Skip dependabot.yml generation (default: true)")
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: go run scripts/onboard-repo.go [flags] <owner/repo>")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		flag.PrintDefaults()
		os.Exit(1)
	}

	repo := args[0]
	if !strings.Contains(repo, "/") {
		repo = "qws941/" + repo
	}

	apply := !*dryRun
	mode := "DRY-RUN"
	if apply {
		mode = "EXECUTE"
	}
	fmt.Printf("=== Onboard %s [%s] ===\n\n", repo, mode)

	// Validate repo exists.
	if _, err := ghOutput("repo", "view", repo, "--json", "name", "-q", ".name"); err != nil {
		fatal("repository %s not found or not accessible: %v", repo, err)
	}

	var errs []string

	if !*skipSync {
		if err := stepSyncYml(repo, apply); err != nil {
			errs = append(errs, fmt.Sprintf("sync.yml: %v", err))
		}
	} else {
		fmt.Println("--- Step 1: sync.yml [SKIPPED] ---")
	}

	if !*skipLabels {
		if err := stepLabels(repo, apply); err != nil {
			errs = append(errs, fmt.Sprintf("labels: %v", err))
		}
	} else {
		fmt.Println("\n--- Step 2: Labels [SKIPPED] ---")
	}

	if !*skipWebhooks {
		if err := stepWebhooks(repo, apply); err != nil {
			errs = append(errs, fmt.Sprintf("webhooks: %v", err))
		}
	} else {
		fmt.Println("\n--- Step 3: Webhooks [SKIPPED] ---")
	}

	if !*skipDependabot {
		if err := stepDependabot(repo, apply); err != nil {
			errs = append(errs, fmt.Sprintf("dependabot: %v", err))
		}
	} else {
		fmt.Println("\n--- Step 4: Dependabot [SKIPPED] ---")
	}

	stepVerify(repo)

	// Summary.
	fmt.Println("\n=== Summary ===")
	if len(errs) > 0 {
		fmt.Println("Errors:")
		for _, e := range errs {
			fmt.Printf("  ✗ %s\n", e)
		}
	} else {
		fmt.Println("All steps completed.")
	}

	if !apply {
		fmt.Println("\nRe-run without --dry-run to apply changes.")
	} else if !*skipSync {
		fmt.Println("\nNext: commit & push sync.yml to trigger file sync workflow.")
		fmt.Println("      Update repo count in AGENTS.md if it changed.")
	}

	if len(errs) > 0 {
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// Step 1: sync.yml
// ---------------------------------------------------------------------------

func stepSyncYml(repo string, apply bool) error {
	fmt.Println("--- Step 1: sync.yml ---")

	syncPath, err := resolveFromRoot(syncYmlRel)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(syncPath)
	if err != nil {
		return fmt.Errorf("read: %w", err)
	}

	content := string(data)
	if strings.Contains(content, repo) {
		fmt.Printf("  [skip] %s already in sync.yml\n", repo)
		return nil
	}

	lines := strings.Split(content, "\n")
	reposStart, reposEnd := -1, -1
	var repos []string
	inRepos := false

	for i, line := range lines {
		if strings.Contains(line, "repos:") && strings.Contains(line, "|") {
			inRepos = true
			reposStart = i + 1
			continue
		}
		if inRepos {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" {
				continue
			}
			// Block scalar ends when indentation drops.
			if !strings.HasPrefix(line, "      ") {
				break
			}
			repos = append(repos, trimmed)
			reposEnd = i
		}
	}

	if reposStart < 0 || reposEnd < 0 {
		return fmt.Errorf("could not find repos section in sync.yml")
	}

	repos = append(repos, repo)
	sort.Strings(repos)

	var newReposLines []string
	for _, r := range repos {
		newReposLines = append(newReposLines, "      "+r)
	}

	// Rebuild: everything before repos section + sorted repos + everything after.
	var rebuilt []string
	rebuilt = append(rebuilt, lines[:reposStart]...)
	rebuilt = append(rebuilt, newReposLines...)
	if reposEnd+1 < len(lines) {
		rebuilt = append(rebuilt, lines[reposEnd+1:]...)
	}

	if !apply {
		fmt.Printf("  [dry-run] Would add %s to sync.yml (%d → %d repos)\n", repo, len(repos)-1, len(repos))
		return nil
	}

	if err := os.WriteFile(syncPath, []byte(strings.Join(rebuilt, "\n")), 0644); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	fmt.Printf("  [added] %s (%d repos total)\n", repo, len(repos))
	return nil
}

// ---------------------------------------------------------------------------
// Step 2: Labels
// ---------------------------------------------------------------------------

func stepLabels(repo string, apply bool) error {
	fmt.Println("\n--- Step 2: Labels ---")

	labelsPath, err := resolveFromRoot(labelsYmlRel)
	if err != nil {
		return err
	}
	labels, err := parseLabelsYml(labelsPath)
	if err != nil {
		return fmt.Errorf("parse labels.yml: %w", err)
	}

	// Fetch existing labels.
	out, err := ghOutput("label", "list", "--repo", repo, "--json", "name,color,description", "--limit", "200")
	if err != nil {
		return fmt.Errorf("list labels: %w", err)
	}

	var existing []struct {
		Name        string `json:"name"`
		Color       string `json:"color"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(out), &existing); err != nil {
		return fmt.Errorf("parse response: %w", err)
	}

	type existingInfo struct{ Color, Desc string }
	exMap := make(map[string]existingInfo)
	for _, l := range existing {
		exMap[l.Name] = existingInfo{l.Color, l.Description}
	}

	created, updated, skipped := 0, 0, 0

	for _, l := range labels {
		ex, exists := exMap[l.Name]
		if exists && strings.EqualFold(ex.Color, l.Color) && ex.Desc == l.Description {
			skipped++
			continue
		}

		action := "create"
		ghArgs := []string{"label"}
		if exists {
			action = "edit"
			ghArgs = append(ghArgs, "edit", l.Name)
		} else {
			ghArgs = append(ghArgs, "create", l.Name)
		}
		ghArgs = append(ghArgs, "--repo", repo, "--color", l.Color, "--description", l.Description)

		if !apply {
			fmt.Printf("  [dry-run] Would %s: %s\n", action, l.Name)
		} else {
			if _, err := ghOutput(ghArgs...); err != nil {
				fmt.Printf("  [error] %s %s: %v\n", action, l.Name, err)
				continue
			}
		}

		if action == "create" {
			created++
		} else {
			updated++
		}
	}

	fmt.Printf("  Created: %d, Updated: %d, Unchanged: %d (of %d)\n", created, updated, skipped, len(labels))
	return nil
}

// ---------------------------------------------------------------------------
// Step 3: Webhooks
// ---------------------------------------------------------------------------

func stepWebhooks(repo string, apply bool) error {
	fmt.Println("\n--- Step 3: Webhooks ---")

	out, err := ghOutput("api", fmt.Sprintf("repos/%s/hooks", repo), "--paginate")
	if err != nil {
		return fmt.Errorf("list hooks: %w", err)
	}

	var hooks []struct {
		ID     int `json:"id"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if out != "" {
		if err := json.Unmarshal([]byte(out), &hooks); err != nil {
			return fmt.Errorf("parse hooks: %w", err)
		}
	}

	existingURLs := make(map[string]bool)
	for _, h := range hooks {
		existingURLs[h.Config.URL] = true
	}

	secret := os.Getenv("WEBHOOK_SECRET")
	created, skipped := 0, 0

	for _, wh := range standardWebhooks {
		if existingURLs[wh.URL] {
			fmt.Printf("  [skip] %s (exists)\n", wh.Name)
			skipped++
			continue
		}

		if !apply {
			fmt.Printf("  [dry-run] Would create: %s → %s\n", wh.Name, wh.URL)
			created++
			continue
		}

		ghArgs := []string{
			"api", fmt.Sprintf("repos/%s/hooks", repo),
			"--method", "POST",
			"-f", "name=web",
			"-F", "active=true",
			"-f", "config[url]=" + wh.URL,
			"-f", "config[content_type]=json",
		}
		if secret != "" {
			ghArgs = append(ghArgs, "-f", "config[secret]="+secret)
		}
		for _, ev := range wh.Events {
			ghArgs = append(ghArgs, "-f", "events[]="+ev)
		}

		if _, err := ghOutput(ghArgs...); err != nil {
			fmt.Printf("  [error] %s: %v\n", wh.Name, err)
			continue
		}
		fmt.Printf("  [created] %s\n", wh.Name)
		created++
	}

	fmt.Printf("  Created: %d, Existed: %d\n", created, skipped)
	return nil
}

// ---------------------------------------------------------------------------
// Step 4: Dependabot
// ---------------------------------------------------------------------------

func stepDependabot(repo string, apply bool) error {
	fmt.Println("\n--- Step 4: Dependabot ---")

	// Check if already exists.
	if _, err := ghOutput("api", fmt.Sprintf("repos/%s/contents/.github/dependabot.yml", repo), "-q", ".name"); err == nil {
		fmt.Println("  [skip] dependabot.yml already exists")
		return nil
	}

	// Detect ecosystems from repo languages.
	out, err := ghOutput("api", fmt.Sprintf("repos/%s/languages", repo))
	if err != nil {
		return fmt.Errorf("get languages: %w", err)
	}

	var languages map[string]int
	if err := json.Unmarshal([]byte(out), &languages); err != nil {
		return fmt.Errorf("parse languages: %w", err)
	}

	ecosystems := map[string]bool{"github-actions": true}
	for lang := range languages {
		if eco, ok := langEcosystem[lang]; ok {
			ecosystems[eco] = true
		}
	}

	// Check for Dockerfile.
	if _, err := ghOutput("api", fmt.Sprintf("repos/%s/contents/Dockerfile", repo), "-q", ".name"); err == nil {
		ecosystems["docker"] = true
	}

	var sorted []string
	for eco := range ecosystems {
		sorted = append(sorted, eco)
	}
	sort.Strings(sorted)

	// Generate dependabot.yml content.
	var buf bytes.Buffer
	buf.WriteString("version: 2\nupdates:\n")
	for _, eco := range sorted {
		fmt.Fprintf(&buf, "  - package-ecosystem: %q\n    directory: \"/\"\n    schedule:\n      interval: \"weekly\"\n", eco)
	}
	content := buf.String()

	fmt.Printf("  Ecosystems: %s\n", strings.Join(sorted, ", "))

	if !apply {
		fmt.Println("  [dry-run] Would create .github/dependabot.yml")
		return nil
	}

	encoded := base64.StdEncoding.EncodeToString([]byte(content))
	if _, err := ghOutput("api", fmt.Sprintf("repos/%s/contents/.github/dependabot.yml", repo),
		"--method", "PUT",
		"-f", "message=chore: add dependabot configuration",
		"-f", "content="+encoded,
	); err != nil {
		return fmt.Errorf("create dependabot.yml: %w", err)
	}

	fmt.Println("  [created] .github/dependabot.yml")
	return nil
}

// ---------------------------------------------------------------------------
// Step 5: Verify
// ---------------------------------------------------------------------------

func stepVerify(repo string) {
	fmt.Println("\n--- Step 5: Verify ---")

	// sync.yml.
	syncPath, _ := resolveFromRoot(syncYmlRel)
	if data, err := os.ReadFile(syncPath); err == nil {
		if strings.Contains(string(data), repo) {
			fmt.Println("  ✓ sync.yml")
		} else {
			fmt.Println("  ✗ sync.yml (not found)")
		}
	}

	// Labels.
	out, err := ghOutput("label", "list", "--repo", repo, "--json", "name", "--limit", "200", "-q", "length")
	if err == nil {
		fmt.Printf("  ✓ Labels: %s\n", out)
	} else {
		fmt.Println("  ✗ Labels (query failed)")
	}

	// Webhooks.
	out, err = ghOutput("api", fmt.Sprintf("repos/%s/hooks", repo), "--paginate", "-q", "length")
	if err == nil {
		fmt.Printf("  ✓ Webhooks: %s\n", out)
	} else {
		fmt.Println("  ✗ Webhooks (query failed)")
	}

	// Dependabot.
	if _, err := ghOutput("api", fmt.Sprintf("repos/%s/contents/.github/dependabot.yml", repo), "-q", ".name"); err == nil {
		fmt.Println("  ✓ dependabot.yml")
	} else {
		fmt.Println("  ✗ dependabot.yml (not found)")
	}
}

// ---------------------------------------------------------------------------
// Label YAML parser (stdlib-only, no gopkg.in/yaml.v3)
// ---------------------------------------------------------------------------

func parseLabelsYml(path string) ([]label, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var labels []label
	var cur *label

	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "- name:"):
			if cur != nil {
				labels = append(labels, *cur)
			}
			cur = &label{Name: unquote(strings.TrimPrefix(trimmed, "- name:"))}
		case strings.HasPrefix(trimmed, "color:") && cur != nil:
			cur.Color = unquote(strings.TrimPrefix(trimmed, "color:"))
		case strings.HasPrefix(trimmed, "description:") && cur != nil:
			cur.Description = unquote(strings.TrimPrefix(trimmed, "description:"))
		}
	}
	if cur != nil {
		labels = append(labels, *cur)
	}
	return labels, nil
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		return s[1 : len(s)-1]
	}
	return s
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func ghOutput(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

func resolveFromRoot(rel string) (string, error) {
	if _, err := os.Stat(rel); err == nil {
		abs, _ := filepath.Abs(rel)
		return abs, nil
	}
	return "", fmt.Errorf("%s not found — run from repo root", rel)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", a...)
	os.Exit(1)
}
