package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func testXClient(server *httptest.Server) *XAPIClient {
	c := NewXAPIClient(XCredentials{ConsumerKey: "consumer-key", ConsumerSecret: "consumer-secret", AccessToken: "access-token", AccessTokenSecret: "access-secret"})
	c.baseURL = server.URL
	c.client = server.Client()
	c.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.now = func() time.Time { return time.Unix(1700000000, 0) }
	c.nonce = func() string { return "nonce" }
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}
func TestXAPIClientSignsAndDecodesAuthenticatedUser(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/2/users/me", r.URL.Path)
		auth := r.Header.Get("Authorization")
		require.True(t, strings.HasPrefix(auth, "OAuth "))
		require.Contains(t, auth, `oauth_consumer_key="consumer-key"`)
		require.Contains(t, auth, `oauth_token="access-token"`)
		require.NotContains(t, auth, "consumer-secret")
		require.NotContains(t, auth, "access-secret")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"id":"1","username":"bot"}}`))
	}))
	defer server.Close()
	user, err := testXClient(server).Me(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1", user.ID)
	require.Equal(t, "bot", user.Username)
}
func TestXAPIClientRetriesRateLimitedReads(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"detail":"slow down"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"1","username":"bot"}}`))
	}))
	defer server.Close()
	user, err := testXClient(server).Me(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1", user.ID)
	require.Equal(t, int32(2), calls.Load())
}

func TestXAPIClientDoesNotRetryAmbiguousPostFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"detail":"provider unavailable"}`, http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, err := testXClient(server).Post(context.Background(), "hello", "")
	require.Error(t, err)
	require.Equal(t, int32(1), calls.Load())
}

func TestXRetryDelayClampsOversizedDecimal(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": {strings.Repeat("9", 1000)}}}
	require.Equal(t, time.Minute, xRetryDelay(resp, 1, time.Now()))
}
func TestXAPIClientCancellationStopsRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c := testXClient(server)
	c.sleep = func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Me(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.LessOrEqual(t, calls.Load(), int32(1))
}
func TestXAPIClientRejectsRedirectWithoutReplayingAuthorization(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected.Add(1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	_, err := testXClient(origin).Me(context.Background())
	require.Error(t, err)
	require.Equal(t, int32(0), redirected.Load())
}

func TestXProviderErrorDoesNotExposeResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `token=secret-provider-payload`, http.StatusUnauthorized)
	}))
	defer server.Close()
	_, err := testXClient(server).Me(context.Background())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "secret-provider-payload")
}
