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

func TestDetectOllamaContextWindow(t *testing.T) {
	original := OllamaHTTPClient
	t.Cleanup(func() { OllamaHTTPClient = original })
	for _, tc := range []struct {
		name, model, body string
		status, want      int
	}{
		{"loaded", "qwen", `{"models":[{"name":"other:latest","context_length":999},{"model":"qwen:latest","context_length":65536}]}`, 200, 65536},
		{"exact tag", "qwen:small", `{"models":[{"name":"qwen:latest","context_length":65536},{"name":"qwen:small","context_length":8192}]}`, 200, 8192},
		{"unloaded", "qwen", `{"models":[{"name":"other:latest","context_length":999}]}`, 200, 0},
		{"missing metadata", "qwen", `{"models":[{"name":"qwen:latest"}]}`, 200, 0},
		{"failed", "qwen", `private response`, 500, 0},
		{"malformed", "qwen", `not json`, 200, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			OllamaHTTPClient = ollamaListDoerFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/api/ps" {
					t.Fatalf("path %s", req.URL.Path)
				}
				if _, ok := req.Context().Deadline(); !ok {
					t.Fatal("missing discovery deadline")
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})
			got, err := DetectOllamaContextWindow(context.Background(), "http://fixture/v1/", tc.model)
			if got != tc.want {
				t.Fatalf("got %d want %d", got, tc.want)
			}
			if tc.name == "failed" || tc.name == "malformed" {
				if err == nil {
					t.Fatal("expected error")
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDetectOllamaContextCancellation(t *testing.T) {
	original := OllamaHTTPClient
	t.Cleanup(func() { OllamaHTTPClient = original })
	ctx, cancel := context.WithCancel(context.Background())
	OllamaHTTPClient = ollamaListDoerFunc(func(req *http.Request) (*http.Response, error) {
		cancel()
		<-req.Context().Done()
		return nil, req.Context().Err()
	})
	_, err := DetectOllamaContextWindow(ctx, "http://fixture", "qwen")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}
