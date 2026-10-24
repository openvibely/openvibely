package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProfilingOptIn(t *testing.T) {
	stop, err := startProfiling("")
	require.NoError(t, err)
	stop()
	for _, port := range []string{"localhost:6060", "-1", "65536", "0.0.0.0:6060"} {
		_, err := startProfiling(port)
		require.Error(t, err)
	}
	stop, err = startProfiling("0")
	require.NoError(t, err)
	stop()
	stop()
}

func TestProfilingRoutes(t *testing.T) {
	mux := profilingMux()
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/heap?gc=1", "/debug/pprof/allocs", "/debug/pprof/goroutine?debug=1"} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		require.Equal(t, http.StatusOK, response.Code, path)
		require.NotEmpty(t, response.Body.Bytes(), path)
	}
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/tasks", nil))
	require.Equal(t, http.StatusNotFound, response.Code)
	response = httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/debug/pprof/heap", nil))
	require.Equal(t, http.StatusMethodNotAllowed, response.Code)
}
