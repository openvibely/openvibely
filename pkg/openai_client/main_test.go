package openaiclient

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
)

// TestMain points the OAuth token endpoint at a local server for the whole package so an
// accidental refresh can never reach auth.openai.com. Tests that exercise refresh still
// install their own server; anything reaching this one fails the run.
func TestMain(m *testing.M) {
	var unexpectedRefreshes atomic.Int32
	guard := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		unexpectedRefreshes.Add(1)
		http.Error(w, "test attempted an OAuth refresh without its own token server", http.StatusInternalServerError)
	}))
	OpenAIOAuthTokenURL = guard.URL
	code := m.Run()
	guard.Close()
	if n := unexpectedRefreshes.Load(); n > 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d test OAuth refresh request(s) hit the guard server; give test tokens a longer expiry or install a token server\n", n)
		if code == 0 {
			code = 1
		}
	}
	os.Exit(code)
}
