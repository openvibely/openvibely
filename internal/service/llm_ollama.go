package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

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

const defaultOllamaRequestTimeout = 10 * time.Minute

// OllamaHTTPClient is an interface for making HTTP requests to Ollama.
// Can be overridden in tests.
var OllamaHTTPClient HTTPDoer = &http.Client{Timeout: defaultOllamaRequestTimeout}

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
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	url := strings.TrimRight(baseURL, "/") + "/api/tags"

	type bufferedResponse struct {
		statusCode int
		body       []byte
	}
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	buffered, err := httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (bufferedResponse, bool, error) {
		result := bufferedResponse{}
		resp, err := httpretry.Do(attemptCtx, OllamaHTTPClient, func() (*http.Request, error) {
			return http.NewRequestWithContext(attemptCtx, http.MethodGet, url, nil)
		}, policy)
		if err != nil {
			return result, false, err
		}
		defer resp.Body.Close()
		result.statusCode = resp.StatusCode
		result.body, err = io.ReadAll(resp.Body)
		if err != nil {
			return result, false, httpretry.NewStreamError(fmt.Errorf("reading ollama model list response: %w", err))
		}
		if httpretry.IsRetryableStatus(resp.StatusCode) {
			return result, false, httpretry.NewResponseError(resp, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, string(result.body)))
		}
		return result, false, nil
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
