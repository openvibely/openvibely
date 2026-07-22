package contracts

import (
	"context"
	"testing"
)

func TestNotifyActivity(t *testing.T) {
	called := 0
	ctx := WithActivityCallback(context.Background(), func() { called++ })

	NotifyActivity(ctx)

	if called != 1 {
		t.Fatalf("activity callback calls = %d, want 1", called)
	}
}

func TestNotifyActivityWithoutCallback(t *testing.T) {
	NotifyActivity(nil)
	NotifyActivity(context.Background())
}
