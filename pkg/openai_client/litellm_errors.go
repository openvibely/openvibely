package openaiclient

import "strings"

// LiteLLM's HTTP gateway puts its normalized exception class at the start of
// error.message. Read only that class, never a class mentioned in fallback
// history or a model name later in the message.
func liteLLMExceptionName(message string) string {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, "litellm.") {
		return ""
	}
	name, _, ok := strings.Cut(strings.TrimPrefix(message, "litellm."), ":")
	if !ok || strings.ContainsAny(name, " \t\r\n") {
		return ""
	}
	return name
}

func liteLLMExceptionStatus(name string) int {
	switch name {
	case "ContextWindowExceededError", "BadRequestError", "InvalidRequestError", "UnsupportedParamsError", "ContentPolicyViolationError", "ImageFetchError":
		return 400
	case "AuthenticationError":
		return 401
	case "PaymentRequiredError":
		return 402
	case "PermissionDeniedError":
		return 403
	case "NotFoundError":
		return 404
	case "Timeout":
		return 408
	case "UnprocessableEntityError":
		return 422
	case "RateLimitError", "BudgetExceededError":
		return 429
	case "APIConnectionError", "APIError", "InternalServerError":
		return 500
	case "ServiceUnavailableError":
		return 503
	}
	return 0
}

// Specific structured codes win, followed by LiteLLM's exception class. Only
// generic request errors need prose matching. Numeric codes are HTTP statuses,
// not specific error categories.
func isCompatibleContextOverflow(e *APIError) bool {
	if e.Code == "context_length_exceeded" {
		return true
	}
	if e.StatusCode != 0 && e.StatusCode != 400 {
		return false
	}
	if e.Code != "" && e.Code != "400" && e.Code != "invalid_request_error" && e.Code != "bad_request_error" {
		return false
	}
	name := liteLLMExceptionName(e.Message)
	switch name {
	case "ContextWindowExceededError":
		return true
	case "", "BadRequestError", "InvalidRequestError":
		// Generic wrappers can contain a more specific provider diagnosis.
	default:
		return false
	}

	// Router diagnostics can mention failures from other models. They must not
	// change the classification of the original request failure.
	primary, _, _ := strings.Cut(e.Message, "\n\n")
	msg := strings.ToLower(strings.ReplaceAll(primary, "`", ""))
	if strings.Contains(msg, "input length and max_tokens exceed context limit") ||
		strings.Contains(msg, "input tokens + max_tokens exceed") ||
		strings.Contains(msg, "inputs tokens + max_new_tokens must be") {
		return true
	}
	// These describe the output setting itself, not input + output fitting in
	// the context window. Do not exclude every message mentioning max_tokens.
	for _, phrase := range []string{"max_tokens must be <=", "max_tokens must be less than", "max_tokens must be at most", "max_tokens exceeds the maximum output", "maximum output tokens", "max output tokens", "output budget"} {
		if strings.Contains(msg, phrase) {
			return false
		}
	}
	for _, phrase := range []string{"context window", "context length", "context_length_exceeded", "context limit", "maximum context", "too many tokens", "token limit exceeded", "input tokens exceed", "input is too long", "prompt is too long"} {
		if strings.Contains(msg, phrase) {
			return true
		}
	}
	return false
}
