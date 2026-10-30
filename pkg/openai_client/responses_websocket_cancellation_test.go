package openaiclient

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/httpclient"
)

// An incomplete frame exercises a blocked payload read, not just Reader waiting
// for a header. The response-wide interrupt must wake both without per-read
// context callbacks, preserve the cancellation cause, and discard the socket.
func TestResponsesWebsocketCancellationInterruptsHeaderAndPayload(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial-payload=%v", partial), func(t *testing.T) {
			ready, closed := make(chan struct{}), make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				defer close(closed)
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				key := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
				_, err = fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(key[:]))
				if err == nil && partial {
					// A 2048-byte text frame with only one payload byte ever delivered.
					_, err = rw.Write([]byte{0x81, 126, 0x08, 0x00, '{'})
				}
				if err == nil {
					err = rw.Flush()
				}
				if err != nil {
					t.Error(err)
					return
				}
				close(ready)
				_, _ = io.Copy(io.Discard, rw)
			}))
			defer srv.Close()
			original := OpenAIAPIBaseURL
			OpenAIAPIBaseURL = srv.URL
			defer func() { OpenAIAPIBaseURL = original }()
			client := NewWithAPIKey("test-key")
			ctx, cancel := context.WithCancelCause(context.Background())
			defer cancel(context.Canceled)
			body, err := client.openResponsesWebsocketStream(ctx, map[string]any{"model": "gpt-5.3-codex", "input": []any{}}, false, responsesWebsocketStreamOptions{})
			if err != nil {
				t.Fatal(err)
			}
			defer body.Close()
			result := make(chan error, 1)
			go func() { _, err := io.ReadAll(body); result <- err }()
			<-ready
			cause := errors.New("cancel active response")
			cancel(cause)
			select {
			case err := <-result:
				if !errors.Is(err, cause) {
					t.Fatalf("read error=%v, want cause %v", err, cause)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("cancellation did not interrupt socket read")
			}
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("cancelled socket remained open")
			}
			state := client.responsesTransportState
			state.mu.Lock()
			retained := state.conn != nil || state.lastResponseID != ""
			state.mu.Unlock()
			if retained {
				t.Fatal("cancelled connection remained reusable")
			}
		})
	}
}

func TestResponsesWebsocketWatchdogInterruptsSocket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer c.CloseNow()
		_, _, _ = c.Read(r.Context()) // Stay silent until the client closes.
	}))
	defer srv.Close()
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn, _, err := websocket.Dial(parent, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	ctx, activity, stop := httpclient.WithActivityTimeout(parent, 0)
	defer stop()
	finish := interruptResponsesWebsocketRead(ctx, conn)
	activity(20 * time.Millisecond)
	result := make(chan error, 1)
	go func() { _, _, err := conn.Read(context.WithoutCancel(ctx)); result <- err }()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("idle read unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not close blocked read")
	}
	if !finish() {
		t.Fatal("expired watchdog did not run the interrupt")
	}
	if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) || parent.Err() != nil {
		t.Fatalf("idle cause=%v parent=%v", context.Cause(ctx), parent.Err())
	}
}

// Old request cancellation must not close the same physical socket while a new
// request is blocked reading it. EOF establishes that previous cleanup ran.
func TestResponsesWebsocketOldCancellationCannotInterruptReuse(t *testing.T) {
	var connections atomic.Int32
	secondReady, releaseSecond := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		for i := 0; i < 2; i++ {
			if _, _, err = conn.Read(r.Context()); err != nil {
				t.Error(err)
				return
			}
			if i == 1 {
				close(secondReady)
				<-releaseSecond
			}
			event := fmt.Sprintf(`{"type":"response.completed","response":{"id":"response_%d","status":"completed","output":[]}}`, i)
			if err = conn.Write(r.Context(), websocket.MessageText, []byte(event)); err != nil {
				t.Error(err)
				return
			}
		}
	}))
	defer srv.Close()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseSecond) }) }
	defer release()
	original := OpenAIAPIBaseURL
	OpenAIAPIBaseURL = srv.URL
	defer func() { OpenAIAPIBaseURL = original }()
	client := NewWithAPIKey("test-key")
	defer client.responsesTransportState.Close()
	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	second, cancelSecond := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelSecond()
	payload := map[string]any{"model": "gpt-5.3-codex", "input": []any{}}
	body, err := client.openResponsesWebsocketStream(first, payload, false, responsesWebsocketStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatal(err)
	}
	body, err = client.openResponsesWebsocketStream(second, payload, false, responsesWebsocketStreamOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer body.Close()
	select {
	case <-secondReady:
	case <-time.After(2 * time.Second):
		t.Fatal("second request not received")
	}
	cancelFirst()
	release()
	data, err := io.ReadAll(body)
	if err != nil || !strings.Contains(string(data), "response_1") {
		t.Fatalf("reuse read=%s err=%v", data, err)
	}
	if connections.Load() != 1 {
		t.Fatalf("connections=%d, want one reused connection", connections.Load())
	}
}
