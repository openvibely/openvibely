package retry

import (
	"errors"
	"testing"

	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func TestClassifyOpenAIError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want OpenAIErrorClass
	}{
		{name: "retryable 429", err: errors.New("429 too many requests"), want: OpenAIErrorRetryable},
		{name: "fatal auth", err: errors.New("invalid api key provided"), want: OpenAIErrorFatal},
		{name: "fallback missing scopes", err: errors.New("missing scopes: responses.write"), want: OpenAIErrorFallbackable},
		{name: "fallback endpoint", err: errors.New("endpoint does not support this model"), want: OpenAIErrorFallbackable},
		{name: "fatal sentinel quota", err: openaiclient.ErrQuotaExceeded, want: OpenAIErrorFatal},
		{name: "fallback sentinel model", err: openaiclient.ErrModelNotFound, want: OpenAIErrorFallbackable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyOpenAIError(tt.err)
			if got != tt.want {
				t.Fatalf("ClassifyOpenAIError() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestShouldFallbackOpenAI(t *testing.T) {
	if !ShouldFallbackOpenAI(errors.New("missing scopes: responses.write")) {
		t.Fatal("expected fallback to be true")
	}
	if ShouldFallbackOpenAI(errors.New("invalid api key")) {
		t.Fatal("expected fallback to be false")
	}
}
