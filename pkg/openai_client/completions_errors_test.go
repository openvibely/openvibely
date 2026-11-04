package openaiclient

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/testutil/retrytest"
)

const liteLLMOverflowFrame = `{"error":{"message":"litellm.ContextWindowExceededError: litellm.BadRequestError: litellm._pre_call_checks: Context Window exceeded for given call. No models have context window large enough for this call.\nModel=bedrock/us.anthropic.claude-sonnet-4-5-20250929-v1:0, Max Input Tokens=200000, Got=200805\n\nLiteLLM: model group 'premium' failed with the error above. No fallback was attempted.","type":"invalid_request_error"}}`

func TestCompletionsStreamErrorRecovery(t *testing.T) {
	tests := []struct {
		name, frame     string
		status          int
		code, typ       string
		overflow, retry bool
	}{
		{name: "captured LiteLLM overflow", frame: liteLLMOverflowFrame, status: 400, typ: "invalid_request_error", overflow: true},
		{name: "structured overflow", frame: `{"error":{"code":"context_length_exceeded","message":"opaque"}}`, status: 400, code: "context_length_exceeded", overflow: true},
		{name: "output limit is not input overflow", frame: `{"error":{"type":"invalid_request_error","message":"max_tokens must be <= 8192; got 16000"}}`, status: 400, typ: "invalid_request_error"},
		{name: "LiteLLM context class with appended output parameter", frame: `{"error":{"type":"invalid_request_error","message":"  litellm.ContextWindowExceededError: opaque.\nModel=bedrock/anthropic-test, max_tokens=64000\n\nLiteLLM: No fallback was attempted."}}`, status: 400, typ: "invalid_request_error", overflow: true},
		{name: "combined context overflow", frame: `{"error":{"type":"invalid_request_error","message":"litellm.BadRequestError: AnthropicException - input length and max_tokens exceed context limit: 192926 + 64000 > 200000, decrease input length or max_tokens and try again"}}`, status: 400, typ: "invalid_request_error", overflow: true},
		{name: "specific code overrides prefix", frame: `{"error":{"code":"context_length_exceeded","message":"litellm.BadRequestError: opaque"}}`, status: 400, code: "context_length_exceeded", overflow: true},
		{name: "explicit status overrides retryable prefix", frame: `{"error":{"status_code":429,"message":"litellm.ServiceUnavailableError: opaque"}}`, status: 429},
		{name: "service class overrides coarse type", frame: `{"error":{"type":"invalid_request_error","message":"litellm.ServiceUnavailableError: opaque"}}`, status: 503, typ: "invalid_request_error", retry: true},
		{name: "unknown with appended class", frame: `{"error":{"message":"opaque\n\nLiteLLM: fallback failed with litellm.ServiceUnavailableError: context window exceeded"}}`},
		{name: "generic primary ignores fallback overflow", frame: `{"error":{"message":"litellm.BadRequestError: opaque\n\nLiteLLM: fallback failed with litellm.ContextWindowExceededError: context window exceeded"}}`, status: 400},
		// Synthetic combinations exercise LiteLLM's documented routing-history format.
		{name: "single newline fallback overflow", frame: `{"error":{"message":"litellm.BadRequestError: Missing required parameter: messages. Received Model Group=primary\nAvailable Model Group Fallbacks=['secondary']\nError doing the fallback: litellm.ContextWindowExceededError: context window exceeded"}}`, status: 400},
		{name: "fallback without routing group", frame: `{"error":{"message":"litellm.InvalidRequestError: Missing required parameter: messages.\r\nError doing the fallback: litellm.ContextWindowExceededError: context window exceeded"}}`, status: 400},
		{name: "context phrase in routing name", frame: `{"error":{"message":"litellm.BadRequestError: Missing required parameter. Received Model Group=context window\nAvailable Model Group Fallbacks=[]"}}`, status: 400},
		{name: "primary combined overflow before fallback", frame: `{"error":{"message":"litellm.BadRequestError: AnthropicException -\ninput length and max_tokens exceed context limit: 192926 + 64000 > 200000. Received Model Group=primary\nError doing the fallback: AuthenticationError: invalid key"}}`, status: 400, overflow: true},
		{name: "other provider routing words unchanged", frame: `{"error":{"type":"invalid_request_error","message":"OtherProvider: Received Model Group=primary\ninput length and max_tokens exceed context limit: 192926 + 64000 > 200000"}}`, status: 400, typ: "invalid_request_error", overflow: true},
		{name: "rate class ignores appended overflow", frame: `{"error":{"message":"litellm.RateLimitError: opaque\n\nLiteLLM: fallback failed with litellm.ContextWindowExceededError: context window exceeded"}}`, status: 429},
		{name: "unknown exception", frame: `{"error":{"message":"litellm.FutureError: opaque context window timeout"}}`},
		{name: "numeric status code", frame: `{"error":{"code":503,"message":"opaque"}}`, status: 503, code: "503", retry: true},
		{name: "string status code", frame: `{"error":{"code":"503","message":"opaque"}}`, status: 503, code: "503", retry: true},
		{name: "nested explicit status", frame: `{"error":{"status_code":502,"message":"opaque"}}`, status: 502, retry: true},
		{name: "envelope explicit status", frame: `{"status":"504","error":{"message":"opaque"}}`, status: 504, retry: true},
		{name: "overloaded type", frame: `{"error":{"type":"overloaded_error","message":"opaque"}}`, status: 529, typ: "overloaded_error", retry: true},
		{name: "server type", frame: `{"error":{"type":"server_error","message":"opaque"}}`, status: 500, typ: "server_error", retry: true},
		{name: "rate limit", frame: `{"error":{"type":"rate_limit_error","message":"try again"}}`, status: 429, typ: "rate_limit_error"},
		{name: "rate limit mentioning timeout", frame: `{"error":{"status_code":429,"type":"rate_limit_error","message":"rate limit exceeded; retry after the timeout"}}`, status: 429, typ: "rate_limit_error"},
		{name: "quota", frame: `{"error":{"code":"insufficient_quota","message":"try again"}}`, status: 429, code: "insufficient_quota"},
		{name: "authentication", frame: `{"error":{"type":"authentication_error","message":"opaque"}}`, status: 401, typ: "authentication_error"},
		{name: "permission", frame: `{"error":{"type":"permission_error","message":"opaque"}}`, status: 403, typ: "permission_error"},
		{name: "unsupported feature", frame: `{"error":{"code":"unsupported_feature","message":"opaque"}}`, status: 400, code: "unsupported_feature"},
		{name: "unknown", frame: `{"error":{"code":"new_error","message":"opaque"}}`, code: "new_error"},
		{name: "empty object", frame: `{"error":{}}`},
		{name: "string error", frame: `{"error":"opaque"}`},
	}
	for _, exception := range []struct {
		name   string
		status int
		retry  bool
	}{
		{"BadRequestError", 400, false},
		{"InvalidRequestError", 400, false},
		{"UnsupportedParamsError", 400, false},
		{"ContentPolicyViolationError", 400, false},
		{"ImageFetchError", 400, false},
		{"AuthenticationError", 401, false},
		{"PaymentRequiredError", 402, false},
		{"PermissionDeniedError", 403, false},
		{"NotFoundError", 404, false},
		{"Timeout", 408, true},
		{"UnprocessableEntityError", 422, false},
		{"RateLimitError", 429, false},
		{"BudgetExceededError", 429, false},
		{"APIConnectionError", 500, true},
		{"APIError", 500, true},
		{"InternalServerError", 500, true},
		{"ServiceUnavailableError", 503, true},
	} {
		tests = append(tests, struct {
			name, frame     string
			status          int
			code, typ       string
			overflow, retry bool
		}{name: exception.name, frame: `{"error":{"message":"litellm.` + exception.name + `: opaque"}}`, status: exception.status, retry: exception.retry})
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, prefix := range []string{"", "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"} {
				client := NewWithCompatibleAPIKey("test", "https://compatible.test/v1", "", "")
				stream := ": ping\n\n" + prefix + "data: " + tt.frame + "\n\ndata: [DONE]\n\n"
				var delivered strings.Builder
				result, err := client.parseCompletionsStreamWithMode(strings.NewReader(stream), func(s string) { delivered.WriteString(s) }, nil, true)
				var apiErr *APIError
				if result != nil || !errors.As(err, &apiErr) {
					t.Fatalf("result/error = %#v/%v", result, err)
				}
				if apiErr.StatusCode != tt.status || apiErr.Code != tt.code || apiErr.Type != tt.typ || apiErr.Message == "" {
					t.Fatalf("API error = %+v", apiErr)
				}
				if got := llmcontracts.ErrorIs(CategorizeCompatibleStreamError(err), llmcontracts.ErrorContextWindowExceeded); got != tt.overflow {
					t.Fatalf("overflow = %v: %v", got, err)
				}
				if got := httpretry.IsRetryableError(err); got != tt.retry {
					t.Fatalf("retry = %v: %v", got, err)
				}
				if tt.code == "unsupported_feature" && !llmcontracts.ErrorIs(CategorizeCompactionError(err), llmcontracts.ErrorNativeCompactionUnsupported) {
					t.Fatalf("unsupported classification lost: %v", err)
				}
				if prefix != "" && delivered.String() != "partial" {
					t.Fatalf("partial output lost: %q", delivered.String())
				}

				attempts := 0
				client.httpClient = &http.Client{Transport: completionsRoundTripFunc(func(*http.Request) (*http.Response, error) {
					attempts++
					body := stream
					if attempts > 1 {
						body = "data: {\"choices\":[{\"delta\":{\"content\":\"recovered\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
					}
					return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
				})}
				response, err := client.SendCompletions(retrytest.Context(), "test", &CompletionsOptions{DisableTools: true})
				if tt.retry {
					if err != nil || attempts != 2 || response.Text != "recovered" {
						t.Fatalf("retry result = %+v, %v; attempts %d", response, err, attempts)
					}
				} else if err == nil || attempts != 1 {
					t.Fatalf("expected failure without retry, got %v; attempts %d", err, attempts)
				}
			}
		})
	}
}

func TestCompletionsNullErrorIsNotFailure(t *testing.T) {
	result, err := (&Client{}).parseCompletionsStreamWithMode(strings.NewReader("data: {\"error\":null,\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"), nil, nil, true)
	if err != nil || result.text != "ok" {
		t.Fatalf("result/error = %+v/%v", result, err)
	}
}

func TestFirstPartyCompletionsDoesNotUseLiteLLMClassifier(t *testing.T) {
	for _, firstParty := range []bool{false, true} {
		attempts := 0
		client := NewWithCompatibleAPIKey("test", "https://compatible.test/v1", "", "")
		client.httpClient = &http.Client{Transport: completionsRoundTripFunc(func(*http.Request) (*http.Response, error) {
			attempts++
			body := "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			if attempts == 1 {
				body = "data: {\"error\":{\"message\":\"litellm.ServiceUnavailableError: opaque\"}}\n\n" + body
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
		})}
		response, err := client.SendCompletions(retrytest.Context(), "test", &CompletionsOptions{DisableTools: true, FirstPartyOpenAI: firstParty})
		wantAttempts := 2
		if firstParty {
			wantAttempts = 1 // Preserve native parsing; do not infer LiteLLM errors.
		}
		if err != nil || response.Text != "done" || attempts != wantAttempts {
			t.Fatalf("firstParty=%v: attempts=%d response=%+v err=%v", firstParty, attempts, response, err)
		}
	}
}
