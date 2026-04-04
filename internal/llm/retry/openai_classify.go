package retry

import (
	"errors"
	"strings"

	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

type OpenAIErrorClass string

const (
	OpenAIErrorFatal       OpenAIErrorClass = "fatal"
	OpenAIErrorRetryable   OpenAIErrorClass = "retryable"
	OpenAIErrorFallbackable OpenAIErrorClass = "fallbackable"
)

// ClassifyOpenAIError categorizes OpenAI call failures for retry/fallback policy.
func ClassifyOpenAIError(err error) OpenAIErrorClass {
	if err == nil {
		return OpenAIErrorFatal
	}
	if IsRetryable(err) {
		return OpenAIErrorRetryable
	}

	if errors.Is(err, openaiclient.ErrNoAuth) ||
		errors.Is(err, openaiclient.ErrTokenExpired) ||
		errors.Is(err, openaiclient.ErrQuotaExceeded) ||
		errors.Is(err, openaiclient.ErrContextLengthExceeded) {
		return OpenAIErrorFatal
	}
	if errors.Is(err, openaiclient.ErrModelNotFound) {
		return OpenAIErrorFallbackable
	}

	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if msg == "" {
		return OpenAIErrorFatal
	}

	fatalHints := []string{
		"invalid api key",
		"incorrect api key",
		"unauthorized",
		"forbidden",
		"authentication",
		"oauth token refresh failed",
		"context length",
		"insufficient_quota",
	}
	for _, h := range fatalHints {
		if strings.Contains(msg, h) {
			return OpenAIErrorFatal
		}
	}

	fallbackHints := []string{
		"missing scopes:",
		"/v1/responses",
		"model not found",
		"unsupported parameter",
		"unsupported model",
		"endpoint does not support",
		"responses api",
	}
	for _, h := range fallbackHints {
		if strings.Contains(msg, h) {
			return OpenAIErrorFallbackable
		}
	}

	return OpenAIErrorFatal
}

func ShouldFallbackOpenAI(err error) bool {
	return ClassifyOpenAIError(err) == OpenAIErrorFallbackable
}
