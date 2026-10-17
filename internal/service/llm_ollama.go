package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/httpclient"
	"github.com/openvibely/openvibely/internal/httpretry"
)

// ---- Ollama Model Listing Types ----

// OllamaModelList is the response from GET /api/tags.
type OllamaModelList struct {
	Models []OllamaModelInfo `json:"models"`
}

// OllamaModelInfo describes a model available on the Ollama instance.
type OllamaModelInfo struct {
	Name       string             `json:"name"`
	Model      string             `json:"model"`
	ModifiedAt time.Time          `json:"modified_at"`
	Size       int64              `json:"size"`
	Digest     string             `json:"digest"`
	Details    OllamaModelDetails `json:"details"`
}

// OllamaModelDetails holds metadata about an Ollama model.
type OllamaModelDetails struct {
	ParentModel       string   `json:"parent_model"`
	Format            string   `json:"format"`
	Family            string   `json:"family"`
	Families          []string `json:"families"`
	ParameterSize     string   `json:"parameter_size"`
	QuantizationLevel string   `json:"quantization_level"`
}

// OllamaHTTPClient is an interface for making HTTP requests to Ollama.
// Can be overridden in tests.
var OllamaHTTPClient HTTPDoer = httpclient.NewModel(nil)

// HTTPDoer is an interface for making HTTP requests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// ollamaErrorResponse is the error response from Ollama API.
type ollamaErrorResponse struct {
	Error string `json:"error"`
}

// ListOllamaModels queries an Ollama instance for available models.
func ListOllamaModels(ctx context.Context, baseURL string) ([]OllamaModelInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, ModelDiscoveryTimeout)
	defer cancel()
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	url := strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1") + "/api/tags"

	type bufferedResponse struct {
		statusCode int
		body       []byte
	}
	buffered, err := httpretry.DoStreamTurn(ctx, httpretry.StreamTurnPolicy{}, func(attemptCtx context.Context) (bufferedResponse, error) {
		result := bufferedResponse{}
		policy := httpretry.DefaultPolicy()
		policy.MaxRetries = 0
		policy.AllowReplay = true
		resp, err := httpretry.Do(attemptCtx, OllamaHTTPClient, func() (*http.Request, error) {
			return http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
		}, policy)
		if err != nil {
			return result, err
		}
		defer resp.Body.Close()
		result.statusCode = resp.StatusCode
		result.body, err = io.ReadAll(resp.Body)
		if err != nil {
			return result, httpretry.NewStreamError(fmt.Errorf("reading ollama model list response: %w", err))
		}
		if httpretry.IsRetryableStatus(resp.StatusCode) {
			return result, httpretry.NewResponseError(resp, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, string(result.body)))
		}
		return result, nil
	})
	if err != nil {
		return nil, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}

	if buffered.statusCode != http.StatusOK {
		var errResp ollamaErrorResponse
		if json.Unmarshal(buffered.body, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("ollama API error (%d): %s", buffered.statusCode, errResp.Error)
		}
		return nil, fmt.Errorf("ollama API error (%d): %s", buffered.statusCode, string(buffered.body))
	}

	var modelList OllamaModelList
	if err := json.Unmarshal(buffered.body, &modelList); err != nil {
		return nil, fmt.Errorf("parsing ollama model list: %w", err)
	}

	return modelList.Models, nil
}

// DetectOllamaContextWindow reports only the selected model's active server
// window. Zero means unloaded or unavailable; no model is loaded by this call.
func DetectOllamaContextWindow(ctx context.Context, baseURL, model string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, ModelDiscoveryTimeout)
	defer cancel()
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	baseURL = strings.TrimSuffix(baseURL, "/v1")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/ps", nil)
	if err != nil {
		return 0, fmt.Errorf("invalid Ollama endpoint")
	}
	resp, err := OllamaHTTPClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, fmt.Errorf("Ollama context discovery unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Ollama context discovery status %d", resp.StatusCode)
	}
	var result struct {
		Models []struct {
			Name          string `json:"name"`
			Model         string `json:"model"`
			ContextLength int    `json:"context_length"`
		} `json:"models"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&result); err != nil {
		return 0, fmt.Errorf("invalid Ollama context discovery response")
	}
	canonical := func(name string) string {
		name = strings.TrimSpace(name)
		if name != "" && !strings.Contains(name[strings.LastIndex(name, "/")+1:], ":") {
			name += ":latest"
		}
		return name
	}
	if strings.TrimSpace(model) == "" {
		return 0, nil
	}
	for _, entry := range result.Models {
		if canonical(entry.Name) == canonical(model) || canonical(entry.Model) == canonical(model) {
			return max(0, entry.ContextLength), nil
		}
	}
	return 0, nil
}
