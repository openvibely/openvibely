package service

import (
	"context"
	"fmt"
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
	c.after = func(time.Duration) <-chan time.Time {
		ready := make(chan time.Time, 1)
		ready <- time.Now()
		return ready
	}
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
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		n := calls.Add(1)
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, `{"detail":"slow down"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"1","username":"bot"}}`))
	}))
	defer server.Close()
	c := testXClient(server)
	var nonce atomic.Int32
	c.nonce = func() string { return fmt.Sprintf("nonce-%d", nonce.Add(1)) }
	user, err := c.Me(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1", user.ID)
	require.Equal(t, int32(2), calls.Load())
	require.Len(t, authHeaders, 2)
	require.NotEqual(t, authHeaders[0], authHeaders[1])
}

func TestXAPIClientRetriesRateLimitResetWithBoundedDelay(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("x-rate-limit-reset", "1700000120")
			http.Error(w, `{"detail":"slow down"}`, http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"1","username":"bot"}}`))
	}))
	defer server.Close()
	c := testXClient(server)
	var delays []time.Duration
	c.after = func(delay time.Duration) <-chan time.Time {
		delays = append(delays, delay)
		ready := make(chan time.Time, 1)
		ready <- time.Now()
		return ready
	}
	user, err := c.Me(context.Background())
	require.NoError(t, err)
	require.Equal(t, "1", user.ID)
	require.Equal(t, int32(2), calls.Load())
	require.Equal(t, []time.Duration{time.Minute}, delays)
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

func TestXNormalizeRetryHeadersClampsOversizedDecimal(t *testing.T) {
	resp := &http.Response{Header: http.Header{"Retry-After": {strings.Repeat("9", 1000)}}}
	xNormalizeRetryHeaders(resp, time.Now())
	require.Equal(t, "60", resp.Header.Get("Retry-After"))
}
func TestXAPIClientCancellationStopsRetry(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	c := testXClient(server)
	ctx, cancel := context.WithCancel(context.Background())
	c.after = func(time.Duration) <-chan time.Time {
		cancel()
		return make(chan time.Time)
	}
	_, err := c.Me(ctx)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), calls.Load())
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
