//go:build ignore

// scripts/validate-labels.go — Validate label references against labels.yml SSoT
//
// Scans all synced label-bearing files (workflow templates, labeler config,
// release-drafter config, issue templates, and caller workflows) to verify
// every referenced label exists in scripts/labels.yml.
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

	"scripts/internal/cli"
	"scripts/internal/fsutil"
	"scripts/internal/labels"
)

const (
	labelsFile = "scripts/labels.yml"
)

// scanTargets defines all file sets to validate.
// Each entry maps a glob pattern (relative to repo root) to its scan mode.
var scanTargets = []scanTarget{
	{glob: ".github/workflows/_*.yml", mode: modeQuotedAndCSV},
	{glob: ".github/workflows/automation-health.yml", mode: modeQuotedAndCSV},
	{glob: ".github/labeler.yml", mode: modeYAMLKeys},
	{glob: ".github/release-drafter.yml", mode: modeYAMLScalars},
	{glob: ".github/ISSUE_TEMPLATE/*.yml", mode: modeYAMLArray},
}

type scanMode int

const (
	modeQuotedAndCSV scanMode = iota // quoted strings + CSV (workflow templates, callers)
	modeYAMLKeys                     // unquoted YAML map keys (labeler.yml: 'type:docs:')
	modeYAMLScalars                  // unquoted YAML list scalars (release-drafter.yml: '- type:bug')
	modeYAMLArray                    // YAML array syntax (issue templates: labels: ["type:bug"])
)

type scanTarget struct {
	glob string
	mode scanMode
}

// Quoted label patterns for workflow YAML files.
var labelPatterns = []*regexp.Regexp{
	regexp.MustCompile(`['"]([a-z]+:[a-z][-a-z]*)['"]`),                             // type:bug, status:in-progress, priority:high, risk:low
	regexp.MustCompile(`['"]size/(xs|s|m|l|xl)['"]`),                                // size/xs .. size/xl
	regexp.MustCompile(`['"](auto-merge|opencode-agent|sync|keep-open|pinned)['"]`), // bare automation labels
}

// csvLabelPattern matches comma-separated label lists inside quotes.
// Example: 'keep-open,pinned,type:security'
var csvLabelPattern = regexp.MustCompile(`['"]([a-z][-a-z]*(?::[a-z][-a-z]*)?,(?:[a-z][-a-z]*(?::[a-z][-a-z]*)?,)*[a-z][-a-z]*(?::[a-z][-a-z]*)?)['"]`)

// individualLabelShape matches a single label token after CSV splitting.
var individualLabelShape = regexp.MustCompile(`^(?:[a-z]+:[a-z][-a-z]*|size/(?:xs|s|m|l|xl)|auto-merge|opencode-agent|sync|keep-open|pinned)$`)

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

// perFileAllowlist contains label-like tokens that are valid config values
// but intentionally absent from SSoT (e.g., release-drafter category shortcuts).
var perFileAllowlist = map[string]map[string]bool{
	"release-drafter.yml": {
		"feat":          true,
		"fix":           true,
		"documentation": true,
		"major":         true,
		"breaking":      true,
	},
}

// yamlKeyPattern matches unquoted YAML map keys that look like labels (labeler.yml).
// Example: 'type:docs:' at start of line → captures 'type:docs' (strips trailing colon).
var yamlKeyPattern = regexp.MustCompile(`^([a-z]+:[a-z][-a-z]*):$`)

// yamlScalarPattern matches unquoted plain YAML list scalars that look like labels.
// Example: '  - type:bug' → captures 'type:bug'.
var yamlScalarPattern = regexp.MustCompile(`^\s*-\s+([a-z]+:[a-z][-a-z]*)$`)

// yamlArrayLabelPattern matches labels inside YAML array syntax.
// Example: labels: ["type:bug", 'type:feature'] → captures each label.
var yamlArrayLabelPattern = regexp.MustCompile(`['"]([a-z]+:[a-z][-a-z]*)['"]`)

func main() {
	verbose := flag.Bool("verbose", false, "show matched labels and file locations")
	flag.Parse()

	// Resolve labels.yml from repo root.
	labelsPath, err := fsutil.ResolveFromRoot(labelsFile)
	if err != nil {
		cli.Fatal("labels.yml: %v", err)
	}

	// Parse SSoT labels.
	parsedLabels, err := labels.ParseFile(labelsPath)
	if err != nil {
		cli.Fatal("parse labels.yml: %v", err)
	}

	ssot := make(map[string]bool, len(parsedLabels))
	for _, l := range parsedLabels {
		ssot[l.Name] = true
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Loaded %d labels from %s\n", len(ssot), labelsFile)
	}

	// Collect all files to scan.
	var filesToScan []fileEntry

	for _, target := range scanTargets {
		targetPath, err := fsutil.ResolveFromRoot(filepath.Dir(target.glob))
		if err != nil {
			if *verbose {
				fmt.Fprintf(os.Stderr, "[VERBOSE] Skipping %s: directory not found\n", target.glob)
			}
			continue
		}

		pattern := filepath.Join(targetPath, filepath.Base(target.glob))
		matches, err := filepath.Glob(pattern)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: glob %s: %v\n", target.glob, err)
			continue
		}

		for _, m := range matches {
			filesToScan = append(filesToScan, fileEntry{path: m, mode: target.mode})
		}
	}

	if *verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Scanning %d files across %d target patterns\n", len(filesToScan), len(scanTargets))
	}

	// Scan each file for label references.
	type ref struct {
		file  string
		line  int
		label string
	}

	var allRefs []ref
	var unknown []ref

	githubDir, _ := fsutil.ResolveFromRoot(".github")

	for _, fe := range filesToScan {
		data, err := os.ReadFile(fe.path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: cannot read %s: %v\n", fe.path, err)
			continue
		}

		relPath, _ := filepath.Rel(filepath.Dir(githubDir), fe.path)
		baseName := filepath.Base(fe.path)
		lines := strings.Split(string(data), "\n")

		for lineNum, line := range lines {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "#") {
				continue
			}

			var extracted []string

			switch fe.mode {
			case modeQuotedAndCSV:
				// Phase 1: CSV lists.
				for _, m := range csvLabelPattern.FindAllStringSubmatch(line, -1) {
					for _, token := range strings.Split(m[1], ",") {
						token = strings.TrimSpace(token)
						if token != "" && individualLabelShape.MatchString(token) {
							extracted = append(extracted, token)
						}
					}
				}
				// Phase 2: Individually quoted labels.
				for _, pat := range labelPatterns {
					for _, m := range pat.FindAllStringSubmatch(line, -1) {
						labelName := m[1]
						if strings.Contains(pat.String(), "size/") {
							labelName = "size/" + labelName
						}
						extracted = append(extracted, labelName)
					}
				}

			case modeYAMLKeys:
				// Unquoted YAML map keys: 'type:docs:'
				if m := yamlKeyPattern.FindStringSubmatch(trimmed); m != nil {
					extracted = append(extracted, m[1])
				}

			case modeYAMLScalars:
				// Unquoted plain YAML list scalars: '  - type:bug'
				if m := yamlScalarPattern.FindStringSubmatch(line); m != nil {
					extracted = append(extracted, m[1])
				}

			case modeYAMLArray:
				// YAML array syntax: labels: ["type:bug"]
				for _, m := range yamlArrayLabelPattern.FindAllStringSubmatch(line, -1) {
					extracted = append(extracted, m[1])
				}
			}

			// Validate extracted labels.
			for _, labelName := range extracted {
				if falsePositives[labelName] {
					continue
				}
				if allow, ok := perFileAllowlist[baseName]; ok && allow[labelName] {
					continue
				}

				allRefs = append(allRefs, ref{file: relPath, line: lineNum + 1, label: labelName})
				if !ssot[labelName] {
					unknown = append(unknown, ref{file: relPath, line: lineNum + 1, label: labelName})
				}
			}
		}
	}

	// Report results.
	if *verbose {
		fmt.Fprintf(os.Stderr, "[VERBOSE] Found %d label references across %d files\n", len(allRefs), len(filesToScan))

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

	fmt.Fprintf(os.Stderr, "\n✗ %d label(s) referenced but NOT defined in %s:\n\n", len(unknownList), labelsFile)
	for _, l := range unknownList {
		fmt.Fprintf(os.Stderr, "  • %s\n", l)
		for _, r := range unknown {
			if r.label == l {
				fmt.Fprintf(os.Stderr, "    %s:%d\n", r.file, r.line)
			}
		}
	}
	fmt.Fprintf(os.Stderr, "\nFix: add missing labels to %s or remove stale references.\n", labelsFile)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Label YAML parser (stdlib-only, matches sync-labels.go)
// ---------------------------------------------------------------------------

type fileEntry struct {
	path string
	mode scanMode
}

// ---------------------------------------------------------------------------
// Helpers (same pattern as sync-labels.go)
// ---------------------------------------------------------------------------
