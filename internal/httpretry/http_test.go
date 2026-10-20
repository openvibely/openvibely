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

func TestDoRetriesCustomRetryableResponse(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		status := http.StatusTooManyRequests
		if attempts == 2 {
			status = http.StatusOK
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("response")), Header: make(http.Header)}, nil
	})}
	policy := instantPolicy()
	policy.AllowReplay = false
	policy.RetryableResponse = func(resp *http.Response) bool { return resp.StatusCode == http.StatusTooManyRequests }
	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodGet, "https://provider.test/messages", nil)
	}, policy)
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
	if got := Backoff(2, nil, time.Second); got < 3600*time.Millisecond || got > 4400*time.Millisecond {
		t.Fatalf("exponential backoff = %v, want 4s +/-10%%", got)
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

func TestBackoffRetryAfterOverflowBoundary(t *testing.T) {
	const maxDuration = time.Duration(1<<63 - 1)

	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"2147483647"}}}
	if got := Backoff(0, resp, time.Second); got != 2147483647*time.Second {
		t.Fatalf("at math.MaxInt32 = %v, want %v", got, 2147483647*time.Second)
	}

	// 3 billion seconds exceeds math.MaxInt32 (2,147,483,647) but is fully representable in 64-bit time.Duration.
	// On 32-bit architectures, this must NOT be prematurely saturated to maxDuration.
	resp.Header.Set("Retry-After", "3000000000")
	if got := Backoff(0, resp, time.Second); got != 3000000000*time.Second {
		t.Fatalf("32-bit representable duration = %v, want %v", got, 3000000000*time.Second)
	}

	// Largest representable whole-second duration in time.Duration (math.MaxInt64 / 1e9).
	resp.Header.Set("Retry-After", "9223372036")
	if got := Backoff(0, resp, time.Second); got != 9223372036*time.Second {
		t.Fatalf("at max duration boundary = %v, want %v", got, 9223372036*time.Second)
	}

	// Overflowing numeric seconds must saturate exactly to maxDuration.
	resp.Header.Set("Retry-After", "9223372037")
	if got := Backoff(0, resp, time.Second); got != maxDuration {
		t.Fatalf("above boundary = %v, want max duration %v", got, maxDuration)
	}

	// Largest int64 seconds must saturate exactly to maxDuration.
	resp.Header.Set("Retry-After", "9223372036854775807")
	if got := Backoff(0, resp, time.Second); got != maxDuration {
		t.Fatalf("max int64 = %v, want max duration %v", got, maxDuration)
	}

	// Values exceeding int64 range must saturate exactly to maxDuration.
	resp.Header.Set("Retry-After", "99999999999999999999")
	if got := Backoff(0, resp, time.Second); got != maxDuration {
		t.Fatalf("range overflow = %v, want max duration %v", got, maxDuration)
	}
}

func TestStreamTurnRetryDelayNumericOverflow(t *testing.T) {
	resp := &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{"Retry-After": []string{"9223372037"}},
	}
	err := NewResponseError(resp, errors.New("rate limited"))
	got := StreamTurnRetryDelay(1, err)
	if got <= DefaultMaxBackoff {
		t.Fatalf("overflowing server wait = %v, must exceed retry limit", got)
	}
}

func TestDoSkipsOverflowingRetryAfter(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Retry-After": []string{"9223372037"}},
			Body:       io.NopCloser(strings.NewReader("service unavailable")),
		}, nil
	})}
	resp, err := Do(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://external.test", nil)
	}, instantPolicy())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if attempts != 1 || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("attempts/status = %d/%d, want 1/503", attempts, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "service unavailable" {
		t.Fatalf("body = %q, want %q", string(body), "service unavailable")
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

func TestDoStreamTurnCustomRetryBudget(t *testing.T) {
	attempts := 0
	wantErr := errors.New("retryable stream failure")
	_, err := DoStreamTurn(context.Background(), StreamTurnPolicy{
		MaxRetries:     2,
		RetryableError: func(error) bool { return true },
		After: func(time.Duration) <-chan time.Time {
			ready := make(chan time.Time, 1)
			ready <- time.Now()
			return ready
		},
		OnRetry: func(event RetryEvent) {
			if event.MaxRetries != 2 {
				t.Errorf("retry budget = %d, want 2", event.MaxRetries)
			}
		},
	}, func(context.Context) (string, error) {
		attempts++
		return "", wantErr
	})
	if !errors.Is(err, wantErr) || attempts != 3 {
		t.Fatalf("attempts = %d, error = %v; want initial attempt plus two retries", attempts, err)
	}
}

func TestDoStreamTurnConnectionRetriesShareBudgetAndBackoff(t *testing.T) {
	attempts := 0
	var delays []time.Duration
	_, err := DoStreamTurn(context.Background(), StreamTurnPolicy{
		After:   instantPolicy().After,
		OnRetry: func(event RetryEvent) { delays = append(delays, event.Delay) },
	}, func(context.Context) (string, error) {
		attempts++
		return "", errors.New("send request: dial tcp: no such host")
	})
	if err == nil || attempts != 7 || len(delays) != 6 {
		t.Fatalf("attempts=%d delays=%v err=%v", attempts, delays, err)
	}
	for i, seconds := range []int{1, 2, 4, 8, 16, 30} {
		want := time.Duration(seconds) * time.Second
		if delays[i] < want*9/10 || delays[i] > want*11/10 {
			t.Fatalf("retry %d delay = %v", i+1, delays[i])
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

func TestBackoffServerMinimumAndJitter(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, header := range []string{"", "invalid", "0", "-1", "1", now.Add(-time.Minute).Format(http.TimeFormat)} {
		resp := &http.Response{Header: http.Header{"Retry-After": {header}}}
		for i := 0; i < 100; i++ {
			got := Backoff(5, resp, DefaultBaseDelay, now)
			if got < 27*time.Second || got > 33*time.Second {
				t.Fatalf("header %q: %v", header, got)
			}
		}
	}
	for _, header := range []string{"120", now.Add(120 * time.Second).Format(http.TimeFormat)} {
		got := Backoff(5, &http.Response{Header: http.Header{"Retry-After": {header}}}, DefaultBaseDelay, now)
		if got != 120*time.Second {
			t.Fatalf("server minimum %q: %v", header, got)
		}
	}
}

func TestStreamTurnRejectsLongServerWait(t *testing.T) {
	for _, header := range []string{"121", "9223372037", "99999999999999999999"} {
		attempts := 0
		expected := NewResponseError(&http.Response{StatusCode: 503, Header: http.Header{"Retry-After": {header}}}, errors.New("unavailable"))
		_, err := DoStreamTurn(context.Background(), StreamTurnPolicy{After: instantPolicy().After}, func(context.Context) (string, error) {
			attempts++
			return "", expected
		})
		if err != expected || attempts != 1 {
			t.Fatalf("header %s: attempts=%d error=%v", header, attempts, err)
		}
	}
}

func TestDoServerWaitLimitPreservesResponse(t *testing.T) {
	for _, seconds := range []string{"120", "121"} {
		attempts := 0
		body := &trackingBody{reader: strings.NewReader("busy")}
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			if attempts > 1 {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("ok"))}, nil
			}
			return &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": {seconds}}, Body: body}, nil
		})}
		policy := instantPolicy()
		policy.OnRetry = func(e RetryEvent) {
			if e.Delay != 2*time.Minute {
				t.Errorf("delay=%v", e.Delay)
			}
		}
		resp, err := Do(context.Background(), client, func() (*http.Request, error) { return http.NewRequest("GET", "https://example.test", nil) }, policy)
		if err != nil {
			t.Fatal(err)
		}
		if seconds == "121" && (attempts != 1 || body.closed || body.read != 0) {
			t.Fatalf("server response consumed or retried: attempts=%d body=%+v", attempts, body)
		}
		if seconds == "120" && (attempts != 2 || !body.closed) {
			t.Fatalf("allowed wait: attempts=%d body=%+v", attempts, body)
		}
		resp.Body.Close()
	}
}

func TestNestedStreamTurnsShareRetryBudget(t *testing.T) {
	attempts := 0
	p := StreamTurnPolicy{After: instantPolicy().After}
	_, err := DoStreamTurn(context.Background(), p, func(ctx context.Context) (string, error) {
		return DoStreamTurn(ctx, p, func(context.Context) (string, error) { attempts++; return "", io.ErrUnexpectedEOF })
	})
	if err == nil || attempts != 7 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRecoveryIsBounded(t *testing.T) {
	attempts := 0
	_, err := DoStreamTurn(context.Background(), StreamTurnPolicy{Recover: func(error) (bool, error) { return true, nil }}, func(context.Context) (string, error) { attempts++; return "", io.ErrUnexpectedEOF })
	if err == nil || attempts != 7 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
}

func TestRetryTimerContext(t *testing.T) {
	const delay = 30 * time.Second
	policyCalls, overrideCalls := 0, 0
	ready := func() <-chan time.Time {
		ch := make(chan time.Time, 1)
		ch <- time.Time{}
		return ch
	}
	policyTimer := func(got time.Duration) <-chan time.Time {
		policyCalls++
		if got != delay {
			t.Errorf("delay = %v, want %v", got, delay)
		}
		return ready()
	}
	ctx := WithRetryTimer(context.Background(), func(got time.Duration) <-chan time.Time {
		overrideCalls++
		if got != delay {
			t.Errorf("override delay = %v, want %v", got, delay)
		}
		return ready()
	})
	if err := wait(ctx, policyTimer, delay); err != nil {
		t.Fatal(err)
	}
	if policyCalls != 0 || overrideCalls != 1 {
		t.Fatalf("policy calls=%d override calls=%d", policyCalls, overrideCalls)
	}
	// An unrelated request must still use the policy's timer.
	if err := wait(context.Background(), policyTimer, delay); err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := wait(cancelled, policyTimer, delay); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled wait = %v", err)
	}
	if policyCalls != 1 || overrideCalls != 1 {
		t.Fatalf("policy calls=%d override calls=%d", policyCalls, overrideCalls)
	}
}
