package openaiclient

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		code int
		want bool
	}{
		{200, false},
		{201, false},
		{400, false},
		{401, false},
		{403, false},
		{404, false},
		{429, true},
		{500, true},
		{502, true},
		{503, true},
		{529, true},
	}

	for _, tt := range tests {
		got := isRetryable(tt.code)
		if got != tt.want {
			t.Errorf("isRetryable(%d) = %v, want %v", tt.code, got, tt.want)
		}
	}
}

func TestRetryBackoff(t *testing.T) {
	t.Run("exponential", func(t *testing.T) {
		if got := retryBackoff(0, nil); got != 1*time.Second {
			t.Errorf("attempt 0: %v, want 1s", got)
		}
		if got := retryBackoff(1, nil); got != 2*time.Second {
			t.Errorf("attempt 1: %v, want 2s", got)
		}
		if got := retryBackoff(2, nil); got != 4*time.Second {
			t.Errorf("attempt 2: %v, want 4s", got)
		}
	})

	t.Run("retry-after header", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 429,
			Header:     http.Header{"Retry-After": []string{"5"}},
		}
		if got := retryBackoff(0, resp); got != 5*time.Second {
			t.Errorf("got %v, want 5s", got)
		}
	})

	t.Run("retry-after non-429", func(t *testing.T) {
		resp := &http.Response{
			StatusCode: 500,
			Header:     http.Header{"Retry-After": []string{"5"}},
		}
		// Should use exponential backoff for non-429
		if got := retryBackoff(0, resp); got != 1*time.Second {
			t.Errorf("got %v, want 1s", got)
		}
	})
}

func TestDoWithRetry_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), http.DefaultClient, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestDoWithRetry_RetryOn429(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte("rate limited"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), http.DefaultClient, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("attempts = %d, want 3", atomic.LoadInt32(&attempts))
	}
}

func TestDoWithRetry_RetryOn500(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n <= 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte("error"))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), http.DefaultClient, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("attempts = %d, want 2", atomic.LoadInt32(&attempts))
	}
}

func TestDoWithRetry_NoRetryOn400(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte("bad request"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), http.DefaultClient, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("attempts = %d, want 1 (no retry for 400)", atomic.LoadInt32(&attempts))
	}
}

func TestDoWithRetry_ExhaustedRetries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("unavailable"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), http.DefaultClient, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// After maxRetries+1 attempts, returns the last response
	if resp.StatusCode != 503 {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != int32(maxRetries)+1 {
		t.Errorf("attempts = %d, want %d", atomic.LoadInt32(&attempts), maxRetries+1)
	}
}

func TestDoWithRetry_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte("rate limited"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the retry sleep is interrupted
	cancel()

	buildReq := func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "POST", srv.URL, nil)
	}

	_, err := doWithRetry(ctx, http.DefaultClient, buildReq)
	if err == nil {
		t.Error("expected error when context is cancelled")
	}
}

func TestDoWithRetry_ReturnsNon2xxNonRetryable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("unauthorized"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), http.DefaultClient, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
}

func TestDoWithRetry_NetworkError(t *testing.T) {
	var attempts int32

	// Create a custom transport that simulates network errors
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			n := atomic.AddInt32(&attempts, 1)
			if n <= 2 {
				// Simulate connection refused for first 2 attempts
				return nil, fmt.Errorf("dial tcp %s: connection refused", addr)
			}
			// Success on third attempt
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}

	client := &http.Client{Transport: transport}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), client, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("attempts = %d, want 3", atomic.LoadInt32(&attempts))
	}
}

func TestDoWithRetry_NetworkTimeout(t *testing.T) {
	var attempts int32

	// Create a custom transport that simulates timeouts
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			n := atomic.AddInt32(&attempts, 1)
			if n <= 1 {
				// Simulate timeout on first attempt
				return nil, &net.OpError{
					Op:  "dial",
					Net: network,
					Err: &timeoutError{},
				}
			}
			// Success on second attempt
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
	}

	client := &http.Client{Transport: transport}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), client, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("attempts = %d, want 2", atomic.LoadInt32(&attempts))
	}
}

func TestDoWithRetry_APIErrorParsing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error": {"type": "invalid_request", "message": "bad input", "code": "invalid_request"}}`))
	}))
	defer srv.Close()

	buildReq := func() (*http.Request, error) {
		return http.NewRequest("POST", srv.URL, nil)
	}

	resp, err := doWithRetry(context.Background(), http.DefaultClient, buildReq)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// timeoutError implements net.Error for testing
type timeoutError struct{}

func (e *timeoutError) Error() string   { return "i/o timeout" }
func (e *timeoutError) Timeout() bool   { return true }
func (e *timeoutError) Temporary() bool { return true }
