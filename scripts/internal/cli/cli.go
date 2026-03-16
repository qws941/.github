// Package cli provides common CLI helpers shared by scripts in this repository.
package cli

import (
	"fmt"
	"os"
)

// ExitFunc is the function called by Fatal. Override in tests to prevent os.Exit.
var ExitFunc = os.Exit

// Fatal prints a formatted error message to stderr and exits with code 1.
func Fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	ExitFunc(1)
}
