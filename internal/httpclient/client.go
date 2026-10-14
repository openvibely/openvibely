// Package httpclient centralizes outbound HTTP connection and timeout settings.
package httpclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	ConnectTimeout         = 30 * time.Second
	ModelHeaderTimeout     = 5 * time.Minute
	ModelIdleTimeout       = 5 * time.Minute
	ModelRequestTimeout    = 10 * time.Minute
	UpdateTimeout          = 15 * time.Second
	GitHubTimeout          = 20 * time.Second
	OAuthTimeout           = 30 * time.Second
	XTimeout               = OAuthTimeout
	UsageTimeout           = 5 * time.Second
	MessagingTimeout       = 20 * time.Second
	MCPTimeout             = time.Minute
	HostedSSOTimeout       = 5 * time.Second
	HealthTimeout          = 3 * time.Second
	LocalOAuthProbeTimeout = 500 * time.Millisecond
)

// NewDialer also supports guarded DNS resolution and local Unix sockets.
func NewDialer() *net.Dialer {
	return &net.Dialer{Timeout: ConnectTimeout, KeepAlive: 30 * time.Second}
}

// Capture the standard transport once. Callers may replace DefaultTransport
// with an arbitrary RoundTripper; client construction must not depend on its type.
var transportDefaults = http.DefaultTransport.(*http.Transport).Clone()

// NewTransport keeps standard proxy/TLS behavior and configures connection pooling.
func NewTransport() *http.Transport {
	t := transportDefaults.Clone()
	t.DialContext = NewDialer().DialContext
	t.MaxIdleConns = 100
	t.MaxIdleConnsPerHost = 100
	return t
}

// NewDirectTransport retains direct routing for clients that do not use
// environment proxies, including guarded endpoints and local Unix sockets.
func NewDirectTransport() *http.Transport {
	transport := NewTransport()
	transport.Proxy = nil
	return transport
}

// New applies a total per-attempt deadline, including connection and body reads.
func New(timeout time.Duration) *http.Client {
	return &http.Client{Transport: NewTransport(), Timeout: timeout}
}

// NewModel applies per-request model deadlines without a total streaming limit.
// Pass a transport to preserve custom endpoint guards or provider behavior.
func NewModel(base http.RoundTripper) *http.Client {
	if base == nil {
		base = NewTransport()
	}
	return &http.Client{Transport: &modelTransport{base: base, header: ModelHeaderTimeout, idle: ModelIdleTimeout, total: ModelRequestTimeout}}
}

type streamingKey struct{}

// Streaming marks non-SSE model streams (for example Ollama's NDJSON).
func Streaming(ctx context.Context) context.Context {
	return context.WithValue(ctx, streamingKey{}, true)
}

type modelTransport struct {
	base                http.RoundTripper
	header, idle, total time.Duration
}

func (t *modelTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (t *modelTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// WebSocket owns its read lifecycle after the HTTP upgrade. In particular,
	// wrapping this body would hide io.Writer and break websocket.Dial.
	if strings.EqualFold(req.Header.Get("Upgrade"), "websocket") {
		return t.base.RoundTrip(req)
	}
	streaming, _ := req.Context().Value(streamingKey{}).(bool)
	streaming = streaming || strings.Contains(req.Header.Get("Accept"), "text/event-stream")
	if !streaming {
		ctx, cancel := context.WithTimeout(req.Context(), t.total)
		resp, err := t.base.RoundTrip(req.Clone(ctx))
		if err != nil {
			cancel()
			return nil, err
		}
		resp.Body = &deadlineBody{ReadCloser: resp.Body, ctx: ctx, close: cancel}
		return resp, nil
	}
	// This timer covers the request through response headers, including uploads.
	ctx, activity, stop := WithActivityTimeout(req.Context(), t.header)
	resp, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		stop()
		return nil, err
	}
	activity(t.idle)
	resp.Body = &deadlineBody{ReadCloser: resp.Body, ctx: ctx, touch: func() { activity(t.idle) }, close: stop}
	return resp, nil
}

// WithActivityTimeout provides a resettable inactivity deadline. Activity sets
// the next deadline; stop releases the timer and cancels the derived context.
// It is shared by HTTP bodies and WebSocket message reads.
func WithActivityTimeout(parent context.Context, timeout time.Duration) (context.Context, func(time.Duration), func()) {
	ctx, cancel := context.WithCancelCause(parent)
	var mu sync.Mutex
	deadline := time.Now().Add(timeout)
	stopped := false
	var timer *time.Timer
	mu.Lock()
	timer = time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if stopped {
			return
		}
		if remaining := time.Until(deadline); remaining > 0 {
			timer.Reset(remaining)
			return
		}
		stopped = true
		cancel(context.DeadlineExceeded)
	})
	mu.Unlock()
	touch := func(timeout time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		if !stopped {
			deadline = time.Now().Add(timeout)
			timer.Reset(timeout)
		}
	}
	stop := func() {
		mu.Lock()
		stopped = true
		timer.Stop()
		mu.Unlock()
		cancel(context.Canceled)
	}
	return ctx, touch, stop
}

type deadlineBody struct {
	io.ReadCloser
	ctx   context.Context
	touch func()
	close func()
}

func (b *deadlineBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 && b.touch != nil {
		b.touch()
	}
	if err != nil {
		if cause := context.Cause(b.ctx); cause != nil {
			err = cause
		}
		b.close()
	}
	return n, err
}
func (b *deadlineBody) Close() error { b.close(); return b.ReadCloser.Close() }

// ReadAllWithActivity reports each successful underlying read, including partial
// WebSocket messages, so large messages do not trip the inactivity deadline.
func ReadAllWithActivity(r io.Reader, activity func()) ([]byte, error) {
	return io.ReadAll(activityReader{r, activity})
}

type activityReader struct {
	io.Reader
	activity func()
}

func (r activityReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.activity()
	}
	return n, err
}
