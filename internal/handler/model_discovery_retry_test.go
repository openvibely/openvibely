package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/retrytest"
	"github.com/stretchr/testify/require"
)

func TestCompatibleModelDiscoveryRetriesWithHeaders(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		require.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
		require.Equal(t, "fixture", r.Header.Get("X-Discovery"))
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(retrytest.Context(), 3*time.Second)
	defer cancel()
	found, err := fetchOpenAICompatibleModels(ctx, httpclient.New(3*time.Second), server.URL, "fixture-key", &models.LLMConfig{ExtraHeadersJSON: `{"X-Discovery":"fixture"}`})
	require.NoError(t, err)
	require.Len(t, found, 1)
	require.Equal(t, "fixture-model", found[0].ID)
	require.Equal(t, 2, attempts)
}
