package retry

import (
	"context"
	"strings"
	"time"
)

// Policy defines retry behavior for provider calls.
type Policy struct {
	MaxAttempts int
	BaseDelay   time.Duration
}

func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 3, BaseDelay: 200 * time.Millisecond}
}

// IsRetryable returns true for transient provider/network conditions.
func IsRetryable(err error) bool {
	if err == nil {
		return false
	}
	if err == context.Canceled || err == context.DeadlineExceeded {
		return false
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return false
	}
	transientHints := []string{
		"429",
		"rate limit",
		"too many requests",
		"timeout",
		"temporar",
		"connection reset",
		"connection refused",
		"eof",
		"502",
		"503",
		"504",
		"server error",
		"unavailable",
	}
	for _, hint := range transientHints {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// Do executes fn with retry/backoff for retryable errors.
func Do[T any](ctx context.Context, policy Policy, fn func() (T, error)) (T, error) {
	if policy.MaxAttempts <= 0 {
		policy.MaxAttempts = 1
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = 200 * time.Millisecond
	}

	var zero T
	var lastErr error
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		res, err := fn()
		if err == nil {
			return res, nil
		}
		lastErr = err
		if !IsRetryable(err) || attempt == policy.MaxAttempts {
			return zero, err
		}

		delay := policy.BaseDelay * time.Duration(attempt)
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		case <-time.After(delay):
		}
	}
	return zero, lastErr
}
