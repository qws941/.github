package labels

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseFileParsesQuotedUnquotedAndComments(t *testing.T) {
	path := writeFixture(t, `# Test labels
- name: bug
  color: d73a4a
  description: Something isn't working
- name: "enhancement"
  color: "#a2eeef"
  description: "New feature or request"
`)

	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(got))
	}
	if got[0].Name != "bug" || got[0].Color != "d73a4a" || got[0].Description != "Something isn't working" {
		t.Fatalf("unexpected first label: %#v", got[0])
	}
	if got[1].Name != "enhancement" || got[1].Color != "a2eeef" || got[1].Description != "New feature or request" {
		t.Fatalf("unexpected second label: %#v", got[1])
	}
}

func TestParseFileHandlesMultilineSpecialCharactersAndMissingFields(t *testing.T) {
	path := writeFixture(t, `- name: docs
  color: "#ABCDEF"
  description: |
    First line: keep punctuation [] {} : # !
    Second line with unicode-free symbols -> <= >=
- name: chore
  description: ""
`)

	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(got))
	}
	if got[0].Color != "abcdef" {
		t.Fatalf("expected normalized color, got %q", got[0].Color)
	}
	if !strings.Contains(got[0].Description, "First line: keep punctuation [] {} : # !") || !strings.Contains(got[0].Description, "Second line with unicode-free symbols -> <= >=") {
		t.Fatalf("expected multiline description, got %q", got[0].Description)
	}
	if got[1].Color != "" || got[1].Description != "" {
		t.Fatalf("expected missing fields to stay empty, got %#v", got[1])
	}
}

func TestParseFileHandlesEmptyFile(t *testing.T) {
	path := writeFixture(t, "")

	got, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no labels, got %d", len(got))
	}
}

func TestParseFileRejectsEmptyName(t *testing.T) {
	path := writeFixture(t, `- name: ""
  color: ff0000
  description: invalid
`)

	_, err := ParseFile(path)
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "label at index 0 has empty name") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseFilePerformance(t *testing.T) {
	path := writeFixture(t, `- name: bug
  color: "#ff0000"
  description: fast enough
`)

	start := time.Now()
	_, err := ParseFile(path)
	if err != nil {
		t.Fatalf("ParseFile returned error: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 100*time.Millisecond {
		t.Fatalf("expected parse under 100ms, got %s", elapsed)
	}
}

func TestUnquote(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "double quoted", in: ` "value" `, want: "value"},
		{name: "single quoted", in: " 'value' ", want: "value"},
		{name: "unquoted", in: " value ", want: "value"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Unquote(tc.in); got != tc.want {
				t.Fatalf("Unquote(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "labels.yml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}
