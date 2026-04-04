package openaiclient

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
)

// Common sentinel errors
var (
	// ErrNoAuth is returned when authentication is missing or invalid.
	ErrNoAuth = errors.New("missing or invalid authentication")

	// ErrTokenExpired is returned when the OAuth token has expired and cannot be refreshed.
	ErrTokenExpired = errors.New("token expired and refresh failed")

	// ErrRateLimited is returned when the API rate limit is exceeded.
	ErrRateLimited = errors.New("rate limit exceeded")

	// ErrQuotaExceeded is returned when the API quota is exceeded.
	ErrQuotaExceeded = errors.New("quota exceeded")

	// ErrModelNotFound is returned when the requested model doesn't exist.
	ErrModelNotFound = errors.New("model not found")

	// ErrContextLengthExceeded is returned when the input exceeds the model's context window.
	ErrContextLengthExceeded = errors.New("context length exceeded")

	// ErrTimeout is returned when a request times out.
	ErrTimeout = errors.New("request timeout")

	// ErrNetworkError is returned for network-related failures.
	ErrNetworkError = errors.New("network error")
)

// APIError represents an error response from the OpenAI API.
type APIError struct {
	StatusCode int    `json:"status_code"`
	Type       string `json:"type"`
	Message    string `json:"message"`
	Code       string `json:"code,omitempty"`
	Param      string `json:"param,omitempty"`
}

// Error implements the error interface.
func (e *APIError) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("openai api error (status %d, code %s): %s", e.StatusCode, e.Code, e.Message)
	}
	return fmt.Sprintf("openai api error (status %d): %s", e.StatusCode, e.Message)
}

// Is implements error matching for sentinel errors.
func (e *APIError) Is(target error) bool {
	switch e.StatusCode {
	case 401, 403:
		return target == ErrNoAuth
	case 429:
		return target == ErrRateLimited
	case 404:
		if strings.Contains(strings.ToLower(e.Message), "model") {
			return target == ErrModelNotFound
		}
	case 400:
		if e.Code == "context_length_exceeded" || strings.Contains(strings.ToLower(e.Message), "context length") {
			return target == ErrContextLengthExceeded
		}
	}

	// Check error codes for quota
	if e.Code == "insufficient_quota" || strings.Contains(strings.ToLower(e.Message), "quota") {
		return target == ErrQuotaExceeded
	}

	return false
}

// Temporary returns true if the error is likely temporary and retryable.
func (e *APIError) Temporary() bool {
	return isRetryable(e.StatusCode)
}

// parseAPIError attempts to parse an API error from the response body.
func parseAPIError(statusCode int, body []byte) error {
	if len(body) == 0 {
		return &APIError{
			StatusCode: statusCode,
			Message:    "empty response body",
		}
	}

	// Try to parse as JSON error response
	var errResp struct {
		Error *APIError `json:"error"`
	}

	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != nil {
		errResp.Error.StatusCode = statusCode
		return errResp.Error
	}

	// If not JSON, use the body as the message
	return &APIError{
		StatusCode: statusCode,
		Message:    strings.TrimSpace(string(body)),
	}
}

// isNetworkError checks if an error is network-related.
func isNetworkError(err error) bool {
	if err == nil {
		return false
	}

	// Check for common network error types
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// Check for specific error strings
	errStr := strings.ToLower(err.Error())
	networkStrings := []string{
		"connection refused",
		"no such host",
		"timeout",
		"network is unreachable",
		"connection reset",
		"broken pipe",
		"eof",
		"tls handshake",
	}

	for _, s := range networkStrings {
		if strings.Contains(errStr, s) {
			return true
		}
	}

	return false
}

// wrapNetworkError wraps network errors with additional context.
func wrapNetworkError(err error) error {
	if err == nil || !isNetworkError(err) {
		return err
	}

	// Check for timeout
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%w: %w", ErrTimeout, err)
	}

	return fmt.Errorf("%w: %w", ErrNetworkError, err)
}