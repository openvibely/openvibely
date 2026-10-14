package httpclient

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
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
					if trace := httptrace.ContextClientTrace(r.Context()); trace != nil && trace.WroteRequest != nil {
						trace.WroteRequest(httptrace.WroteRequestInfo{})
					}
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
	client := &http.Client{Transport: &modelTransport{upload: ModelUploadTimeout, base: transport, header: time.Second, idle: 30 * time.Millisecond, total: time.Second}}
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

func TestNonStreamingDeadlineSurvivesRedirects(t *testing.T) {
	for _, tc := range []struct {
		name         string
		header, body time.Duration
		wantTimeout  bool
	}{
		{"headers exceed total", 6 * time.Minute, 0, true},
		{"body exceeds total", 4 * time.Minute, 3 * time.Minute, true},
		{"redirect succeeds within total", 3 * time.Minute, time.Minute, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				calls := 0
				start := time.Now()
				client := NewModel(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					calls++
					if r.Method != "POST" {
						t.Errorf("redirect changed method: %s", r.Method)
					}
					select {
					case <-time.After(tc.header):
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
					resp := &http.Response{StatusCode: 200, Header: make(http.Header), Body: &slowBody{r.Context(), tc.body, 1}}
					if calls == 1 {
						resp.StatusCode = 307
						resp.Header.Set("Location", "/second")
						resp.Body = io.NopCloser(strings.NewReader("redirect"))
					}
					return resp, nil
				}))
				req, _ := http.NewRequest("POST", "https://model.test/first", strings.NewReader("{}"))
				resp, err := client.Do(req)
				if err == nil {
					_, err = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				if calls != 2 {
					t.Fatalf("calls=%d", calls)
				}
				if tc.wantTimeout {
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != ModelRequestTimeout {
						t.Fatalf("elapsed=%v err=%v", time.Since(start), err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestHeaderWaitStartsAfterRequestWrite(t *testing.T) {
	for _, tc := range []struct {
		name        string
		header      time.Duration
		wantTimeout bool
		replay      bool
	}{
		{"full header allowance", 4 * time.Minute, false, false},
		{"replayed upload", 4 * time.Minute, false, true},
		{"stalled headers", 6 * time.Minute, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				client := NewModel(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					if tc.replay {
						r.Body.Close()
						body, err := r.GetBody()
						if err != nil {
							return nil, err
						}
						r.Body = body
					}
					defer r.Body.Close()
					// An active six-minute upload must not consume the header allowance.
					for range 3 {
						select {
						case <-time.After(2 * time.Minute):
						case <-r.Context().Done():
							return nil, r.Context().Err()
						}
						if _, err := io.ReadFull(r.Body, make([]byte, 1)); err != nil {
							return nil, err
						}
					}
					httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
					select {
					case <-time.After(tc.header):
					case <-r.Context().Done():
						return nil, r.Context().Err()
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &slowBody{r.Context(), 4 * time.Minute, 3}}, nil
				}))
				req, _ := http.NewRequestWithContext(Streaming(context.Background()), "POST", "https://model.test", strings.NewReader("{} "))
				resp, err := client.Do(req)
				if err == nil {
					_, err = io.ReadAll(resp.Body)
					resp.Body.Close()
				}
				if tc.wantTimeout {
					if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 6*time.Minute+ModelHeaderTimeout {
						t.Fatalf("elapsed=%v err=%v", time.Since(start), err)
					}
				} else if err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestRealHTTPHeaderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	transport := NewTransport()
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: &modelTransport{upload: ModelUploadTimeout, base: transport, header: 30 * time.Millisecond, idle: time.Second, total: time.Second}}
	req, _ := http.NewRequestWithContext(Streaming(context.Background()), "GET", server.URL, nil)
	_, err := client.Do(req)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
}

// Exercise a real HTTP transport blocked writing to a peer that stops reading.
func TestStalledUploadTimeout(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()
	headersRead := make(chan struct{})
	go func() {
		reader := bufio.NewReader(serverConn)
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				return
			}
			if line == "\r\n" {
				close(headersRead)
				return
			}
		}
	}()
	base := NewDirectTransport()
	base.DialContext = func(context.Context, string, string) (net.Conn, error) { return clientConn, nil }
	defer base.CloseIdleConnections()
	client := &http.Client{Transport: &modelTransport{upload: 50 * time.Millisecond, base: base, header: time.Second, idle: time.Second, total: time.Second}}
	ctx, cancel := context.WithCancel(Streaming(context.Background()))
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "POST", "http://model.test", bytes.NewReader(make([]byte, 1024*1024)))
	done := make(chan error, 1)
	go func() {
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- err
	}()
	select {
	case <-headersRead:
	case <-time.After(2 * time.Second):
		t.Fatal("request headers were not sent")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stalled upload did not time out")
	}
}

func TestEarlyResponseIgnoresLateUploadActivity(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		base := roundTripFunc(func(r *http.Request) (*http.Response, error) {
			go func() {
				time.Sleep(time.Minute)
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
			}()
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &slowBody{r.Context(), 3 * time.Minute, 1}}, nil
		})
		client := &http.Client{Transport: &modelTransport{upload: ModelUploadTimeout, base: base, header: 5 * time.Minute, idle: 2 * time.Minute, total: 10 * time.Minute}}
		req, _ := http.NewRequestWithContext(Streaming(context.Background()), "POST", "https://model.test", strings.NewReader("{}"))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, err = io.ReadAll(resp.Body)
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != 2*time.Minute {
			t.Fatalf("late upload changed response deadline: elapsed=%v err=%v", time.Since(start), err)
		}
	})
}

func TestUploadTotalDeadline(t *testing.T) {
	for _, replay := range []bool{false, true} {
		t.Run(fmt.Sprintf("replay=%v", replay), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				client := NewModel(roundTripFunc(func(r *http.Request) (*http.Response, error) {
					trace := httptrace.ContextClientTrace(r.Context())
					for i := 0; ; i++ {
						select {
						case <-r.Context().Done():
							return nil, r.Context().Err()
						case <-time.After(time.Minute):
						}
						if _, err := io.ReadFull(r.Body, make([]byte, 1)); err != nil {
							return nil, err
						}
						if replay && i == 5 {
							trace.WroteRequest(httptrace.WroteRequestInfo{})
							r.Body.Close()
							body, err := r.GetBody()
							if err != nil {
								return nil, err
							}
							r.Body = body
							trace.GotConn(httptrace.GotConnInfo{})
						}
					}
				}))
				req, _ := http.NewRequestWithContext(Streaming(context.Background()), "POST", "https://model.test", strings.NewReader(strings.Repeat("x", 20)))
				_, err := client.Do(req)
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) != ModelUploadTimeout {
					t.Fatalf("elapsed=%v err=%v", time.Since(start), err)
				}
			})
		})
	}
}

func TestUploadDoesNotRequireBodyReadProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := NewModel(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			// The transport may spend longer than the idle limit sending one buffered chunk.
			io.ReadAll(r.Body)
			select {
			case <-time.After(6 * time.Minute):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			httptrace.ContextClientTrace(r.Context()).WroteRequest(httptrace.WroteRequestInfo{})
			select {
			case <-time.After(4 * time.Minute):
			case <-r.Context().Done():
				return nil, r.Context().Err()
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: &slowBody{r.Context(), 4 * time.Minute, 3}}, nil
		}))
		req, _ := http.NewRequestWithContext(Streaming(context.Background()), "POST", "https://model.test", strings.NewReader("{}"))
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if _, err = io.ReadAll(resp.Body); err != nil {
			t.Fatal(err)
		}
	})
}
