package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/synctest"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type slowBody struct {
	ctx       context.Context
	interval  time.Duration
	remaining int
}

func (b *slowBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	select {
	case <-b.ctx.Done():
		return 0, b.ctx.Err()
	case <-time.After(b.interval):
	}
	p[0] = 'x'
	b.remaining--
	return 1, nil
}
func (b *slowBody) Close() error { return nil }

func TestModelTimeouts(t *testing.T) {
	for _, tc := range []struct {
		name              string
		streaming         bool
		headers, interval time.Duration
		count             int
		wantTimeout       bool
	}{
		{"stalled headers", true, 6 * time.Minute, 0, 0, true},
		{"stalled stream", true, 0, 6 * time.Minute, 1, true},
		{"healthy stream exceeds old total", true, 4 * time.Minute, 4 * time.Minute, 4, false},
		{"nonstreaming total includes body", false, 4 * time.Minute, 4 * time.Minute, 4, true},
		{"normal response", false, time.Second, time.Second, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := NewModel(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					select {
					case <-r.Context().Done():
						return nil, r.Context().Err()
					case <-time.After(tc.headers):
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &slowBody{r.Context(), tc.interval, tc.count}}, nil
				}))
				ctx := context.Background()
				if tc.streaming {
					ctx = Streaming(ctx)
				}
				req, _ := http.NewRequestWithContext(ctx, "GET", "https://model.test", nil)
				resp, err := client.Do(req)
				if err == nil {
					_, err = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				if tc.wantTimeout && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("want timeout, got %v", err)
				}
				if !tc.wantTimeout && err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestModelCancellationAndCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		var bodyCtx context.Context
		client := NewModel(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			bodyCtx = r.Context()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &slowBody{bodyCtx, time.Hour, 1}}, nil
		}))
		req, _ := http.NewRequestWithContext(Streaming(ctx), "GET", "https://model.test", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		go func() { time.Sleep(time.Second); cancel() }()
		_, err = io.ReadAll(resp.Body)
		resp.Body.Close()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	})
}

func TestNonModelTotalDeadlineIncludesConnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := New(UsageTimeout)
		client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })
		start := time.Now()
		_, err := client.Get("https://example.test")
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != UsageTimeout {
			t.Fatalf("elapsed=%v err=%v", time.Since(start), err)
		}
	})
}

func TestRealHTTPIdleTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	transport := NewTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: &modelTransport{base: transport, header: time.Second, idle: 30 * time.Millisecond, total: time.Second}}
	req, _ := http.NewRequest("GET", server.URL, nil)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	_, err = io.ReadAll(resp.Body)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

func TestActivityTimeoutResetsOnPartialReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, touch, stop := WithActivityTimeout(context.Background(), ModelIdleTimeout)
		defer stop()
		body := &slowBody{ctx, 4 * time.Minute, 4}
		data, err := ReadAllWithActivity(body, func() { touch(ModelIdleTimeout) })
		if err != nil || len(data) != 4 {
			t.Fatalf("bytes=%d err=%v", len(data), err)
		}
	})
}

func TestClosingStreamReleasesDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var bodyCtx context.Context
		client := NewModel(roundTripFunc(func(req *http.Request) (*http.Response, error) {
			bodyCtx = req.Context()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &slowBody{bodyCtx, time.Hour, 1}}, nil
		}))
		req, _ := http.NewRequestWithContext(Streaming(context.Background()), "GET", "https://model.test", nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if context.Cause(bodyCtx) != context.Canceled {
			t.Fatalf("body context not released: %v", context.Cause(bodyCtx))
		}
		// A stopped watchdog must not replace cancellation with a late timeout.
		time.Sleep(2 * ModelIdleTimeout)
		if context.Cause(bodyCtx) != context.Canceled {
			t.Fatal(context.Cause(bodyCtx))
		}
	})
}

func TestNewClientAfterDefaultTransportOverride(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") }))
	defer server.Close()
	original := http.DefaultTransport
	http.DefaultTransport = roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("global override") })
	defer func() { http.DefaultTransport = original }()
	client := New(time.Second)
	defer client.CloseIdleConnections()
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil || string(data) != "ok" {
		t.Fatalf("response=%q err=%v", data, err)
	}
}
