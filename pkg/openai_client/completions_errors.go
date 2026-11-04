package openaiclient

import (
	"encoding/json"
	"errors"
	"strconv"

	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

// CompatibleStreamError marks errors decoded only from OpenAI-compatible
// Chat Completions streams. Native provider errors do not use this classifier.
type CompatibleStreamError struct {
	apiErr          *APIError
	contextOverflow bool
}

func (e *CompatibleStreamError) Error() string { return e.apiErr.Error() }
func (e *CompatibleStreamError) Unwrap() error { return e.apiErr }

// IsContextOverflow exposes the compatible provider's classification to recovery.
func (e *CompatibleStreamError) IsContextOverflow() bool { return e.contextOverflow }

// CategorizeCompatibleStreamError preserves the provider decision without
// changing classification of native OpenAI or Anthropic errors.
func CategorizeCompatibleStreamError(err error) error {
	var compatibleErr *CompatibleStreamError
	if !errors.As(err, &compatibleErr) {
		return err
	}
	if compatibleErr.IsContextOverflow() {
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorContextWindowExceeded, "compatible provider request", err)
	}
	switch liteLLMExceptionName(compatibleErr.apiErr.Message) {
	case "Timeout", "APIConnectionError":
		return llmcontracts.NewCategorizedError(llmcontracts.ErrorTransportFailure, "compatible provider transport", err)
	}
	return err
}

// completionsStreamError handles failures delivered after the HTTP response has
// already succeeded. Preserve provider fields rather than treating HTTP 200 as
// the error's status. LiteLLM may encode a status in code as a number or string.
func completionsStreamError(raw, status, statusCode json.RawMessage) error {
	var fields map[string]json.RawMessage
	apiErr := &APIError{}
	if json.Unmarshal(raw, &fields) == nil {
		apiErr.Message = streamErrorScalar(fields["message"])
		apiErr.Type = streamErrorScalar(fields["type"])
		apiErr.Code = streamErrorScalar(fields["code"])
		apiErr.Param = streamErrorScalar(fields["param"])
	}
	for _, value := range []json.RawMessage{fields["status_code"], fields["status"], statusCode, status, fields["code"]} {
		if n, err := strconv.Atoi(streamErrorScalar(value)); err == nil && n >= 400 && n <= 599 {
			apiErr.StatusCode = n
			break
		}
	}
	if apiErr.StatusCode == 0 {
		apiErr.StatusCode = completionsErrorStatus(apiErr.Code)
	}
	if apiErr.StatusCode == 0 {
		apiErr.StatusCode = liteLLMExceptionStatus(liteLLMExceptionName(apiErr.Message))
	}
	if apiErr.StatusCode == 0 {
		apiErr.StatusCode = completionsErrorStatus(apiErr.Type)
	}
	if apiErr.Message == "" {
		apiErr.Message = firstNonEmpty(apiErr.Code, apiErr.Type, streamErrorScalar(raw), "provider returned a stream error")
	}
	// ResponseError makes the existing retry policy honor the decoded status,
	// not words in appended diagnostics. Unknown status (0) is non-retryable.
	// This is not the outer HTTP 200; APIError remains available via errors.As.
	return &httpretry.ResponseError{StatusCode: apiErr.StatusCode, Err: &CompatibleStreamError{
		apiErr:          apiErr,
		contextOverflow: isCompatibleContextOverflow(apiErr),
	}}
}

func streamErrorScalar(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.String()
	}
	return ""
}

func completionsErrorStatus(value string) int {
	switch value {
	case "context_length_exceeded", "invalid_request_error", "bad_request_error", "unsupported_feature", "model_not_supported":
		return 400
	case "authentication_error", "invalid_api_key":
		return 401
	case "permission_error":
		return 403
	case "rate_limit_error", "rate_limit_exceeded", "insufficient_quota", "throttling_error", "budget_exceeded":
		return 429
	case "server_error", "internal_server_error":
		return 500
	case "overloaded_error":
		return 529
	}
	return 0
}
