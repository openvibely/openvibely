package openaiclient

import (
	"context"
	"math"
	"testing"
)

func TestCompletionsOptions_DefaultMaxTurnsNoLimit(t *testing.T) {
	client := NewWithAPIKey("test-key")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	opts := &CompletionsOptions{}
	_, err := client.SendCompletions(ctx, "test", opts)
	if err == nil {
		t.Fatal("expected error from canceled context, got nil")
	}

	if opts.MaxTurns != math.MaxInt32 {
		t.Fatalf("expected MaxTurns default to no limit (%d), got %d", math.MaxInt32, opts.MaxTurns)
	}
}

func TestCompletionsTurnResult_Structure(t *testing.T) {
	// Just a compilation test
	result := &completionsTurnResult{
		text:        "test",
		stopReason:  "stop",
		model:       "gpt-4",
		inputTokens: 10,
	}

	if result.text != "test" {
		t.Errorf("expected text=test, got %s", result.text)
	}
}
