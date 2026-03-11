//go:build sync_labels

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSyncLabelsParseLabelsYmlParsesQuotedUnquotedAndComments(t *testing.T) {
	path := writeSyncLabelsFixture(t, `# Test labels
- name: bug
  color: d73a4a
  description: Something isn't working
- name: "enhancement"
  color: "#a2eeef"
  description: "New feature or request"
`)

	labels, err := parseLabelsYml(path)
	if err != nil {
		t.Fatalf("parseLabelsYml returned error: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
	if labels[0].Name != "bug" || labels[0].Color != "d73a4a" || labels[0].Description != "Something isn't working" {
		t.Fatalf("unexpected first label: %#v", labels[0])
	}
	if labels[1].Name != "enhancement" || labels[1].Color != "a2eeef" || labels[1].Description != "New feature or request" {
		t.Fatalf("unexpected second label: %#v", labels[1])
	}
}

func TestSyncLabelsParseLabelsYmlHandlesMultilineSpecialCharactersAndMissingFields(t *testing.T) {
	path := writeSyncLabelsFixture(t, `- name: docs
  color: "#ABCDEF"
  description: |
    First line: keep punctuation [] {} : # !
    Second line with unicode-free symbols -> <= >=
- name: chore
  description: ""
`)

	labels, err := parseLabelsYml(path)
	if err != nil {
		t.Fatalf("parseLabelsYml returned error: %v", err)
	}
	if len(labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(labels))
	}
	if labels[0].Color != "abcdef" {
		t.Fatalf("expected normalized color, got %q", labels[0].Color)
	}
	if !strings.Contains(labels[0].Description, "First line: keep punctuation [] {} : # !") || !strings.Contains(labels[0].Description, "Second line with unicode-free symbols -> <= >=") {
		t.Fatalf("expected multiline description, got %q", labels[0].Description)
	}
	if labels[1].Color != "" || labels[1].Description != "" {
		t.Fatalf("expected missing fields to stay empty, got %#v", labels[1])
	}
}

func TestSyncLabelsParseLabelsYmlHandlesEmptyFile(t *testing.T) {
	path := writeSyncLabelsFixture(t, "")

	labels, err := parseLabelsYml(path)
	if err != nil {
		t.Fatalf("parseLabelsYml returned error: %v", err)
	}
	if len(labels) != 0 {
		t.Fatalf("expected no labels, got %d", len(labels))
	}
}

func TestSyncLabelsParseLabelsYmlRejectsEmptyName(t *testing.T) {
	path := writeSyncLabelsFixture(t, `- name: ""
  color: ff0000
  description: invalid
`)

	_, err := parseLabelsYml(path)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "label at index 0 has empty name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSyncLabelsParseLabelsYmlPerformance(t *testing.T) {
	path := writeSyncLabelsFixture(t, `- name: bug
  color: "#ff0000"
  description: fast enough
`)

	start := time.Now()
	_, err := parseLabelsYml(path)
	if err != nil {
		t.Fatalf("parseLabelsYml returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("expected parse under 100ms, got %s", elapsed)
	}
}

func writeSyncLabelsFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "labels.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}
