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
	for _, status := range []int{408, 429, 500, 502, 503, 504, 529} {
		if !IsRetryableStatus(status) {
			t.Errorf("status %d should be retryable", status)
		}
	}
	for _, status := range []int{400, 401, 403, 404, 422} {
		if IsRetryableStatus(status) {
			t.Errorf("status %d should not be retryable", status)
		}
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
