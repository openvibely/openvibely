package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

func TestListOllamaModelsHasDiscoveryDeadline(t *testing.T) {
	original := OllamaHTTPClient
	t.Cleanup(func() { OllamaHTTPClient = original })
	attempts := 0
	OllamaHTTPClient = ollamaListDoerFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > ModelDiscoveryTimeout {
			t.Fatalf("expected shared model discovery deadline, got %v, present=%v", deadline, ok)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":[]}`))}, nil
	})
	_, err := ListOllamaModels(context.Background(), "http://ollama.invalid")
	if err != nil || attempts != 1 {
		t.Fatalf("err=%v attempts=%d; expected successful discovery", err, attempts)
	}
}

func TestListOllamaModelsCancelsStalledRequest(t *testing.T) {
	original := OllamaHTTPClient
	t.Cleanup(func() { OllamaHTTPClient = original })
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	attempts := 0
	OllamaHTTPClient = ollamaListDoerFunc(func(req *http.Request) (*http.Response, error) {
		attempts++
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	started := time.Now()
	_, err := ListOllamaModels(ctx, "http://ollama.invalid")
	if !errors.Is(err, context.DeadlineExceeded) || attempts != 1 || time.Since(started) > time.Second {
		t.Fatalf("stalled discovery: err=%v attempts=%d elapsed=%v", err, attempts, time.Since(started))
	}
}
