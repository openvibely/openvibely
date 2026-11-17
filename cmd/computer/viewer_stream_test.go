//go:build linux

package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestViewerScreenProxyStreamsBeforeMessageCompletes(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pair := func() (*websocket.Conn, *websocket.Conn) {
		accepted := make(chan *websocket.Conn, 1)
		stop := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			accepted <- c
			<-stop
		}))
		t.Cleanup(server.Close)
		t.Cleanup(func() { close(stop) })
		client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
		require.NoError(t, err)
		remote := <-accepted
		client.SetReadLimit(2 << 20)
		remote.SetReadLimit(2 << 20)
		t.Cleanup(func() { client.CloseNow(); remote.CloseNow() })
		return client, remote
	}
	source, upstream := pair()
	downstream, receiver := pair()
	done := make(chan error, 1)
	go func() { done <- forwardViewerScreen(ctx, downstream, upstream) }()
	prefix := bytes.Repeat([]byte{42}, 64<<10)
	writer, err := source.Writer(ctx, websocket.MessageBinary)
	require.NoError(t, err)
	_, err = writer.Write(prefix)
	require.NoError(t, err)
	// The source writer is deliberately still open: a Read/Write proxy cannot
	// deliver anything until it is closed.
	kind, reader, err := receiver.Reader(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, kind)
	first := make([]byte, 1024)
	_, err = io.ReadFull(reader, first)
	require.NoError(t, err)
	require.Equal(t, prefix[:1024], first)
	require.NoError(t, writer.Close())
	rest, err := io.ReadAll(reader)
	require.NoError(t, err)
	require.Equal(t, prefix[1024:], rest)
	require.NoError(t, source.Write(ctx, websocket.MessageBinary, []byte("next message")))
	kind, data, err := receiver.Read(ctx)
	require.NoError(t, err)
	require.Equal(t, websocket.MessageBinary, kind)
	require.Equal(t, "next message", string(data))
	// Cancellation must also interrupt a message whose sender never finishes.
	writer, err = source.Writer(ctx, websocket.MessageBinary)
	require.NoError(t, err)
	_, err = writer.Write(prefix)
	require.NoError(t, err)
	_, reader, err = receiver.Reader(ctx)
	require.NoError(t, err)
	_, err = io.ReadFull(reader, first)
	require.NoError(t, err)
	cancel()
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("proxy did not stop after cancellation")
	}
}
