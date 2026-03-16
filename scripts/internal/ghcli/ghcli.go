// Package ghcli provides a rate-limit-aware wrapper around the gh CLI.
//
// Every gh command issued through this package is subject to:
//   - Inter-request throttling (configurable, default 100ms)
//   - Automatic retry on rate-limit errors (429, 403 with rate-limit message)
//   - Pre-flight budget checking via the /rate_limit API endpoint
//
// Usage:
//
//	ctx := context.Background()
//	ghcli.SetThrottle(200 * time.Millisecond)
//
//	rl, err := ghcli.EnsureBudget(ctx, 100)
//	if err != nil {
//	    log.Fatalf("insufficient budget: %v", err)
//	}
//	fmt.Printf("Budget OK: %d remaining\n", rl.Remaining)
//
//	out, err := ghcli.Output(ctx, "label", "list", "--repo", "owner/repo", "--json", "name")
package ghcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"

	"scripts/internal/retry"
)

// RateLimit holds the current GitHub REST API core rate-limit status.
type RateLimit struct {
	Limit     int   `json:"limit"`
	Remaining int   `json:"remaining"`
	Used      int   `json:"used"`
	ResetUnix int64 `json:"reset"`
	ResetAt   time.Time
}

// throttle state — package-level, mutex-guarded.
var (
	mu         sync.Mutex
	minDelay   = 100 * time.Millisecond
	lastCallAt time.Time
)

// SetThrottle sets the minimum delay between consecutive gh CLI invocations.
// The default is 100ms. Set to 0 to disable throttling.
func SetThrottle(d time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	minDelay = d
}

// CheckRateLimit queries GitHub's /rate_limit endpoint (free, does not count
// against the rate limit) and returns the core resource status.
func CheckRateLimit(ctx context.Context) (RateLimit, error) {
	out, err := rawOutput(ctx, "api", "rate_limit", "--jq", ".resources.core")
	if err != nil {
		return RateLimit{}, fmt.Errorf("check rate limit: %w", err)
	}
	var rl RateLimit
	if err := json.Unmarshal([]byte(out), &rl); err != nil {
		return RateLimit{}, fmt.Errorf("parse rate limit: %w", err)
	}
	rl.ResetAt = time.Unix(rl.ResetUnix, 0)
	return rl, nil
}

// EnsureBudget verifies that at least needed API calls remain in the current
// rate-limit window. Returns the current status on success, or an error
// describing the shortfall and when the window resets.
func EnsureBudget(ctx context.Context, needed int) (RateLimit, error) {
	rl, err := CheckRateLimit(ctx)
	if err != nil {
		return rl, err
	}
	if rl.Remaining < needed {
		return rl, fmt.Errorf(
			"insufficient rate limit budget: need %d, have %d/%d (resets %s)",
			needed, rl.Remaining, rl.Limit,
			rl.ResetAt.Local().Format("2006-01-02 15:04:05"),
		)
	}
	return rl, nil
}

// Output runs a gh CLI command with inter-request throttling and automatic
// retry on rate-limit errors (HTTP 429, 403 with rate-limit message, 5xx).
//
// The retry policy: up to 3 retries, exponential backoff from 5s to 60s
// with jitter.
func Output(ctx context.Context, args ...string) (string, error) {
	var result string
	err := retry.Do(ctx, retry.Config{
		MaxRetries: 3,
		BaseDelay:  5 * time.Second,
		MaxDelay:   60 * time.Second,
		Jitter:     true,
		RetryableFn: func(err error) bool {
			return isRateLimitOrServerError(err)
		},
	}, func() error {
		throttle()
		out, callErr := rawOutput(ctx, args...)
		if callErr != nil {
			return callErr
		}
		result = out
		return nil
	})
	return result, err
}

// OutputJSON runs a gh CLI command and unmarshals the JSON result into target.
func OutputJSON(ctx context.Context, target any, args ...string) error {
	out, err := Output(ctx, args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), target)
}

// throttle enforces the minimum inter-request delay.
func throttle() {
	mu.Lock()
	defer mu.Unlock()
	if minDelay <= 0 {
		return
	}
	if !lastCallAt.IsZero() {
		elapsed := time.Since(lastCallAt)
		if elapsed < minDelay {
			time.Sleep(minDelay - elapsed)
		}
	}
	lastCallAt = time.Now()
}

// rawOutput runs gh with the given arguments and returns trimmed stdout.
func rawOutput(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(stdout.String()), nil
}

// isRateLimitOrServerError returns true for errors that should trigger a retry:
// HTTP 429, 403 with rate-limit language, and 5xx server errors.
func isRateLimitOrServerError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())

	// Rate limit signals.
	if strings.Contains(msg, "rate limit") ||
		strings.Contains(msg, "secondary rate limit") ||
		strings.Contains(msg, "abuse detection") {
		return true
	}

	// HTTP status codes embedded in gh CLI error output.
	if strings.Contains(msg, "http 429") {
		return true
	}
	if strings.Contains(msg, "http 403") &&
		(strings.Contains(msg, "rate limit") || strings.Contains(msg, "secondary")) {
		return true
	}

	// Server errors.
	for code := 500; code <= 599; code++ {
		if strings.Contains(msg, fmt.Sprintf("http %d", code)) {
			return true
		}
	}

	return false
}
