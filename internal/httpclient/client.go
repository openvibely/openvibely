// Package httpclient centralizes outbound HTTP connection and timeout settings.
package httpclient

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync"
	"time"
)

const (
	ConnectTimeout         = 30 * time.Second
	ModelUploadTimeout     = 10 * time.Minute
	ModelHeaderTimeout     = 5 * time.Minute
	ModelIdleTimeout       = 5 * time.Minute
	ModelRequestTimeout    = 10 * time.Minute
	UpdateTimeout          = 15 * time.Second
	GitHubTimeout          = 20 * time.Second
	OAuthTimeout           = 30 * time.Second
	XTimeout               = OAuthTimeout
	UsageTimeout           = 5 * time.Second
	MessagingTimeout       = 20 * time.Second
	MessagingPollTimeout   = 90 * time.Second
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
	return &http.Client{Transport: &modelTransport{base: base, upload: ModelUploadTimeout, header: ModelHeaderTimeout, idle: ModelIdleTimeout, total: ModelRequestTimeout}}
}

type streamingKey struct{}

// Streaming marks non-SSE model streams (for example Ollama's NDJSON).
func Streaming(ctx context.Context) context.Context {
	return context.WithValue(ctx, streamingKey{}, true)
}

type modelTransport struct {
	base                        http.RoundTripper
	upload, header, idle, total time.Duration
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
		// Redirect requests carry the preceding response. Preserve its absolute
		// deadline without inheriting the cancellation from closing its body.
		deadline := time.Now().Add(t.total)
		if previous := req.Response; previous != nil && previous.Request != nil {
			if inherited, ok := previous.Request.Context().Deadline(); ok && inherited.Before(deadline) {
				deadline = inherited
			}
		}
		ctx, cancel := context.WithDeadline(req.Context(), deadline)
		request := req.Clone(ctx)
		resp, err := t.base.RoundTrip(request)
		if err != nil {
			cancel()
			return nil, err
		}
		// Also populate Request for custom RoundTrippers, so redirects always
		// have access to the same deadline.
		resp.Request = request
		resp.Body = &deadlineBody{ReadCloser: resp.Body, ctx: ctx, close: cancel}
		return resp, nil
	}
	// Bound connection setup and upload by one total deadline. Once sending
	// finishes, give headers their full allowance, then use body inactivity.
	uploadDeadline := time.Now().Add(t.upload)
	ctx, activity, stop := WithActivityTimeout(req.Context(), t.upload)
	var phaseMu sync.Mutex
	headersReceived := false
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(httptrace.GotConnInfo) {
			phaseMu.Lock()
			defer phaseMu.Unlock()
			if !headersReceived {
				// A transport replay retains the original upload deadline.
				activity(time.Until(uploadDeadline))
			}
		},
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			if info.Err != nil {
				return
			}
			phaseMu.Lock()
			defer phaseMu.Unlock()
			if !headersReceived {
				activity(t.header)
			}
		},
	})
	resp, err := t.base.RoundTrip(req.Clone(ctx))
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		stop()
		return nil, err
	}
	phaseMu.Lock()
	headersReceived = true
	activity(t.idle)
	phaseMu.Unlock()
	resp.Body = &deadlineBody{ReadCloser: resp.Body, ctx: ctx, touch: func() { activity(t.idle) }, close: stop}
	return resp, nil
}

// WithActivityTimeout provides a resettable inactivity deadline. Activity sets
// the next deadline; stop releases the timer and cancels the derived context.
// A nonpositive initial timeout leaves it disarmed until the first activity.
// It is shared by HTTP bodies and WebSocket message reads.
func WithActivityTimeout(parent context.Context, timeout time.Duration) (context.Context, func(time.Duration), func()) {
	ctx, cancel := context.WithCancelCause(parent)
	var mu sync.Mutex
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	stopped := false
	var timer *time.Timer
	mu.Lock()
	timer = time.AfterFunc(timeout, func() {
		mu.Lock()
		defer mu.Unlock()
		if stopped || deadline.IsZero() {
			return
		}
		if remaining := time.Until(deadline); remaining > 0 {
			timer.Reset(remaining)
			return
		}
		stopped = true
		cancel(context.DeadlineExceeded)
	})
	if timeout <= 0 {
		timer.Stop()
	}
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
