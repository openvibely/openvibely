package httpretry

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type requestRetryDoer func(*http.Request) (*http.Response, error)

func (f requestRetryDoer) Do(r *http.Request) (*http.Response, error) { return f(r) }

func TestRequestRetryReplaysBodyAndHeaders(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			req, _ := http.NewRequest(method, "https://fixture.invalid", strings.NewReader("original-body"))
			req.Header.Set("Authorization", "Bearer fixture")
			calls := 0
			client := requestRetryDoer(func(r *http.Request) (*http.Response, error) {
				calls++
				body, _ := io.ReadAll(r.Body)
				r.Body.Close()
				if string(body) != "original-body" || r.Header.Get("Authorization") != "Bearer fixture" {
					t.Fatalf("attempt %d lost request", calls)
				}
				// Mutations of a dispatched request must not contaminate the next attempt.
				r.Header.Del("Authorization")
				status := http.StatusServiceUnavailable
				if calls == 3 {
					status = http.StatusOK
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("response"))}, nil
			})
			p := DefaultPolicy()
			p.AllowReplay = true
			p.After = func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); c <- time.Now(); return c }
			resp, err := doRequest(client, req, p)
			if err != nil || resp.StatusCode != 200 || calls != 3 {
				t.Fatalf("calls=%d resp=%v err=%v", calls, resp, err)
			}
			resp.Body.Close()
		})
	}
}

func TestRequestRetryDoesNotBufferStreamingBody(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid", io.NopCloser(strings.NewReader("stream")))
	calls := 0
	resp, err := DoRequest(requestRetryDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		r.Body.Close()
		return &http.Response{StatusCode: 503, Body: http.NoBody}, nil
	}), req)
	if err != nil || calls != 1 || resp.StatusCode != 503 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestRequestRetryCancellationAndPermanentFailure(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, http.MethodPost, "https://fixture.invalid", strings.NewReader("code"))
			calls := 0
			resp, err := DoRequest(requestRetryDoer(func(*http.Request) (*http.Response, error) {
				calls++
				if status == 503 {
					cancel()
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: http.NoBody}, nil
			}), req)
			if calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if status == 503 {
				if err != context.Canceled {
					t.Fatalf("err=%v", err)
				}
			} else if err != nil || resp.StatusCode != status {
				t.Fatalf("resp=%v err=%v", resp, err)
			}
		})
	}
}

func TestRequestRetryDoesNotRepeatRedirectPolicyFailure(t *testing.T) {
	calls := 0
	req, _ := http.NewRequest(http.MethodGet, "https://fixture.invalid", nil)
	resp, err := DoRequest(requestRetryDoer(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, &url.Error{Op: "Get", URL: "https://fixture.invalid", Err: errors.New("redirect must use HTTPS")}
	}), req)
	if err == nil || resp != nil || calls != 1 {
		t.Fatalf("resp=%v calls=%d err=%v", resp, calls, err)
	}
}

func TestRequestRetryUsesGeneralAttemptBudget(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://fixture.invalid", strings.NewReader("body"))
	calls := 0
	policy := DefaultPolicy()
	policy.AllowReplay = true
	policy.After = func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); c <- time.Now(); return c }
	resp, err := doRequest(requestRetryDoer(func(r *http.Request) (*http.Response, error) {
		calls++
		r.Body.Close()
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: http.NoBody}, nil
	}), req, policy)
	if err != nil || calls != DefaultMaxRetries+1 || resp.StatusCode != 503 {
		t.Fatalf("calls=%d resp=%v err=%v", calls, resp, err)
	}
}
