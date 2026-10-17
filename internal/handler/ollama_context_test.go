package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestOllamaContextModalRouteAndSavedBudget(t *testing.T) {
	_, e, repo := setupTestHandler(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/api/ps", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"models":[{"name":"local:latest","context_length":32768}]}`))
	}))
	defer provider.Close()
	for _, tc := range []struct {
		model string
		want  int
	}{{"local", 32768}, {"unloaded", 0}} {
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/models/ollama/context?"+url.Values{"base_url": {provider.URL}, "model": {tc.model}}.Encode(), nil))
		require.Equal(t, http.StatusOK, rec.Code)
		var body map[string]int
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
		require.Equal(t, tc.want, body["context_length"])
	}
	config := &models.LLMConfig{Name: "Saved Ollama", Provider: models.ProviderOllama, Model: "local", OllamaBaseURL: provider.URL, ContextWindow: 65536, DefaultMaxTokens: 10000}
	require.NoError(t, repo.Create(context.Background(), config))
	form := url.Values{"name": {config.Name}, "provider": {"ollama"}, "model": {"local"}, "ollama_base_url": {provider.URL}, "context_window": {"65536"}, "default_max_tokens": {"10000"}}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/models/"+config.ID, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	saved, err := repo.GetByID(context.Background(), config.ID)
	require.NoError(t, err)
	require.Equal(t, models.ProviderOllama, saved.Provider)
	require.Equal(t, 65536, saved.ContextWindow)
	require.Equal(t, 10000, saved.DefaultMaxTokens)
}
