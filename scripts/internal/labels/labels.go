// Package labels provides shared label parsing helpers used by scripts.
package labels

import (
	"fmt"
	"os"
	"strings"
)

// Label is a single label definition from labels.yml.
type Label struct {
	Name        string
	Color       string
	Description string
}

// ParseFile parses label definitions from a YAML file without external dependencies.
func ParseFile(path string) ([]Label, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var out []Label
	var cur *Label

	inDescriptionBlock := false
	var descriptionLines []string

	appendCurrent := func() error {
		if cur == nil {
			return nil
		}
		if cur.Name == "" {
			return fmt.Errorf("label at index %d has empty name", len(out))
		}
		out = append(out, *cur)
		return nil
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if inDescriptionBlock {
			if strings.HasPrefix(line, "    ") {
				descriptionLines = append(descriptionLines, strings.TrimPrefix(line, "    "))
				continue
			}
			if trimmed == "" {
				descriptionLines = append(descriptionLines, "")
				continue
			}
			if cur != nil {
				cur.Description = strings.Join(descriptionLines, "\n")
			}
			inDescriptionBlock = false
			descriptionLines = nil
		}

		switch {
		case strings.HasPrefix(trimmed, "- name:"):
			if err := appendCurrent(); err != nil {
				return nil, err
			}
			cur = &Label{Name: Unquote(strings.TrimPrefix(trimmed, "- name:"))}

		case strings.HasPrefix(trimmed, "color:") && cur != nil:
			color := Unquote(strings.TrimPrefix(trimmed, "color:"))
			color = strings.TrimPrefix(color, "#")
			cur.Color = strings.ToLower(color)

		case strings.HasPrefix(trimmed, "description:") && cur != nil:
			raw := strings.TrimSpace(strings.TrimPrefix(trimmed, "description:"))
			if raw == "|" {
				inDescriptionBlock = true
				descriptionLines = nil
				continue
			}
			cur.Description = Unquote(raw)
		}
	}

	if inDescriptionBlock && cur != nil {
		cur.Description = strings.Join(descriptionLines, "\n")
	}

	if err := appendCurrent(); err != nil {
		return nil, err
	}

	return out, nil
}

// Unquote trims outer single or double quotes and surrounding whitespace.
func Unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 {
		return s
	}
	if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
