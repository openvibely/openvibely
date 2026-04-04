package retry

import (
	"context"
	"errors"
	"testing"
)

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		err      error
		expected bool
	}{
		{errors.New("429 too many requests"), true},
		{errors.New("connection reset by peer"), true},
		{errors.New("503 unavailable"), true},
		{context.Canceled, false},
		{errors.New("validation failed"), false},
	}
	for _, tc := range cases {
		if got := IsRetryable(tc.err); got != tc.expected {
			t.Fatalf("IsRetryable(%v)=%v want %v", tc.err, got, tc.expected)
		}
	}
}

func TestDo_RetriesThenSucceeds(t *testing.T) {
	attempts := 0
	res, err := Do(context.Background(), Policy{MaxAttempts: 3, BaseDelay: 1}, func() (string, error) {
		attempts++
		if attempts < 2 {
			return "", errors.New("503 unavailable")
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatalf("Do error: %v", err)
	}
	if res != "ok" {
		t.Fatalf("expected ok, got %q", res)
	}
	if attempts != 2 {
		t.Fatalf("expected 2 attempts, got %d", attempts)
	}
}
