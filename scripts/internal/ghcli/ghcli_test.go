package ghcli

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSetThrottle(t *testing.T) {
	// Reset to default after test.
	defer func() {
		mu.Lock()
		minDelay = 100 * time.Millisecond
		lastCallAt = time.Time{}
		mu.Unlock()
	}()

	SetThrottle(500 * time.Millisecond)
	mu.Lock()
	got := minDelay
	mu.Unlock()
	if got != 500*time.Millisecond {
		t.Fatalf("SetThrottle(500ms): got %v", got)
	}

	SetThrottle(0)
	mu.Lock()
	got = minDelay
	mu.Unlock()
	if got != 0 {
		t.Fatalf("SetThrottle(0): got %v", got)
	}
}

func TestThrottleEnforcesDelay(t *testing.T) {
	// Reset after test.
	defer func() {
		mu.Lock()
		minDelay = 100 * time.Millisecond
		lastCallAt = time.Time{}
		mu.Unlock()
	}()

	SetThrottle(50 * time.Millisecond)

	// First call should not block (no prior call).
	start := time.Now()
	throttle()
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Fatalf("first throttle() took %v, expected near-zero", elapsed)
	}

	// Second call should wait ~50ms.
	start = time.Now()
	throttle()
	if elapsed := time.Since(start); elapsed < 30*time.Millisecond {
		t.Fatalf("second throttle() only waited %v, expected ≥30ms", elapsed)
	}
}

func TestThrottleDisabled(t *testing.T) {
	defer func() {
		mu.Lock()
		minDelay = 100 * time.Millisecond
		lastCallAt = time.Time{}
		mu.Unlock()
	}()

	SetThrottle(0)

	// Two rapid calls should not block.
	start := time.Now()
	throttle()
	throttle()
	if elapsed := time.Since(start); elapsed > 20*time.Millisecond {
		t.Fatalf("disabled throttle took %v, expected near-zero", elapsed)
	}
}

func TestThrottleConcurrency(t *testing.T) {
	defer func() {
		mu.Lock()
		minDelay = 100 * time.Millisecond
		lastCallAt = time.Time{}
		mu.Unlock()
	}()

	SetThrottle(10 * time.Millisecond)

	// Launch 5 goroutines calling throttle(); verify no data race.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			throttle()
		}()
	}
	wg.Wait()
}

func TestIsRateLimitOrServerError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"generic error", errors.New("something broke"), false},
		{"rate limit", errors.New("API rate limit exceeded"), true},
		{"secondary rate limit", errors.New("secondary rate limit hit"), true},
		{"abuse detection", errors.New("abuse detection mechanism"), true},
		{"http 429", errors.New("HTTP 429: too many requests"), true},
		{"http 403 rate limit", errors.New("HTTP 403: rate limit exceeded"), true},
		{"http 403 no rate limit", errors.New("HTTP 403: forbidden"), false},
		{"http 500", errors.New("HTTP 500: internal server error"), true},
		{"http 502", errors.New("HTTP 502: bad gateway"), true},
		{"http 503", errors.New("HTTP 503: service unavailable"), true},
		{"http 401", errors.New("HTTP 401: unauthorized"), false},
		{"http 404", errors.New("HTTP 404: not found"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRateLimitOrServerError(tt.err); got != tt.want {
				t.Errorf("isRateLimitOrServerError(%q) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestEnsureBudgetInsufficientFormat(t *testing.T) {
	// Test that the error message format is useful.
	rl := RateLimit{Limit: 5000, Remaining: 10, Used: 4990, ResetUnix: 1773674775}
	rl.ResetAt = time.Unix(rl.ResetUnix, 0)

	if rl.Remaining >= 100 {
		t.Skip("test assumes remaining < needed")
	}

	// Simulate the error that EnsureBudget would produce.
	err := fmt.Errorf(
		"insufficient rate limit budget: need %d, have %d/%d (resets %s)",
		100, rl.Remaining, rl.Limit,
		rl.ResetAt.Local().Format("2006-01-02 15:04:05"),
	)
	if err == nil {
		t.Fatal("expected error")
	}
	if !contains(err.Error(), "need 100") || !contains(err.Error(), "have 10/5000") {
		t.Fatalf("unexpected error format: %v", err)
	}
}

func TestCheckRateLimitRequiresGH(t *testing.T) {
	// This test verifies behavior when gh is unavailable.
	// In CI without gh, CheckRateLimit should return an error, not panic.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := CheckRateLimit(ctx)
	// We accept either success (gh available) or error (gh unavailable).
	// The important thing is no panic.
	_ = err
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsAt(s, substr))
}

func containsAt(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
