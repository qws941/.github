package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strings"
	"syscall"
	"time"
)

type Config struct {
	MaxRetries  int
	BaseDelay   time.Duration
	MaxDelay    time.Duration
	Jitter      bool
	RetryableFn func(error) bool
}

type statusCoder interface {
	StatusCode() int
}

type temporary interface {
	Temporary() bool
}

var randInt63n = rand.Int63n

func Do(ctx context.Context, cfg Config, fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	retryable := cfg.RetryableFn
	if retryable == nil {
		retryable = IsRetryableHTTPError
	}

	for attempt := 0; ; attempt++ {
		err := fn()
		if err == nil {
			return nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if !retryable(err) || attempt >= cfg.MaxRetries {
			return err
		}

		delay := retryDelay(cfg, attempt)
		if delay <= 0 {
			continue
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func IsRetryableHTTPError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}

	status, ok := httpStatusCode(err)
	if ok {
		switch {
		case status == 429:
			return true
		case status == 401 || status == 404:
			return false
		case status == 403:
			return isRateLimitMessage(err.Error())
		case status >= 500 && status <= 599:
			return true
		default:
			return false
		}
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}

	var tempErr temporary
	if errors.As(err, &tempErr) && tempErr.Temporary() {
		return true
	}

	if errors.Is(err, os.ErrDeadlineExceeded) || os.IsTimeout(err) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "timeout") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "connection reset") {
		return true
	}

	return false
}

func retryDelay(cfg Config, attempt int) time.Duration {
	base := cfg.BaseDelay
	if base < 0 {
		base = 0
	}

	delay := base
	for i := 0; i < attempt; i++ {
		if delay > 0 && delay > time.Duration(^uint64(0)>>1)/2 {
			delay = time.Duration(^uint64(0) >> 1)
			break
		}
		delay *= 2
	}

	if cfg.MaxDelay > 0 && delay > cfg.MaxDelay {
		delay = cfg.MaxDelay
	}
	if cfg.Jitter && delay > 0 {
		delay = time.Duration(randInt63n(int64(delay) + 1))
	}
	return delay
}

func httpStatusCode(err error) (int, bool) {
	var coder statusCoder
	if errors.As(err, &coder) {
		return coder.StatusCode(), true
	}

	for code := 401; code <= 599; code++ {
		needle := fmt.Sprintf("http %d", code)
		if strings.Contains(strings.ToLower(err.Error()), needle) {
			return code, true
		}
	}

	return 0, false
}

func isRateLimitMessage(message string) bool {
	msg := strings.ToLower(message)
	return strings.Contains(msg, "rate limit") || strings.Contains(msg, "secondary limit") || strings.Contains(msg, "abuse detection")
}
