package server

import (
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strconv"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
)

// startProfiling uses a dedicated loopback listener, never the application mux.
// An empty port disables profiling; zero selects an available loopback port.
func startProfiling(port string) (func(), error) {
	if port == "" {
		return func() {}, nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 0 || n > 65535 {
		return nil, fmt.Errorf("OPENVIBELY_PPROF_PORT must be a port number between 0 and 65535")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(n)))
	if err != nil {
		return nil, fmt.Errorf("start profiling listener: %w", err)
	}
	mux := profilingMux()
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	applog.Infof("Go profiling enabled at http://%s/debug/pprof/", listener.Addr())
	go func() {
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			applog.Infof("profiling server stopped: %v", err)
		}
	}()
	return func() { _ = srv.Close(); _ = listener.Close() }, nil
}

func profilingMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /debug/pprof/", pprof.Index)
	mux.HandleFunc("GET /debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("GET /debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("GET /debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("GET /debug/pprof/trace", pprof.Trace)
	return mux
}
