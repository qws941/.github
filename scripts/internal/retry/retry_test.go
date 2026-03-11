package retry

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"sync"
	"syscall"
	"testing"
	"time"
)

type httpError struct {
	code int
	msg  string
}

func (e httpError) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("HTTP %d", e.code)
}

func (e httpError) StatusCode() int {
	return e.code
}

type timeoutNetError struct{}

func (timeoutNetError) Error() string   { return "timeout" }
func (timeoutNetError) Timeout() bool   { return true }
func (timeoutNetError) Temporary() bool { return false }

type temporaryError struct{}

func (temporaryError) Error() string   { return "temporary" }
func (temporaryError) Timeout() bool   { return false }
func (temporaryError) Temporary() bool { return true }

type onlyTimeoutError struct{}

func (onlyTimeoutError) Error() string { return "only timeout" }
func (onlyTimeoutError) Timeout() bool { return true }

var _ net.Error = timeoutNetError{}
var _ net.Error = temporaryError{}

func TestDoSuccessfulFirstAttempt(t *testing.T) {
	t.Parallel()

	var attempts int
	err := Do(context.Background(), Config{MaxRetries: 3}, func() error {
		attempts++
		return nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestDoCanceledContextBeforeStart(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var attempts int
	err := Do(ctx, Config{MaxRetries: 3}, func() error {
		attempts++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() error = %v, want context.Canceled", err)
	}
	if attempts != 0 {
		t.Fatalf("attempts = %d, want 0", attempts)
	}
}

func TestDoRetryOnTransientErrorThenSuccess(t *testing.T) {
	t.Parallel()

	var attempts int
	err := Do(context.Background(), Config{MaxRetries: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}, func() error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("temporary failure: %w", syscall.ECONNREFUSED)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestDoRetryWithoutDelayWhenBackoffZero(t *testing.T) {
	t.Parallel()

	var attempts int
	err := Do(context.Background(), Config{MaxRetries: 1, RetryableFn: func(error) bool { return true }}, func() error {
		attempts++
		if attempts == 1 {
			return errors.New("retry once")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestDoNoRetryOnPermanentError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "unauthorized", err: httpError{code: 401}},
		{name: "forbidden", err: httpError{code: 403}},
		{name: "not found", err: httpError{code: 404}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var attempts int
			err := Do(context.Background(), Config{MaxRetries: 5, BaseDelay: time.Millisecond}, func() error {
				attempts++
				return tc.err
			})
			if !errors.Is(err, tc.err) {
				t.Fatalf("Do() error = %v, want %v", err, tc.err)
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1", attempts)
			}
		})
	}
}

func TestDoContextCancellationStopsRetry(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var attempts int
	started := make(chan struct{}, 1)
	errCh := make(chan error, 1)
	go func() {
		errCh <- Do(ctx, Config{MaxRetries: 10, BaseDelay: 50 * time.Millisecond}, func() error {
			attempts++
			if attempts == 1 {
				started <- struct{}{}
			}
			return fmt.Errorf("retry me: %w", syscall.ECONNREFUSED)
		})
	}()

	<-started
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Do() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Do() did not stop after context cancellation")
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

func TestDoMaxRetryExhaustionReturnsLastError(t *testing.T) {
	t.Parallel()

	want := errors.New("still failing")
	var attempts int
	err := Do(context.Background(), Config{
		MaxRetries: 2,
		BaseDelay:  time.Millisecond,
		MaxDelay:   time.Millisecond,
		RetryableFn: func(error) bool {
			return true
		},
	}, func() error {
		attempts++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("Do() error = %v, want %v", err, want)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestDoReturnsContextErrorFromFunction(t *testing.T) {
	t.Parallel()

	err := Do(context.Background(), Config{MaxRetries: 5}, func() error {
		return context.DeadlineExceeded
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Do() error = %v, want context.DeadlineExceeded", err)
	}
}

func TestRetryDelayJitterAddsRandomness(t *testing.T) {
	t.Parallel()

	original := randInt63n
	defer func() { randInt63n = original }()

	var mu sync.Mutex
	values := []int64{2, 7}
	randInt63n = func(int64) int64 {
		mu.Lock()
		defer mu.Unlock()
		v := values[0]
		values = values[1:]
		return v
	}

	cfg := Config{BaseDelay: 10 * time.Millisecond, Jitter: true}
	first := retryDelay(cfg, 0)
	second := retryDelay(cfg, 0)

	if first == second {
		t.Fatalf("retryDelay() returned identical jittered delays: %v", first)
	}
	if first != 2 || second != 7 {
		t.Fatalf("retryDelay() = %v, %v, want 2ns and 7ns", first, second)
	}
}

func TestRetryDelayClampsAndSanitizes(t *testing.T) {
	t.Parallel()

	const maxDuration = time.Duration(1<<63 - 1)

	if got := retryDelay(Config{BaseDelay: -time.Second}, 2); got != 0 {
		t.Fatalf("retryDelay() with negative base = %v, want 0", got)
	}
	if got := retryDelay(Config{BaseDelay: 10 * time.Millisecond, MaxDelay: 15 * time.Millisecond}, 2); got != 15*time.Millisecond {
		t.Fatalf("retryDelay() with max clamp = %v, want 15ms", got)
	}
	if got := retryDelay(Config{BaseDelay: maxDuration}, 1); got != maxDuration {
		t.Fatalf("retryDelay() with overflow clamp = %v, want %v", got, maxDuration)
	}
}

func TestIsRetryableHTTPError(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "context canceled", err: context.Canceled, want: false},
		{name: "context deadline", err: context.DeadlineExceeded, want: false},
		{name: "status 429", err: httpError{code: 429}, want: true},
		{name: "status 500", err: httpError{code: 500}, want: true},
		{name: "status 503 string", err: errors.New("received HTTP 503 from api"), want: true},
		{name: "status 403 rate limit", err: httpError{code: 403, msg: "HTTP 403 rate limit exceeded"}, want: true},
		{name: "status 403 forbidden", err: httpError{code: 403, msg: "HTTP 403 forbidden"}, want: false},
		{name: "status 418", err: httpError{code: 418}, want: false},
		{name: "status 401", err: httpError{code: 401}, want: false},
		{name: "status 404", err: httpError{code: 404}, want: false},
		{name: "net timeout", err: timeoutNetError{}, want: true},
		{name: "temporary error", err: temporaryError{}, want: true},
		{name: "os timeout", err: onlyTimeoutError{}, want: true},
		{name: "os deadline exceeded", err: os.ErrDeadlineExceeded, want: true},
		{name: "timeout text", err: errors.New("request timeout while connecting"), want: true},
		{name: "connection refused text", err: errors.New("dial tcp: connection refused"), want: true},
		{name: "connection reset text", err: errors.New("read: connection reset by peer"), want: true},
		{name: "conn refused errno", err: syscall.ECONNREFUSED, want: true},
		{name: "conn reset errno", err: syscall.ECONNRESET, want: true},
		{name: "other", err: errors.New("bad request"), want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsRetryableHTTPError(tc.err); got != tc.want {
				t.Fatalf("IsRetryableHTTPError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
