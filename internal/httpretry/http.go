// Package httpretry provides dependency-free retry policy for external HTTP
// clients, including safe handling for streaming responses.
package httpretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxRetries = 3
	DefaultMaxBackoff = 30 * time.Second
)

type Policy struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxBackoff time.Duration
	After      func(time.Duration) <-chan time.Time
	OnRetry    func(RetryEvent)
	// WrapNetworkError lets a provider preserve its public typed errors.
	WrapNetworkError func(error) error
	// AllowReplay explicitly permits repeating an operation that may be
	// non-idempotent. It is false by default so generic callers cannot
	// accidentally duplicate POST side effects.
	AllowReplay bool
	// RetryableError may extend the default transient-error classification.
	RetryableError func(error) bool
	Now            func() time.Time
}

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type retryContextKey struct{}

// RetryEvent describes a retry that will be attempted. Attempt is the
// one-based retry number (the initial request is not counted as a retry).
type RetryEvent struct {
	Attempt    int
	MaxRetries int
	Delay      time.Duration
	StatusCode int
	Err        error
}

// StreamError marks a failure that happened while consuming a successful HTTP
// streaming response, as opposed to while establishing the request.
type StreamError struct{ Err error }

func (e *StreamError) Error() string { return "stream read: " + e.Err.Error() }
func (e *StreamError) Unwrap() error { return e.Err }

func NewStreamError(err error) error {
	if err == nil {
		return nil
	}
	return &StreamError{Err: err}
}

func DefaultPolicy() Policy {
	return Policy{
		MaxRetries: DefaultMaxRetries,
		BaseDelay:  time.Second,
		MaxBackoff: DefaultMaxBackoff,
		After:      time.After,
		Now:        time.Now,
	}
}

func IsRetryableStatus(statusCode int) bool {
	switch statusCode {
	case http.StatusRequestTimeout,
		http.StatusTooManyRequests,
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
		http.StatusGatewayTimeout,
		529: // Provider overloaded.
		return true
	default:
		return false
	}
}

func IsRetryableNetworkError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{"timeout", "timed out", "connection refused", "connection reset", "network is unreachable", "no such host", "broken pipe", "unexpected eof", "tls handshake"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

// IsRetryableError recognizes transient transport and provider errors. It is
// intentionally conservative and is only acted on when replay is allowed.
func IsRetryableError(err error) bool {
	if IsRetryableNetworkError(err) {
		return true
	}
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, hint := range []string{"rate limit", "too many requests", "overloaded", "temporar", "unavailable", "server error", "408", "429", "500", "502", "503", "504", "529"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func Backoff(retry int, resp *http.Response, baseDelay time.Duration, now ...time.Time) time.Duration {
	if resp != nil {
		retryAfter := strings.TrimSpace(resp.Header.Get("Retry-After"))
		if seconds, err := strconv.Atoi(retryAfter); err == nil && seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
		if at, err := http.ParseTime(retryAfter); err == nil {
			current := time.Now()
			if len(now) > 0 {
				current = now[0]
			}
			if delay := at.Sub(current); delay > 0 {
				return delay
			}
		}
	}
	if baseDelay <= 0 {
		baseDelay = time.Second
	}
	return baseDelay * time.Duration(1<<uint(retry))
}

// Do executes a fresh request for every attempt. It retries transient network
// errors and provider statuses, returning the final response unchanged for
// provider-specific error parsing.
func Do(ctx context.Context, client Doer, buildReq func() (*http.Request, error), policy Policy) (*http.Response, error) {
	policy = normalize(policy)
	if nested, _ := ctx.Value(retryContextKey{}).(bool); nested {
		policy.MaxRetries = 0
	}
	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		resp, err := client.Do(req)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if attempt == policy.MaxRetries || !requestReplayable(req, policy) || !retryableError(policy, err) {
				if policy.WrapNetworkError != nil {
					err = policy.WrapNetworkError(err)
				}
				return nil, fmt.Errorf("send request: %w", err)
			}
			delay := Backoff(attempt, nil, policy.BaseDelay, policy.Now())
			notify(policy, attempt+1, delay, 0, err)
			if err := wait(ctx, policy.After, delay); err != nil {
				return nil, err
			}
			continue
		}
		if !IsRetryableStatus(resp.StatusCode) || attempt == policy.MaxRetries || !requestReplayable(req, policy) {
			return resp, nil
		}
		delay := Backoff(attempt, resp, policy.BaseDelay, policy.Now())
		if delay > policy.MaxBackoff {
			return resp, nil
		}
		drainAndClose(resp.Body)
		notify(policy, attempt+1, delay, resp.StatusCode, nil)
		if err := wait(ctx, policy.After, delay); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("retry loop exited unexpectedly")
}

// DoStream retries a stream read only when the failed attempt emitted no text,
// thinking, or tool activity. This avoids replaying partial turns and tool calls.
func DoStream[T any](ctx context.Context, policy Policy, fn func(context.Context) (result T, observed bool, err error)) (T, error) {
	policy = normalize(policy)
	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		// A streamed operation owns the retry budget. Any HTTP helper it calls
		// must make a single attempt so nested policies cannot multiply it.
		attemptCtx := context.WithValue(ctx, retryContextKey{}, true)
		result, observed, err := fn(attemptCtx)
		if err == nil {
			return result, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, ctxErr
		}
		if observed || attempt == policy.MaxRetries || !policy.AllowReplay || !retryableError(policy, err) {
			return result, err
		}
		delay := Backoff(attempt, nil, policy.BaseDelay, policy.Now())
		notify(policy, attempt+1, delay, 0, err)
		if err := wait(ctx, policy.After, delay); err != nil {
			var zero T
			return zero, err
		}
	}
	var zero T
	return zero, errors.New("stream retry loop exited unexpectedly")
}

func normalize(policy Policy) Policy {
	if policy.MaxRetries < 0 {
		policy.MaxRetries = 0
	}
	if policy.BaseDelay <= 0 {
		policy.BaseDelay = time.Second
	}
	if policy.MaxBackoff <= 0 {
		policy.MaxBackoff = DefaultMaxBackoff
	}
	if policy.After == nil {
		policy.After = time.After
	}
	if policy.Now == nil {
		policy.Now = time.Now
	}
	return policy
}

func retryableError(policy Policy, err error) bool {
	if IsRetryableError(err) {
		return true
	}
	return policy.RetryableError != nil && policy.RetryableError(err)
}

func requestReplayable(req *http.Request, policy Policy) bool {
	if policy.AllowReplay || strings.TrimSpace(req.Header.Get("Idempotency-Key")) != "" {
		return true
	}
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}

func notify(policy Policy, attempt int, delay time.Duration, statusCode int, err error) {
	if policy.OnRetry != nil {
		policy.OnRetry(RetryEvent{Attempt: attempt, MaxRetries: policy.MaxRetries, Delay: delay, StatusCode: statusCode, Err: err})
	}
}

func wait(ctx context.Context, after func(time.Duration) <-chan time.Time, delay time.Duration) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-after(delay):
		return nil
	}
}
