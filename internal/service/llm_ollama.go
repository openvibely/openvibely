package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
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
var OllamaHTTPClient HTTPDoer = &http.Client{Timeout: 5 * time.Minute}

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

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating ollama list request: %w", err)
	}

	resp, err := OllamaHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading ollama model list response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp ollamaErrorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return nil, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var modelList OllamaModelList
	if err := json.Unmarshal(respBody, &modelList); err != nil {
		return nil, fmt.Errorf("parsing ollama model list: %w", err)
	}

	return modelList.Models, nil
}
