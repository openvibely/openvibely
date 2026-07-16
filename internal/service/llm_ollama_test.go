package service

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type ollamaListDoerFunc func(*http.Request) (*http.Response, error)

func (f ollamaListDoerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }

func TestListOllamaModelsRetriesTransientStatus(t *testing.T) {
	original := OllamaHTTPClient
	t.Cleanup(func() { OllamaHTTPClient = original })
	attempts := 0
	OllamaHTTPClient = ollamaListDoerFunc(func(*http.Request) (*http.Response, error) {
		attempts++
		if attempts == 1 {
			return &http.Response{StatusCode: http.StatusServiceUnavailable, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"error":"unavailable"}`))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[{"name":"llama3","model":"llama3"}]}`))}, nil
	})

	models, err := ListOllamaModels(context.Background(), "http://ollama.invalid")
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || len(models) != 1 || models[0].Name != "llama3" {
		t.Fatalf("attempts/models = %d/%#v, want 2/llama3", attempts, models)
	}
}
