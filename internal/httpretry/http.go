// Package httpretry provides dependency-free retry policy for external HTTP
// clients, including safe handling for streaming responses.
package httpretry

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultMaxRetries = 3
	DefaultMaxBackoff = 30 * time.Second

	// Stream turn retry defaults match the Codex bundle's Responses stream
	// retry behavior while remaining provider-neutral for OpenVibely callers.
	StreamTurnMaxRetries        = 5
	StreamTurnBaseDelay         = 200 * time.Millisecond
	StreamTurnMaxBackoff        = 60 * time.Second
	ConnectionRetryInitialDelay = 5 * time.Second
	ConnectionRetryMaxDelay     = 60 * time.Second
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

// StreamTurnPolicy configures retries around a logical model turn. Callers
// rebuild the provider request from canonical turn state on every attempt, so
// provider-side history commits only successful attempts. Live callbacks from
// failed attempts may already have been emitted; callers own whether those
// visible/persisted deltas are appended, marked, or rolled back.
type StreamTurnPolicy struct {
	After                                func(time.Duration) <-chan time.Time
	OnRetry                              func(RetryEvent)
	RetryableError                       func(error) bool
	RetryConnectionFailuresWithoutBudget bool
	Recover                              func(error) (bool, error)
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

// ResponseError preserves HTTP retry metadata after provider-specific code has
// consumed and closed a response body.
type ResponseError struct {
	StatusCode int
	Header     http.Header
	Err        error
}

func (e *ResponseError) Error() string { return e.Err.Error() }
func (e *ResponseError) Unwrap() error { return e.Err }

func NewResponseError(resp *http.Response, err error) error {
	if err == nil {
		return nil
	}
	if resp == nil {
		return err
	}
	return &ResponseError{StatusCode: resp.StatusCode, Header: resp.Header.Clone(), Err: err}
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
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
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
	var responseErr *ResponseError
	if errors.As(err, &responseErr) {
		return IsRetryableStatus(responseErr.StatusCode)
	}
	msg := strings.ToLower(err.Error())
	if isNonRetryableRateLimitMessage(msg) {
		return false
	}
	if messageHasStatusCode(msg, http.StatusTooManyRequests) {
		return false
	}
	for _, hint := range []string{"overloaded", "temporar", "unavailable", "server error", "internal_error", "received from peer", "retry your request", "try again"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	for _, field := range strings.Fields(msg) {
		field = strings.Trim(field, "()[]{}:;,.\"")
		if code, parseErr := strconv.Atoi(field); parseErr == nil && IsRetryableStatus(code) {
			return true
		}
	}
	return false
}

func isNonRetryableRateLimitMessage(msg string) bool {
	for _, hint := range []string{"rate_limit", "rate limit", "too many requests", "usage limit", "exceeded your account", "insufficient_quota", "quota exceeded"} {
		if strings.Contains(msg, hint) {
			return true
		}
	}
	return false
}

func messageHasStatusCode(msg string, statusCode int) bool {
	want := strconv.Itoa(statusCode)
	for _, field := range strings.Fields(msg) {
		field = strings.Trim(field, "()[]{}:;,.\"'")
		if field == want {
			return true
		}
	}
	return false
}

// StreamTurnBackoff returns the provider stream-turn backoff for a one-based
// retry number: 200ms * 2^(retry-1), jittered by +/-10% and capped.
func StreamTurnBackoff(retry int) time.Duration {
	if retry < 1 {
		retry = 1
	}
	if retry >= 63 {
		return StreamTurnMaxBackoff
	}
	factor := time.Duration(1) << uint(retry-1)
	const maxDuration = time.Duration(1<<63 - 1)
	if StreamTurnBaseDelay > maxDuration/factor {
		return StreamTurnMaxBackoff
	}
	delay := StreamTurnBaseDelay * factor
	jitter := 0.9 + rand.Float64()*0.2
	delay = time.Duration(float64(delay) * jitter)
	if delay > StreamTurnMaxBackoff {
		return StreamTurnMaxBackoff
	}
	return delay
}

func StreamTurnRetryDelay(retry int, err error, now ...time.Time) time.Duration {
	var responseErr *ResponseError
	if errors.As(err, &responseErr) && strings.TrimSpace(responseErr.Header.Get("Retry-After")) != "" {
		current := time.Now()
		if len(now) > 0 {
			current = now[0]
		}
		delay := Backoff(retry-1, &http.Response{StatusCode: responseErr.StatusCode, Header: responseErr.Header}, StreamTurnBaseDelay, current)
		if delay > StreamTurnMaxBackoff {
			return StreamTurnMaxBackoff
		}
		return delay
	}
	return StreamTurnBackoff(retry)
}

// IsConnectionSetupFailure reports whether the request failed before a stream
// was established. Hosted providers may retry these on a separate reconnect
// lane without consuming the ordinary stream retry budget.
func IsConnectionSetupFailure(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var streamErr *StreamError
	if errors.As(err, &streamErr) {
		return false
	}
	var responseErr *ResponseError
	if errors.As(err, &responseErr) {
		return false
	}
	if !IsRetryableNetworkError(err) {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "send request") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "network is unreachable") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "tls handshake")
}

// DoStreamTurn retries a logical model turn using the shared stream retry
// policy. It is intended to wrap provider-specific request construction and
// parsing, while keeping retry count, backoff, Retry-After handling, and
// optional connection reconnect behavior consistent across providers. It does
// not buffer callbacks or enforce UI rollback; fn/callers decide live-output
// semantics.
func DoStreamTurn[T any](ctx context.Context, policy StreamTurnPolicy, fn func(context.Context) (T, error)) (T, error) {
	after := policy.After
	if after == nil {
		after = time.After
	}
	retryable := func(err error) bool {
		if IsRetryableError(err) {
			return true
		}
		return policy.RetryableError != nil && policy.RetryableError(err)
	}

	retries := 0
	connectionRetries := 0
	connectionDelay := ConnectionRetryInitialDelay
	for {
		attemptCtx := context.WithValue(ctx, retryContextKey{}, true)
		result, err := fn(attemptCtx)
		if err == nil {
			return result, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			var zero T
			return zero, ctxErr
		}
		if policy.Recover != nil {
			recovered, recoverErr := policy.Recover(err)
			if recoverErr != nil {
				return result, recoverErr
			}
			if recovered {
				continue
			}
		}
		if !retryable(err) {
			return result, err
		}

		if policy.RetryConnectionFailuresWithoutBudget && IsConnectionSetupFailure(err) {
			connectionRetries++
			delay := connectionDelay
			if connectionDelay < ConnectionRetryMaxDelay {
				connectionDelay *= 2
				if connectionDelay > ConnectionRetryMaxDelay {
					connectionDelay = ConnectionRetryMaxDelay
				}
			}
			notify(streamTurnNotifyPolicy(policy), connectionRetries, delay, 0, err)
			if waitErr := wait(ctx, after, delay); waitErr != nil {
				var zero T
				return zero, waitErr
			}
			continue
		}

		if retries >= StreamTurnMaxRetries {
			return result, err
		}
		retries++
		delay := StreamTurnRetryDelay(retries, err)
		notify(streamTurnNotifyPolicy(policy), retries, delay, 0, err)
		if waitErr := wait(ctx, after, delay); waitErr != nil {
			var zero T
			return zero, waitErr
		}
	}
}

func streamTurnNotifyPolicy(policy StreamTurnPolicy) Policy {
	return Policy{
		MaxRetries:     StreamTurnMaxRetries,
		After:          policy.After,
		OnRetry:        policy.OnRetry,
		RetryableError: policy.RetryableError,
	}
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
	if retry < 0 {
		retry = 0
	}
	const maxDuration = time.Duration(1<<63 - 1)
	if retry >= 63 {
		return maxDuration
	}
	factor := time.Duration(1) << uint(retry)
	if baseDelay > maxDuration/factor {
		return maxDuration
	}
	return baseDelay * factor
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
			if delay > policy.MaxBackoff {
				if policy.WrapNetworkError != nil {
					err = policy.WrapNetworkError(err)
				}
				return nil, fmt.Errorf("send request: %w", err)
			}
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
		delay := backoffForError(attempt, err, policy)
		if delay > policy.MaxBackoff {
			return result, err
		}
		notify(policy, attempt+1, delay, 0, err)
		if err := wait(ctx, policy.After, delay); err != nil {
			var zero T
			return zero, err
		}
	}
	var zero T
	return zero, errors.New("stream retry loop exited unexpectedly")
}

func backoffForError(attempt int, err error, policy Policy) time.Duration {
	var responseErr *ResponseError
	if errors.As(err, &responseErr) {
		return Backoff(attempt, &http.Response{StatusCode: responseErr.StatusCode, Header: responseErr.Header}, policy.BaseDelay, policy.Now())
	}
	return Backoff(attempt, nil, policy.BaseDelay, policy.Now())
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
