package httpretry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type trackingBody struct {
	reader io.Reader
	read   int
	closed bool
}

func (b *trackingBody) Read(p []byte) (int, error) {
	n, err := b.reader.Read(p)
	b.read += n
	return n, err
}
func (b *trackingBody) Close() error { b.closed = true; return nil }

func instantPolicy() Policy {
	policy := DefaultPolicy()
	policy.AllowReplay = true
	policy.After = func(time.Duration) <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	return policy
}

func TestDoDoesNotReplayUnsafeRequestWithoutOptIn(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("read: operation timed out")
	})}
	policy := instantPolicy()
	policy.AllowReplay = false
	_, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://external.test", nil)
	}, policy)
	if err == nil || attempts != 1 {
		t.Fatalf("error/attempts = %v/%d, want error/1", err, attempts)
	}
}

func TestDoRetriesNetworkTimeout(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("read: operation timed out")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}

	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://provider.test/messages", nil)
	}, instantPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2", attempts)
	}
}

func TestDoRetriesTransientStatus(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		status := http.StatusServiceUnavailable
		if attempts == 2 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("response")), Header: make(http.Header)}, nil
	})}

	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://provider.test/messages", nil)
	}, instantPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts != 2 || resp.StatusCode != http.StatusOK {
		t.Fatalf("attempts/status = %d/%d, want 2/200", attempts, resp.StatusCode)
	}
}

func TestDoDrainsAndClosesResponseBeforeRetry(t *testing.T) {
	attempts := 0
	firstBody := &trackingBody{reader: strings.NewReader("retry response")}
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: firstBody, Header: make(http.Header)}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("ok")), Header: make(http.Header)}, nil
	})}
	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://external.test", nil)
	}, instantPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if !firstBody.closed || firstBody.read != len("retry response") {
		t.Fatalf("retry body closed/read = %v/%d, want true/%d", firstBody.closed, firstBody.read, len("retry response"))
	}
}

func TestRetryableStatuses(t *testing.T) {
	for _, status := range []int{408, 500, 502, 503, 504, 529} {
		if !IsRetryableStatus(status) {
			t.Errorf("status %d should be retryable", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 422, 429} {
		if IsRetryableStatus(status) {
			t.Errorf("status %d should not be retryable", status)
		}
	}
}

func TestRetryableErrorRequiresExactStatusToken(t *testing.T) {
	if IsRetryableError(errors.New("invalid request: maximum is 500-token units")) {
		t.Fatal("embedded number must not make a permanent error retryable")
	}
	if !IsRetryableError(errors.New("API error (503): unavailable")) {
		t.Fatal("exact transient status token should be retryable")
	}
	if IsRetryableError(errors.New("API error 429: rate_limit_error: exceeded usage limit")) {
		t.Fatal("rate-limit usage errors should not be retryable")
	}
	if IsRetryableError(errors.New(`API error 429: {"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."}}`)) {
		t.Fatal("rate-limit errors should not become retryable through try-again wording")
	}
	if IsRetryableError(errors.New("API error 429: Please try again later.")) {
		t.Fatal("plain 429 errors should not become retryable through try-again wording")
	}
}

func TestPlainEOFIsRetryable(t *testing.T) {
	if !IsRetryableNetworkError(io.EOF) {
		t.Fatal("plain EOF before a response should be retryable")
	}
}

func TestPeerInternalStreamErrorIsRetryable(t *testing.T) {
	err := NewStreamError(errors.New("stream error: stream ID 1241; INTERNAL_ERROR; received from peer"))
	if !IsRetryableError(err) {
		t.Fatal("HTTP/2 peer INTERNAL_ERROR stream failure should be retryable")
	}
}

func TestBackoffHonorsRetryAfterAndExponentialDelay(t *testing.T) {
	if got := Backoff(2, nil, time.Second); got != 4*time.Second {
		t.Fatalf("exponential backoff = %v, want 4s", got)
	}
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"7"}}}
	if got := Backoff(0, resp, time.Second); got != 7*time.Second {
		t.Fatalf("Retry-After backoff = %v, want 7s", got)
	}
	now := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	resp.Header.Set("Retry-After", now.Add(9*time.Second).Format(http.TimeFormat))
	if got := Backoff(0, resp, time.Second, now); got != 9*time.Second {
		t.Fatalf("HTTP-date Retry-After backoff = %v, want 9s", got)
	}
}

func TestDoReturnsFinalResponseAfterExhaustingRetries(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader("unavailable")), Header: make(http.Header)}, nil
	})}
	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://external.test", nil)
	}, instantPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts != DefaultMaxRetries+1 || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts/status = %d/%d, want %d/503", attempts, resp.StatusCode, DefaultMaxRetries+1)
	}
}

func TestDoSkipsExcessiveRetryAfter(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"3600"}},
			Body:       io.NopCloser(strings.NewReader("rate limited")),
		}, nil
	})}
	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://external.test", nil)
	}, instantPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts != 1 || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("attempts/status = %d/%d, want 1/429", attempts, resp.StatusCode)
	}
}

func TestDoStreamRetriesOnlyBeforeOutput(t *testing.T) {
	t.Run("before output", func(t *testing.T) {
		attempts := 0
		result, err := DoStream(context.Background(), instantPolicy(), func(context.Context) (string, bool, error) {
			attempts++
			if attempts == 1 {
				return "", false, NewStreamError(errors.New("read: operation timed out"))
			}
			return "ok", true, nil
		})
		if err != nil || result != "ok" || attempts != 2 {
			t.Fatalf("result/error/attempts = %q/%v/%d, want ok/nil/2", result, err, attempts)
		}
	})

	t.Run("after output", func(t *testing.T) {
		attempts := 0
		_, err := DoStream(context.Background(), instantPolicy(), func(context.Context) (string, bool, error) {
			attempts++
			return "partial", true, NewStreamError(errors.New("read: operation timed out"))
		})
		if err == nil {
			t.Fatal("expected stream error")
		}
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1 to avoid replaying partial output", attempts)
		}
	})
}

func TestDoStreamOwnsNestedHTTPRetryBudget(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return nil, errors.New("read: operation timed out")
	})}
	policy := instantPolicy()
	_, err := DoStream(context.Background(), policy, func(attemptCtx context.Context) (string, bool, error) {
		_, err := Do(attemptCtx, client, func() (*http.Request, error) {
			return http.NewRequestWithContext(attemptCtx, http.MethodPost, "https://external.test", nil)
		}, policy)
		return "", false, err
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if attempts != DefaultMaxRetries+1 {
		t.Fatalf("attempts = %d, want bounded total of %d", attempts, DefaultMaxRetries+1)
	}
}

func TestDoStreamRetriesProviderOverloadBeforeOutput(t *testing.T) {
	attempts := 0
	result, err := DoStream(context.Background(), instantPolicy(), func(context.Context) (string, bool, error) {
		attempts++
		if attempts == 1 {
			return "", false, errors.New("anthropic event error: overloaded_error")
		}
		return "ok", true, nil
	})
	if err != nil || result != "ok" || attempts != 2 {
		t.Fatalf("result/error/attempts = %q/%v/%d, want ok/nil/2", result, err, attempts)
	}
}

func TestDoStreamDoesNotRetryRateLimitResponse(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	policy := instantPolicy()
	policy.OnRetry = func(event RetryEvent) { delays = append(delays, event.Delay) }
	_, err := DoStream(context.Background(), policy, func(context.Context) (string, bool, error) {
		attempts++
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"7"}}}
		return "", false, NewResponseError(resp, errors.New("rate limited"))
	})
	if err == nil || attempts != 1 || len(delays) != 0 {
		t.Fatalf("error/attempts/delays = %v/%d/%v, want error/1/[]", err, attempts, delays)
	}
}

func TestDoStreamTurnConnectionRetryCanBypassStreamBudget(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	var retryAttempts []int
	policy := StreamTurnPolicy{
		RetryConnectionFailuresWithoutBudget: true,
		After: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
		OnRetry: func(event RetryEvent) {
			delays = append(delays, event.Delay)
			retryAttempts = append(retryAttempts, event.Attempt)
		},
	}
	result, err := DoStreamTurn(context.Background(), policy, func(context.Context) (string, error) {
		attempts++
		if attempts <= StreamTurnMaxRetries+2 {
			return "", errors.New(`send request: Post "https://provider.test": dial tcp: no such host`)
		}
		return "ok", nil
	})
	if err != nil || result != "ok" {
		t.Fatalf("result/error = %q/%v, want ok/nil", result, err)
	}
	if attempts != StreamTurnMaxRetries+3 {
		t.Fatalf("attempts = %d, want %d", attempts, StreamTurnMaxRetries+3)
	}
	if len(delays) != StreamTurnMaxRetries+2 {
		t.Fatalf("delays = %v, want %d entries", delays, StreamTurnMaxRetries+2)
	}
	for i, want := range []time.Duration{5 * time.Second, 10 * time.Second, 20 * time.Second, 40 * time.Second, 60 * time.Second, 60 * time.Second, 60 * time.Second} {
		if delays[i] != want {
			t.Fatalf("delay[%d] = %v, want %v; all delays=%v", i, delays[i], want, delays)
		}
	}
	for i, got := range retryAttempts {
		if want := i + 1; got != want {
			t.Fatalf("retry attempt[%d] = %d, want %d; all attempts=%v", i, got, want, retryAttempts)
		}
	}
}

func TestDoStreamTurnConnectionRetryBoundedWhenNotOptedIn(t *testing.T) {
	attempts := 0
	policy := StreamTurnPolicy{
		After: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	}
	_, err := DoStreamTurn(context.Background(), policy, func(context.Context) (string, error) {
		attempts++
		return "", errors.New(`send request: Post "http://localhost:11434": dial tcp: connection refused`)
	})
	if err == nil {
		t.Fatal("expected connection error after bounded retries")
	}
	if attempts != StreamTurnMaxRetries+1 {
		t.Fatalf("attempts = %d, want %d", attempts, StreamTurnMaxRetries+1)
	}
}

func TestDoStreamTurnDoesNotRetry429(t *testing.T) {
	attempts := 0
	policy := StreamTurnPolicy{
		After: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	}
	_, err := DoStreamTurn(context.Background(), policy, func(context.Context) (string, error) {
		attempts++
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"1"}}}
		return "", NewResponseError(resp, errors.New("rate limited"))
	})
	if err == nil || attempts != 1 {
		t.Fatalf("error/attempts = %v/%d, want error/1", err, attempts)
	}
}

func TestMaxBackoffAppliesToNetworkAndStreamRetries(t *testing.T) {
	t.Run("network", func(t *testing.T) {
		attempts := 0
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			return nil, errors.New("read: operation timed out")
		})}
		policy := instantPolicy()
		policy.BaseDelay = 2 * time.Second
		policy.MaxBackoff = time.Second
		_, _ = Do(context.Background(), client, func() (*http.Request, error) {
			return http.NewRequest(http.MethodGet, "https://external.test", nil)
		}, policy)
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1 when delay exceeds maximum", attempts)
		}
	})

	t.Run("stream", func(t *testing.T) {
		attempts := 0
		policy := instantPolicy()
		policy.BaseDelay = 2 * time.Second
		policy.MaxBackoff = time.Second
		_, _ = DoStream(context.Background(), policy, func(context.Context) (string, bool, error) {
			attempts++
			return "", false, NewStreamError(errors.New("read: operation timed out"))
		})
		if attempts != 1 {
			t.Fatalf("attempts = %d, want 1 when delay exceeds maximum", attempts)
		}
	})
}
