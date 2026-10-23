//go:build openvibely_memory_diagnostics && darwin

package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDesktopMemoryDiagnosticsOptIn(t *testing.T) {
	t.Setenv("OPENVIBELY_MEMORY_DIAGNOSTICS", "")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	stop, err := startDesktopMemoryDiagnostics()
	require.NoError(t, err)
	stop()
	require.Empty(t, logs.String())
}

func TestDesktopMemoryDiagnosticsPrivateSocketAndProfiles(t *testing.T) {
	t.Setenv("OPENVIBELY_MEMORY_DIAGNOSTICS", "1")
	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(previous)
	stop, err := startDesktopMemoryDiagnostics()
	require.NoError(t, err)
	defer stop()
	match := regexp.MustCompile(`socket=(\S+)`).FindStringSubmatch(logs.String())
	require.Len(t, match, 2)
	socket := match[1]
	for path, mode := range map[string]os.FileMode{filepath.Dir(socket): 0700, socket: 0600} {
		info, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, mode, info.Mode().Perm())
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	get := func(path string) []byte {
		response, err := client.Get("http://localhost" + path)
		require.NoError(t, err)
		defer response.Body.Close()
		require.Equal(t, http.StatusOK, response.StatusCode)
		body, err := io.ReadAll(response.Body)
		require.NoError(t, err)
		return body
	}
	var stats struct {
		PID    int
		Memory runtime.MemStats
	}
	require.NoError(t, json.Unmarshal(get("/stats"), &stats))
	require.Equal(t, os.Getpid(), stats.PID)
	require.Positive(t, stats.Memory.HeapAlloc)
	for _, path := range []string{"/heap?gc=1", "/allocs"} {
		reader, err := gzip.NewReader(bytes.NewReader(get(path)))
		require.NoError(t, err)
		data, err := io.ReadAll(reader)
		require.NoError(t, err)
		require.NotEmpty(t, data)
		require.NoError(t, reader.Close())
	}
	for _, path := range []string{"/debug/pprof/", "/cmdline", "/profile"} {
		response, err := client.Get("http://localhost" + path)
		require.NoError(t, err)
		response.Body.Close()
		require.Equal(t, http.StatusNotFound, response.StatusCode)
	}
	response, err := client.Post("http://localhost/heap", "text/plain", nil)
	require.NoError(t, err)
	response.Body.Close()
	require.Equal(t, http.StatusMethodNotAllowed, response.StatusCode)
	stop()
	stop()
	_, err = os.Stat(filepath.Dir(socket))
	require.True(t, os.IsNotExist(err))
	_, err = client.Get("http://localhost/stats")
	require.Error(t, err)
}
