package openaiclient

import (
	"errors"
	"fmt"
	"testing"
)

func TestAPIError(t *testing.T) {
	t.Run("error string formatting", func(t *testing.T) {
		tests := []struct {
			name string
			err  *APIError
			want string
		}{
			{
				name: "with code",
				err:  &APIError{StatusCode: 400, Code: "invalid_request", Message: "bad input"},
				want: "openai api error (status 400, code invalid_request): bad input",
			},
			{
				name: "without code",
				err:  &APIError{StatusCode: 500, Message: "internal error"},
				want: "openai api error (status 500): internal error",
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := tt.err.Error(); got != tt.want {
					t.Errorf("got %q, want %q", got, tt.want)
				}
			})
		}
	})

	t.Run("error matching", func(t *testing.T) {
		tests := []struct {
			name   string
			err    *APIError
			target error
			match  bool
		}{
			{
				name:   "401 matches ErrNoAuth",
				err:    &APIError{StatusCode: 401, Message: "unauthorized"},
				target: ErrNoAuth,
				match:  true,
			},
			{
				name:   "403 matches ErrNoAuth",
				err:    &APIError{StatusCode: 403, Message: "forbidden"},
				target: ErrNoAuth,
				match:  true,
			},
			{
				name:   "429 matches ErrRateLimited",
				err:    &APIError{StatusCode: 429, Message: "rate limit"},
				target: ErrRateLimited,
				match:  true,
			},
			{
				name:   "404 with model message matches ErrModelNotFound",
				err:    &APIError{StatusCode: 404, Message: "model gpt-5 not found"},
				target: ErrModelNotFound,
				match:  true,
			},
			{
				name:   "404 without model message does not match ErrModelNotFound",
				err:    &APIError{StatusCode: 404, Message: "endpoint not found"},
				target: ErrModelNotFound,
				match:  false,
			},
			{
				name:   "context length exceeded code",
				err:    &APIError{StatusCode: 400, Code: "context_length_exceeded", Message: "too long"},
				target: ErrContextLengthExceeded,
				match:  true,
			},
			{
				name:   "context length in message",
				err:    &APIError{StatusCode: 400, Message: "request exceeds context length limit"},
				target: ErrContextLengthExceeded,
				match:  true,
			},
			{
				name:   "insufficient quota code",
				err:    &APIError{StatusCode: 402, Code: "insufficient_quota", Message: "no credits"},
				target: ErrQuotaExceeded,
				match:  true,
			},
			{
				name:   "quota in message",
				err:    &APIError{StatusCode: 402, Message: "quota exceeded for organization"},
				target: ErrQuotaExceeded,
				match:  true,
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				got := errors.Is(tt.err, tt.target)
				if got != tt.match {
					t.Errorf("errors.Is(%v, %v) = %v, want %v", tt.err, tt.target, got, tt.match)
				}
			})
		}
	})

	t.Run("temporary status", func(t *testing.T) {
		tests := []struct {
			statusCode int
			temporary  bool
		}{
			{429, true},
			{500, true},
			{502, true},
			{503, true},
			{529, true},
			{400, false},
			{401, false},
			{403, false},
			{404, false},
		}

		for _, tt := range tests {
			err := &APIError{StatusCode: tt.statusCode}
			if got := err.Temporary(); got != tt.temporary {
				t.Errorf("status %d: Temporary() = %v, want %v", tt.statusCode, got, tt.temporary)
			}
		}
	})
}

func TestParseAPIError(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       []byte
		wantErr    *APIError
	}{
		{
			name:       "empty body",
			statusCode: 500,
			body:       []byte{},
			wantErr:    &APIError{StatusCode: 500, Message: "empty response body"},
		},
		{
			name:       "json error response",
			statusCode: 400,
			body:       []byte(`{"error": {"type": "invalid_request", "message": "bad input", "code": "invalid_request"}}`),
			wantErr:    &APIError{StatusCode: 400, Type: "invalid_request", Message: "bad input", Code: "invalid_request"},
		},
		{
			name:       "plain text error",
			statusCode: 500,
			body:       []byte("Internal server error"),
			wantErr:    &APIError{StatusCode: 500, Message: "Internal server error"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := parseAPIError(tt.statusCode, tt.body)
			apiErr, ok := err.(*APIError)
			if !ok {
				t.Fatalf("expected *APIError, got %T", err)
			}

			if apiErr.StatusCode != tt.wantErr.StatusCode {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tt.wantErr.StatusCode)
			}
			if apiErr.Message != tt.wantErr.Message {
				t.Errorf("Message = %q, want %q", apiErr.Message, tt.wantErr.Message)
			}
			if tt.wantErr.Code != "" && apiErr.Code != tt.wantErr.Code {
				t.Errorf("Code = %q, want %q", apiErr.Code, tt.wantErr.Code)
			}
			if tt.wantErr.Type != "" && apiErr.Type != tt.wantErr.Type {
				t.Errorf("Type = %q, want %q", apiErr.Type, tt.wantErr.Type)
			}
		})
	}
}

func TestIsNetworkError(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		isNetwork bool
	}{
		{
			name:      "nil error",
			err:       nil,
			isNetwork: false,
		},
		{
			name:      "net.Error timeout",
			err:       &netTimeoutError{},
			isNetwork: true,
		},
		{
			name:      "connection refused string",
			err:       fmt.Errorf("dial tcp 127.0.0.1:8080: connection refused"),
			isNetwork: true,
		},
		{
			name:      "no such host",
			err:       fmt.Errorf("lookup api.openai.com: no such host"),
			isNetwork: true,
		},
		{
			name:      "timeout string",
			err:       fmt.Errorf("request timeout after 30s"),
			isNetwork: true,
		},
		{
			name:      "TLS handshake",
			err:       fmt.Errorf("TLS handshake failed"),
			isNetwork: true,
		},
		{
			name:      "EOF error",
			err:       fmt.Errorf("unexpected EOF"),
			isNetwork: true,
		},
		{
			name:      "regular error",
			err:       fmt.Errorf("invalid request"),
			isNetwork: false,
		},
		{
			name:      "api error",
			err:       &APIError{StatusCode: 400, Message: "bad request"},
			isNetwork: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNetworkError(tt.err); got != tt.isNetwork {
				t.Errorf("isNetworkError(%v) = %v, want %v", tt.err, got, tt.isNetwork)
			}
		})
	}
}

func TestWrapNetworkError(t *testing.T) {
	t.Run("nil error", func(t *testing.T) {
		if got := wrapNetworkError(nil); got != nil {
			t.Errorf("wrapNetworkError(nil) = %v, want nil", got)
		}
	})

	t.Run("non-network error", func(t *testing.T) {
		err := fmt.Errorf("regular error")
		if got := wrapNetworkError(err); got != err {
			t.Errorf("wrapNetworkError(regular) should return same error")
		}
	})

	t.Run("timeout error", func(t *testing.T) {
		err := &netTimeoutError{}
		wrapped := wrapNetworkError(err)
		if !errors.Is(wrapped, ErrTimeout) {
			t.Errorf("wrapped timeout should match ErrTimeout")
		}
		if !errors.Is(wrapped, err) {
			t.Errorf("wrapped error should still match original")
		}
	})

	t.Run("connection error", func(t *testing.T) {
		err := fmt.Errorf("dial tcp: connection refused")
		wrapped := wrapNetworkError(err)
		if !errors.Is(wrapped, ErrNetworkError) {
			t.Errorf("wrapped connection error should match ErrNetworkError")
		}
	})
}

// netTimeoutError implements net.Error for testing
type netTimeoutError struct{}

func (e *netTimeoutError) Error() string   { return "timeout" }
func (e *netTimeoutError) Timeout() bool   { return true }
func (e *netTimeoutError) Temporary() bool { return true }