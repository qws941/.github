//go:build ignore

// scripts/validate-labels.go — Validate workflow label references against labels.yml SSoT
//
// Scans .github/workflows/_*.yml template files for hard-coded label references
// and verifies every referenced label exists in scripts/labels.yml.
//
// Usage:
//
//	go run scripts/validate-labels.go [flags]
//
// Flags:
//
//	--verbose   Show matched labels and file locations
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	labelsFile   = "scripts/labels.yml"
	workflowGlob = ".github/workflows/_*.yml"
)

// Label reference patterns found in workflow YAML files.
//
// Pattern categories:
//  1. YAML string arrays:      labels: ['codex', 'type:security']
//  2. YAML config values:      stale-issue-label: 'status:stale'
//  3. CSV in config:           exempt-issue-labels: 'keep-open,pinned,type:security'
//  4. contains() expressions:  contains(github.event.pull_request.labels.*.name, 'auto-merge')
//  5. JS map values:           '🐛 버그': 'type:bug'
//  6. JS array/string:         const wantLabels = ['codex', 'auto-merge']
var labelPatterns = []*regexp.Regexp{
	// Matches quoted label-like strings: 'type:bug', "status:stale", 'size/xs',
	// 'auto-merge', 'codex', 'sync', 'keep-open', 'pinned'
	// Label shapes: word:word-word, size/word, or bare automation words
	regexp.MustCompile(`['"]([a-z]+:[a-z][-a-z]*)['"]`),                    // type:bug, status:in-progress, priority:high, risk:low
	regexp.MustCompile(`['"]size/(xs|s|m|l|xl)['"]`),                       // size/xs .. size/xl
	regexp.MustCompile(`['"](auto-merge|codex|sync|keep-open|pinned)['"]`), // bare automation labels
}

// csvLabelPattern matches comma-separated label lists inside quotes.
// Example: 'keep-open,pinned,type:security'
var csvLabelPattern = regexp.MustCompile(`['"]([a-z][-a-z]*(?::[a-z][-a-z]*)?,(?:[a-z][-a-z]*(?::[a-z][-a-z]*)?,)*[a-z][-a-z]*(?::[a-z][-a-z]*)?)['"]`)

// individualLabelShape matches a single label token after CSV splitting.
var individualLabelShape = regexp.MustCompile(`^(?:[a-z]+:[a-z][-a-z]*|size/(?:xs|s|m|l|xl)|auto-merge|codex|sync|keep-open|pinned)$`)

// Known false positives — strings that match label patterns but aren't labels.
var falsePositives = map[string]bool{
	// Workflow config keys that look like type:value
	"on:push":                true,
	"on:pull_request":        true,
	"on:schedule":            true,
	"on:workflow_dispatch":   true,
	"on:workflow_call":       true,
	"on:issues":              true,
	"on:pull_request_target": true,
	// GitHub Actions expressions
	"github:token":   true,
	"secrets:gh_pat": true,
	// Common YAML keys
	"runs:ubuntu": true,
}

func main() {
	verbose := flag.Bool("verbose", false, "show matched labels and file locations")
	flag.Parse()

	// Resolve labels.yml from repo root.
	labelsPath, err := resolveFromRoot(labelsFile)
	if err != nil {
		fatal("labels.yml: %v", err)
	}

	// Parse SSoT labels.
	labels, err := parseLabelsYml(labelsPath)
	if err != nil {
		fatal("parse labels.yml: %v", err)
	}

	ssot := make(map[string]bool, len(labels))
	for _, l := range labels {
		ssot[l.name] = true
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d labels from %s\n", len(ssot), labelsFile)
	}

	// Find workflow template files.
	workflowDir, err := resolveFromRoot(".github/workflows")
	if err != nil {
		fatal("workflows dir: %v", err)
	}

	matches, err := filepath.Glob(filepath.Join(workflowDir, "_*.yml"))
	if err != nil {
		fatal("glob workflows: %v", err)
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Scanning %d workflow templates\n", len(matches))
	}

	// Scan each workflow file for label references.
	type ref struct {
		file  string
		line  int
		label string
	}

	var allRefs []ref
	var unknown []ref

	for _, path := range matches {
		data, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", path, err)
			continue
		}

		relPath, _ := filepath.Rel(filepath.Dir(workflowDir), path)
		lines := strings.Split(string(data), "\n")

		for lineNum, line := range lines {
			// Skip comment-only lines.
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}

			// Phase 1: Extract labels from CSV lists (e.g., 'keep-open,pinned,type:security').
			for _, m := range csvLabelPattern.FindAllStringSubmatch(line, -1) {
				for _, token := range strings.Split(m[1], ",") {
					token = strings.TrimSpace(token)
					if token == "" || !individualLabelShape.MatchString(token) {
						continue
					}
					if falsePositives[token] {
						continue
					}
					allRefs = append(allRefs, ref{file: relPath, line: lineNum + 1, label: token})
					if !ssot[token] {
						unknown = append(unknown, ref{file: relPath, line: lineNum + 1, label: token})
					}
				}
			}

			// Phase 2: Extract individually quoted labels.

			for _, pat := range labelPatterns {
				for _, m := range pat.FindAllStringSubmatch(line, -1) {
					labelName := m[1]

					// Reconstruct full label for size/* patterns.
					if strings.Contains(pat.String(), "size/") {
						labelName = "size/" + labelName
					}

					// Skip false positives.
					if falsePositives[labelName] {
						continue
					}

					allRefs = append(allRefs, ref{
						file:  relPath,
						line:  lineNum + 1,
						label: labelName,
					})

					if !ssot[labelName] {
						unknown = append(unknown, ref{
							file:  relPath,
							line:  lineNum + 1,
							label: labelName,
						})
					}
				}
			}
		}
	}

	// Report results.
	if *verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Found %d label references across all templates\n", len(allRefs))

		// Group by label for verbose output.
		byLabel := make(map[string][]ref)
		for _, r := range allRefs {
			byLabel[r.label] = append(byLabel[r.label], r)
		}
		labelNames := make([]string, 0, len(byLabel))
		for l := range byLabel {
			labelNames = append(labelNames, l)
		}
		sort.Strings(labelNames)

		fmt.Fprintf(os.Stderr, "\n[VERBOSE] Label references:\n")
		for _, l := range labelNames {
			status := "✓"
			if !ssot[l] {
				status = "✗ NOT IN SSoT"
			}
			fmt.Fprintf(os.Stderr, "  %s %s\n", status, l)
			for _, r := range byLabel[l] {
				fmt.Fprintf(os.Stderr, "    %s:%d\n", r.file, r.line)
			}
		}
	}

	if len(unknown) == 0 {
		// Deduplicate found labels.
		seen := make(map[string]bool)
		for _, r := range allRefs {
			seen[r.label] = true
		}
		fmt.Printf("✓ All %d unique label references match labels.yml (%d labels in SSoT)\n", len(seen), len(ssot))
		os.Exit(0)
	}

	// Deduplicate unknown labels.
	unknownSet := make(map[string]bool)
	for _, r := range unknown {
		unknownSet[r.label] = true
	}
	unknownList := make([]string, 0, len(unknownSet))
	for l := range unknownSet {
		unknownList = append(unknownList, l)
	}
	sort.Strings(unknownList)

	fmt.Fprintf(os.Stderr, "\n✗ %d label(s) referenced in workflows but NOT defined in %s:\n\n", len(unknownList), labelsFile)
	for _, l := range unknownList {
		fmt.Fprintf(os.Stderr, "  • %s\n", l)
		for _, r := range unknown {
			if r.label == l {
				fmt.Fprintf(os.Stderr, "    %s:%d\n", r.file, r.line)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\nFix: add missing labels to %s or remove stale references from workflows.\n", labelsFile)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Label YAML parser (stdlib-only, matches sync-labels.go)
// ---------------------------------------------------------------------------

type labelEntry struct {
	name string
}

func parseLabelsYml(path string) ([]labelEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var labels []labelEntry
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- name:") {
			name := unquote(strings.TrimPrefix(trimmed, "- name:"))
			labels = append(labels, labelEntry{name: name})
		}
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
// Helpers (same pattern as sync-labels.go)
// ---------------------------------------------------------------------------

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
