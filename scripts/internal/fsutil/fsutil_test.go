package fsutil

import (
	"strings"
	"testing"
)

func TestResolveFromRoot(t *testing.T) {
	tests := []struct {
		name      string
		rel       string
		wantErr   bool
		errSubstr string
	}{
		{name: "found file", rel: "../../go.mod", wantErr: false},
		{name: "not found file", rel: "../../does-not-exist.file", wantErr: true, errSubstr: "does-not-exist.file not found — run from repo root"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ResolveFromRoot(tt.rel)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ResolveFromRoot(%q) expected error, got nil", tt.rel)
				}
				if !strings.Contains(err.Error(), tt.errSubstr) {
					t.Fatalf("ResolveFromRoot(%q) error = %q, want substring %q", tt.rel, err.Error(), tt.errSubstr)
				}
				if got != "" {
					t.Fatalf("ResolveFromRoot(%q) path = %q, want empty string", tt.rel, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("ResolveFromRoot(%q) returned error: %v", tt.rel, err)
			}
			if got == "" {
				t.Fatalf("ResolveFromRoot(%q) returned empty path", tt.rel)
			}
		})
	}
}
