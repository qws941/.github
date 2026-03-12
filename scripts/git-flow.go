// scripts/git-flow.go — Developer git-flow automation for trunk-based development.
//
// Usage:
//
//	go run scripts/git-flow.go <command> [flags]
//
// Commands:
//
//	start <type/name>   Create a new feature branch from master
//	pr                  Push and create a pull request
//	finish              Merge PR, delete branch, return to master
//	status              Show current branch, PR state, and checks
//	sync                Rebase current branch onto origin/master
//
// Global Flags:
//
//	--dry-run    Preview actions without executing (default: false)
//	--verbose    Enable verbose output (default: false)
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

const baseBranch = "master"

var branchPattern = regexp.MustCompile(`^(feat|fix|ci|docs|refactor|test|chore|perf|build|revert)/[a-z0-9-]+$`)

var noColor = os.Getenv("NO_COLOR") != ""

func colorGreen(s string) string {
	if noColor {
		return s
	}
	return "\033[32m" + s + "\033[0m"
}

func colorYellow(s string) string {
	if noColor {
		return s
	}
	return "\033[33m" + s + "\033[0m"
}

func colorRed(s string) string {
	if noColor {
		return s
	}
	return "\033[31m" + s + "\033[0m"
}

func colorCyan(s string) string {
	if noColor {
		return s
	}
	return "\033[36m" + s + "\033[0m"
}

func colorBold(s string) string {
	if noColor {
		return s
	}
	return "\033[1m" + s + "\033[0m"
}

var globalDryRun bool
var globalVerbose bool

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, colorRed("[error] ")+format+"\n", a...)
	os.Exit(1)
}

func gitOutput(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func ghOutput(args ...string) (string, error) {
	cmd := exec.Command("gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s", msg)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func run(dryRun bool, label string, fn func() error) {
	if dryRun {
		fmt.Printf("  [dry-run] Would %s\n", label)
		return
	}
	if err := fn(); err != nil {
		fatal("%s: %v", label, err)
	}
	fmt.Printf("  [done] %s\n", label)
}

func currentBranch() string {
	out, err := gitOutput("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		fatal("resolve current branch: %v", err)
	}
	return out
}

func isCleanWorktree() bool {
	out, err := gitOutput("status", "--porcelain")
	if err != nil {
		fatal("read worktree status: %v", err)
	}
	return len(out) == 0
}

func requireFeatureBranch() {
	branch := currentBranch()
	if branch == baseBranch || branch == "main" || branch == "HEAD" {
		fatal("must be on a feature branch, currently on '%s'", branch)
	}
	if !branchPattern.MatchString(branch) {
		fatal("current branch '%s' does not match naming convention (expected type/kebab-name)", branch)
	}
}

func requireCleanWorktree() {
	if !isCleanWorktree() {
		fatal("worktree has uncommitted changes; commit or stash first")
	}
}

func logVerbose(format string, a ...any) {
	if globalVerbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] "+format+"\n", a...)
	}
}

func commandUsage() {
	fmt.Fprintf(os.Stderr, "%s\n", "scripts/git-flow.go — Developer git-flow automation for trunk-based development.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s\n", "Usage:")
	fmt.Fprintf(os.Stderr, "  %s\n", "go run scripts/git-flow.go <command> [flags]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s\n", "Commands:")
	fmt.Fprintf(os.Stderr, "  %-20s %s\n", "start <type/name>", "Create a new feature branch from master")
	fmt.Fprintf(os.Stderr, "  %-20s %s\n", "pr", "Push and create a pull request")
	fmt.Fprintf(os.Stderr, "  %-20s %s\n", "finish", "Merge PR, delete branch, return to master")
	fmt.Fprintf(os.Stderr, "  %-20s %s\n", "status", "Show current branch, PR state, and checks")
	fmt.Fprintf(os.Stderr, "  %-20s %s\n", "sync", "Rebase current branch onto origin/master")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s\n", "Global Flags:")
	fmt.Fprintf(os.Stderr, "  %-12s %s\n", "--dry-run", "Preview actions without executing (default: false)")
	fmt.Fprintf(os.Stderr, "  %-12s %s\n", "--verbose", "Enable verbose output (default: false)")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "%s\n", "Run 'go run scripts/git-flow.go <command> --help' for details")
}

func parseGlobal(args []string) (string, []string) {
	global := flag.NewFlagSet("git-flow", flag.ContinueOnError)
	global.SetOutput(os.Stderr)
	global.BoolVar(&globalDryRun, "dry-run", false, "Preview actions without executing")
	global.BoolVar(&globalVerbose, "verbose", false, "Enable verbose output")
	global.Usage = commandUsage

	idx := -1
	for i, a := range args {
		if !strings.HasPrefix(a, "-") {
			idx = i
			break
		}
	}

	if idx == -1 {
		if err := global.Parse(args); err != nil {
			fatal("parse global flags: %v", err)
		}
		return "", nil
	}

	if err := global.Parse(args[:idx]); err != nil {
		fatal("parse global flags: %v", err)
	}
	if idx >= len(args) {
		return "", nil
	}
	return args[idx], args[idx+1:]
}

func printMode(dryRun bool) {
	mode := "EXECUTE"
	if dryRun {
		mode = "DRY-RUN"
	}
	fmt.Printf("Mode: %s\n", mode)
}

func countDirtyFiles() int {
	out, err := gitOutput("status", "--porcelain")
	if err != nil {
		return 0
	}
	if strings.TrimSpace(out) == "" {
		return 0
	}
	return len(strings.Split(strings.TrimSpace(out), "\n"))
}

func changedFilesAgainstBase() []string {
	out, err := gitOutput("diff", "--name-only", "origin/"+baseBranch+"...HEAD")
	if err != nil || strings.TrimSpace(out) == "" {
		return nil
	}
	parts := strings.Split(strings.TrimSpace(out), "\n")
	var files []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			files = append(files, p)
		}
	}
	return files
}

func inferScopeFromFiles(files []string) string {
	if len(files) == 0 {
		return ""
	}
	seen := map[string]bool{}
	for _, f := range files {
		scope := ""
		switch {
		case strings.HasPrefix(f, "scripts/"):
			scope = "scripts"
		case strings.HasPrefix(f, ".github/workflows/"):
			scope = "ci"
		case strings.HasPrefix(f, ".github/ISSUE_TEMPLATE/"):
			scope = "issue-template"
		case strings.HasPrefix(f, ".github/"):
			scope = "github"
		case strings.HasPrefix(f, "profile/"):
			scope = "profile"
		case !strings.Contains(f, "/"):
			scope = "governance"
		default:
			return ""
		}
		seen[scope] = true
	}
	if len(seen) != 1 {
		return ""
	}
	for s := range seen {
		return s
	}
	return ""
}

func generatedTitle(branch, explicitScope string) string {
	branchType := "chore"
	desc := branch
	if strings.Contains(branch, "/") {
		parts := strings.SplitN(branch, "/", 2)
		branchType = parts[0]
		desc = parts[1]
	}
	desc = strings.ReplaceAll(desc, "-", " ")
	desc = strings.TrimSpace(desc)
	if desc == "" {
		desc = "update"
	}
	scope := explicitScope
	if scope == "" {
		scope = inferScopeFromFiles(changedFilesAgainstBase())
	}
	if scope != "" {
		return fmt.Sprintf("%s(%s): %s", branchType, scope, desc)
	}
	return fmt.Sprintf("%s: %s", branchType, desc)
}

func generatedBody(branch string, files []string) string {
	branchType := ""
	if idx := strings.Index(branch, "/"); idx > 0 {
		branchType = branch[:idx]
	}

	kindMap := map[string]string{
		"feat":     "/kind feat",
		"fix":      "/kind fix",
		"refactor": "/kind refactor",
		"ci":       "/kind infra",
		"build":    "/kind infra",
		"docs":     "/kind docs",
		"chore":    "/kind chore",
		"perf":     "/kind refactor",
		"test":     "/kind chore",
		"revert":   "/kind fix",
	}
	matchedKind := kindMap[branchType]

	type kindEntry struct {
		label, kind string
	}
	kinds := []kindEntry{
		{"New feature or capability", "/kind feat"},
		{"Bug fix (non-breaking)", "/kind fix"},
		{"Code restructuring (no behavior change)", "/kind refactor"},
		{"Infrastructure, Terraform, CI/CD, deployment", "/kind infra"},
		{"Documentation only", "/kind docs"},
		{"Maintenance, dependencies, tooling", "/kind chore"},
		{"Breaking change (requires migration or coordination)", "/kind breaking"},
	}

	var b strings.Builder
	b.WriteString("## What This PR Does\n\n")
	b.WriteString("<!-- Describe WHAT this PR changes. Be specific. -->\n\n")
	b.WriteString("## Why\n\n")
	b.WriteString("<!-- Explain WHY this change is needed. Link to the problem or motivation. -->\n\n")
	b.WriteString("## Kind\n\n")
	for _, k := range kinds {
		check := " "
		if k.kind == matchedKind {
			check = "x"
		}
		fmt.Fprintf(&b, "- [%s] `%s` — %s\n", check, k.kind, k.label)
	}
	b.WriteString("\n## Changes\n\n")
	if len(files) == 0 {
		b.WriteString("- (no changed files detected against origin/" + baseBranch + "...HEAD)\n")
	} else {
		for _, f := range files {
			b.WriteString("- " + f + "\n")
		}
	}
	b.WriteString("\n## Testing\n\n")
	b.WriteString("- [ ] Tested locally\n")
	b.WriteString("- [ ] CI checks pass\n")
	b.WriteString("- [ ] Infrastructure changes verified (`terraform plan`, deploy preview, etc.) — if applicable\n")
	b.WriteString("- [ ] Manual verification on staging/prod\n")
	b.WriteString("\n## Breaking Changes\n\n")
	b.WriteString("N/A\n")
	b.WriteString("\n## Checklist\n\n")
	b.WriteString("- [ ] PR title follows `type(scope): description` format\n")
	b.WriteString("- [ ] Code follows the project's existing style and conventions\n")
	b.WriteString("- [ ] Self-review completed\n")
	b.WriteString("- [ ] Documentation updated (if applicable)\n")
	b.WriteString("- [ ] No new warnings, errors, or type suppressions\n")
	b.WriteString("- [ ] Change is **< 200 LOC** (excluding generated/lock files), or justification provided\n")
	b.WriteString("\n## Related Issues\n\n")
	b.WriteString("Closes #\n")
	return b.String()
}

func parseAheadBehind(out string) (int, int) {
	var left, right int
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		return 0, 0
	}
	if _, err := fmt.Sscanf(trimmed, "%d %d", &left, &right); err == nil {
		return right, left
	}
	if _, err := fmt.Sscanf(trimmed, "%d\t%d", &left, &right); err == nil {
		return right, left
	}
	return 0, 0
}

// resolveCheckConclusion extracts the effective conclusion from a GitHub
// statusCheckRollup item. CheckRun objects use conclusion + status;
// StatusContext objects use state. Priority: conclusion > state > status.
func resolveCheckConclusion(obj map[string]any) string {
	conclusion, _ := obj["conclusion"].(string)
	if conclusion != "" {
		return conclusion
	}
	state, _ := obj["state"].(string)
	if state != "" {
		return state
	}
	status, _ := obj["status"].(string)
	return status
}
func statusChecksSummary(raw []json.RawMessage) string {
	if len(raw) == 0 {
		return "0/0 passed"
	}
	passed := 0
	failed := 0
	pending := 0
	for _, item := range raw {
		var obj map[string]any
		if err := json.Unmarshal(item, &obj); err != nil {
			continue
		}
		effective := resolveCheckConclusion(obj)
		switch strings.ToUpper(effective) {
		case "SUCCESS", "PASSED":
			passed++
		case "FAILURE", "FAILED", "ERROR", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
			failed++
		case "QUEUED", "IN_PROGRESS", "PENDING", "EXPECTED":
			pending++
		}
	}
	total := len(raw)
	parts := []string{fmt.Sprintf("%d/%d passed", passed, total)}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", failed))
	}
	if pending > 0 {
		parts = append(parts, fmt.Sprintf("%d pending", pending))
	}
	return strings.Join(parts, ", ")
}

func main() {
	flag.Usage = commandUsage
	cmd, cmdArgs := parseGlobal(os.Args[1:])
	if cmd == "" {
		flag.Usage()
		os.Exit(1)
	}

	switch cmd {
	case "start":
		cmdStart(cmdArgs)
	case "pr":
		cmdPR(cmdArgs)
	case "finish":
		cmdFinish(cmdArgs)
	case "status":
		cmdStatus(cmdArgs)
	case "sync":
		cmdSync(cmdArgs)
	default:
		fatal("unknown command: %s", cmd)
	}
}

func cmdStart(args []string) {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", globalDryRun, "Preview actions without executing")
	verbose := fs.Bool("verbose", globalVerbose, "Enable verbose output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: go run scripts/git-flow.go start [--dry-run] [--verbose] <type/name>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		fatal("parse flags: %v", err)
	}
	globalVerbose = *verbose
	if fs.NArg() != 1 {
		fs.Usage()
		fatal("start requires exactly one branch name argument")
	}
	branch := fs.Arg(0)
	if !branchPattern.MatchString(branch) {
		fatal("invalid branch '%s' (expected type/name and lowercase kebab-case)", branch)
	}

	requireCleanWorktree()

	fmt.Printf("--- start: %s ---\n", branch)
	printMode(*dryRun)

	run(*dryRun, "fetch origin", func() error {
		_, err := gitOutput("fetch", "origin")
		return err
	})
	run(*dryRun, "checkout "+baseBranch, func() error {
		_, err := gitOutput("checkout", baseBranch)
		return err
	})
	run(*dryRun, "pull --rebase origin "+baseBranch, func() error {
		_, err := gitOutput("pull", "--rebase", "origin", baseBranch)
		return err
	})
	run(*dryRun, "create branch "+branch, func() error {
		_, err := gitOutput("checkout", "-b", branch)
		return err
	})
	run(*dryRun, "push -u origin "+branch, func() error {
		_, err := gitOutput("push", "-u", "origin", branch)
		return err
	})

	if *dryRun {
		fmt.Println("Dry-run complete. Re-run without --dry-run to apply.")
	} else {
		fmt.Printf("Branch '%s' created and pushed.\n", branch)
	}
}

func cmdPR(args []string) {
	fs := flag.NewFlagSet("pr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	title := fs.String("title", "", "PR title")
	body := fs.String("body", "", "PR body")
	scope := fs.String("scope", "", "Scope for generated conventional title")
	draft := fs.Bool("draft", false, "Create draft PR")
	dryRun := fs.Bool("dry-run", globalDryRun, "Preview actions without executing")
	verbose := fs.Bool("verbose", globalVerbose, "Enable verbose output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: go run scripts/git-flow.go pr [--title t] [--body b] [--scope s] [--draft] [--dry-run] [--verbose]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		fatal("parse flags: %v", err)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		fatal("pr does not accept positional arguments")
	}
	globalVerbose = *verbose
	requireFeatureBranch()
	requireCleanWorktree()

	branch := currentBranch()
	files := changedFilesAgainstBase()
	finalTitle := strings.TrimSpace(*title)
	if finalTitle == "" {
		finalTitle = generatedTitle(branch, strings.TrimSpace(*scope))
	}
	finalBody := strings.TrimSpace(*body)
	if finalBody == "" {
		finalBody = generatedBody(branch, files)
	}

	fmt.Printf("--- pr: %s ---\n", branch)
	printMode(*dryRun)
	fmt.Printf("Generated title: %s\n", colorCyan(finalTitle))
	if len(files) == 0 {
		fmt.Println("Changed files: (none detected against origin/master...HEAD)")
	} else {
		fmt.Println("Changed files:")
		for _, f := range files {
			fmt.Printf("  - %s\n", f)
		}
	}

	run(*dryRun, "push current branch", func() error {
		_, err := gitOutput("push", "origin", branch)
		return err
	})

	if *dryRun {
		fmt.Println("  [dry-run] Would create pull request")
		fmt.Println("Dry-run complete. Re-run without --dry-run to apply.")
		return
	}

	ghArgs := []string{"pr", "create", "--base", baseBranch, "--title", finalTitle, "--body", finalBody}
	if *draft {
		ghArgs = append(ghArgs, "--draft")
	}
	prURL, err := ghOutput(ghArgs...)
	if err != nil {
		fatal("create pull request: %v", err)
	}
	fmt.Printf("  [done] create pull request\n")
	fmt.Printf("PR URL: %s\n", colorGreen(prURL))
}

func cmdFinish(args []string) {
	fs := flag.NewFlagSet("finish", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", globalDryRun, "Preview actions without executing")
	verbose := fs.Bool("verbose", globalVerbose, "Enable verbose output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: go run scripts/git-flow.go finish [--dry-run] [--verbose]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		fatal("parse flags: %v", err)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		fatal("finish does not accept positional arguments")
	}
	globalVerbose = *verbose
	requireFeatureBranch()
	requireCleanWorktree()

	branch := currentBranch()
	fmt.Printf("--- finish: %s ---\n", branch)
	printMode(*dryRun)

	prRaw, err := ghOutput("pr", "view", "--json", "state,url,number,isDraft,mergeable,statusCheckRollup")
	if err != nil {
		fatal("resolve pull request: %v", err)
	}
	var pr struct {
		State             string            `json:"state"`
		URL               string            `json:"url"`
		Number            int               `json:"number"`
		IsDraft           bool              `json:"isDraft"`
		Mergeable         string            `json:"mergeable"`
		StatusCheckRollup []json.RawMessage `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal([]byte(prRaw), &pr); err != nil {
		fatal("parse pull request info: %v", err)
	}
	if strings.ToUpper(pr.State) != "OPEN" {
		fatal("pull request must be OPEN to finish; current state: %s", pr.State)
	}
	if pr.IsDraft {
		fatal("pull request is still a draft; mark as ready before finishing")
	}
	if strings.ToUpper(pr.Mergeable) != "MERGEABLE" && strings.ToUpper(pr.Mergeable) != "" {
		fatal("pull request is not mergeable; current mergeable state: %s", pr.Mergeable)
	}
	checks := statusChecksSummary(pr.StatusCheckRollup)
	hasFailures := false
	hasPending := false
	for _, item := range pr.StatusCheckRollup {
		var obj map[string]any
		if err := json.Unmarshal(item, &obj); err != nil {
			continue
		}
		effective := resolveCheckConclusion(obj)
		switch strings.ToUpper(effective) {
		case "FAILURE", "FAILED", "ERROR", "TIMED_OUT", "ACTION_REQUIRED", "STARTUP_FAILURE":
			hasFailures = true
		case "QUEUED", "IN_PROGRESS", "PENDING", "EXPECTED":
			hasPending = true
		}
	}
	if hasFailures {
		fatal("CI checks have failures (%s); resolve before finishing", checks)
	}
	if hasPending {
		fatal("CI checks are still running (%s); wait for completion before finishing", checks)
	}
	fmt.Printf("PR: #%d %s\n", pr.Number, pr.URL)

	run(*dryRun, "merge pull request", func() error {
		_, err := ghOutput("pr", "merge", "--squash", "--delete-branch")
		return err
	})
	run(*dryRun, "checkout "+baseBranch, func() error {
		_, err := gitOutput("checkout", baseBranch)
		return err
	})
	run(*dryRun, "pull --rebase origin "+baseBranch, func() error {
		_, err := gitOutput("pull", "--rebase", "origin", baseBranch)
		return err
	})

	if *dryRun {
		fmt.Printf("  [dry-run] Would delete local branch %s\n", branch)
		fmt.Println("Dry-run complete. Re-run without --dry-run to apply.")
		return
	}
	if _, err := gitOutput("branch", "-D", branch); err != nil {
		fmt.Printf("  [skip] delete local branch %s: %v\n", branch, err)
	} else {
		fmt.Printf("  [done] delete local branch %s\n", branch)
	}
	fmt.Printf("Merged and cleaned up PR %s.\n", pr.URL)
}

func cmdStatus(args []string) {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: go run scripts/git-flow.go status")
	}
	if err := fs.Parse(args); err != nil {
		fatal("parse flags: %v", err)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		fatal("status does not accept positional arguments")
	}

	branch := currentBranch()
	upstream, upErr := gitOutput("rev-parse", "--abbrev-ref", "@{upstream}")
	if upErr != nil {
		upstream = "(none)"
	}
	dirtyCount := countDirtyFiles()
	worktree := "clean"
	if dirtyCount > 0 {
		worktree = fmt.Sprintf("dirty (%d files)", dirtyCount)
	}

	ahead := 0
	behind := 0
	if out, err := gitOutput("rev-list", "--left-right", "--count", "origin/"+baseBranch+"...HEAD"); err == nil {
		ahead, behind = parseAheadBehind(out)
	}

	fmt.Println("=== Branch Status ===")
	fmt.Printf("Branch:    %s\n", branch)
	fmt.Printf("Upstream:  %s\n", upstream)
	fmt.Printf("Worktree:  %s\n", worktree)
	fmt.Printf("Ahead:     %d commits\n", ahead)
	fmt.Printf("Behind:    %d commits\n", behind)

	if branch == baseBranch || branch == "main" || branch == "HEAD" {
		return
	}

	prRaw, err := ghOutput("pr", "view", "--json", "url,state,isDraft,mergeable,statusCheckRollup,title,number")
	if err != nil {
		fmt.Println()
		fmt.Println("=== Pull Request ===")
		fmt.Println("No pull request found for this branch.")
		return
	}

	var pr struct {
		URL               string            `json:"url"`
		State             string            `json:"state"`
		IsDraft           bool              `json:"isDraft"`
		Mergeable         string            `json:"mergeable"`
		Title             string            `json:"title"`
		Number            int               `json:"number"`
		StatusCheckRollup []json.RawMessage `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal([]byte(prRaw), &pr); err != nil {
		fmt.Println()
		fmt.Println("=== Pull Request ===")
		fmt.Println("No pull request found for this branch.")
		return
	}

	state := strings.ToUpper(pr.State)
	if pr.IsDraft {
		state += " (draft)"
	}
	checks := statusChecksSummary(pr.StatusCheckRollup)

	fmt.Println()
	fmt.Println("=== Pull Request ===")
	fmt.Printf("PR:        #%d -- %s\n", pr.Number, pr.Title)
	fmt.Printf("URL:       %s\n", pr.URL)
	fmt.Printf("State:     %s\n", state)
	fmt.Printf("Mergeable: %s\n", strings.ToUpper(pr.Mergeable))
	fmt.Printf("Checks:    %s\n", checks)
}

func cmdSync(args []string) {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", globalDryRun, "Preview actions without executing")
	verbose := fs.Bool("verbose", globalVerbose, "Enable verbose output")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: go run scripts/git-flow.go sync [--dry-run] [--verbose]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		fatal("parse flags: %v", err)
	}
	if fs.NArg() != 0 {
		fs.Usage()
		fatal("sync does not accept positional arguments")
	}
	globalVerbose = *verbose
	requireFeatureBranch()
	requireCleanWorktree()

	branch := currentBranch()
	fmt.Printf("--- sync: %s ---\n", branch)
	printMode(*dryRun)

	run(*dryRun, "fetch origin", func() error {
		_, err := gitOutput("fetch", "origin")
		return err
	})

	if *dryRun {
		fmt.Printf("  [dry-run] Would rebase onto origin/%s\n", baseBranch)
		fmt.Println("Dry-run complete. Re-run without --dry-run to apply.")
		return
	}

	if _, err := gitOutput("rebase", "origin/"+baseBranch); err != nil {
		fmt.Printf("  %s rebase onto origin/%s failed: %v\n", colorRed("[error]"), baseBranch, err)
		fmt.Println("  Resolve conflicts, then run 'git rebase --continue'.")
		fmt.Println("  To cancel and restore state, run 'git rebase --abort'.")
		fatal("sync aborted due to rebase conflict")
	}
	fmt.Printf("  [done] rebase onto origin/%s\n", baseBranch)
}
