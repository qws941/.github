package cli

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestFatalUsesExitFunc(t *testing.T) {
	originalExit := ExitFunc
	originalStderr := os.Stderr
	defer func() {
		ExitFunc = originalExit
		os.Stderr = originalStderr
	}()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe failed: %v", err)
	}
	os.Stderr = w

	exitCode := 0
	ExitFunc = func(code int) {
		exitCode = code
	}

	Fatal("boom: %d", 7)

	if err := w.Close(); err != nil {
		t.Fatalf("closing write pipe failed: %v", err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("reading stderr output failed: %v", err)
	}
	if err := r.Close(); err != nil {
		t.Fatalf("closing read pipe failed: %v", err)
	}

	if exitCode != 1 {
		t.Fatalf("exit code = %d, want 1", exitCode)
	}
	if got, want := string(out), "Error: boom: 7\n"; !strings.Contains(got, want) {
		t.Fatalf("stderr output = %q, want %q", got, want)
	}
}
