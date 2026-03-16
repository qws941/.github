// Package fsutil provides filesystem helpers shared by scripts in this repository.
package fsutil

import (
	"fmt"
	"os"
	"path/filepath"
)

// ResolveFromRoot returns the absolute path of rel if it exists, otherwise an error.
// This is designed to locate files relative to the repository root.
func ResolveFromRoot(rel string) (string, error) {
	if _, err := os.Stat(rel); err == nil {
		abs, _ := filepath.Abs(rel)
		return abs, nil
	}
	return "", fmt.Errorf("%s not found — run from repo root", rel)
}
