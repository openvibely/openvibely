package contracts

import (
	"context"
	"testing"
)

func TestLifecycleCompletionUserMessageContext(t *testing.T) {
	if _, ok := LifecycleCompletionUserMessageFromContext(context.Background()); ok {
		t.Fatal("unexpected lifecycle completion user message")
	}
	ctx := WithLifecycleCompletionUserMessage(context.Background(), "latest steering")
	if got, ok := LifecycleCompletionUserMessageFromContext(ctx); !ok || got != "latest steering" {
		t.Fatalf("lifecycle completion user message = %q, %v", got, ok)
	}
}
