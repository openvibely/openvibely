package preparedhistory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestTransferConsumedOnceAndReleased(t *testing.T) {
	ctx := WithTransfer(context.Background(), []string{"prepared"})
	var received atomic.Int32
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, ok := TakeTransfer[[]string](ctx)
			if ok {
				if len(value) != 1 || value[0] != "prepared" {
					t.Error(value)
				}
				received.Add(1)
			}
		}()
	}
	wg.Wait()
	if received.Load() != 1 {
		t.Fatal(received.Load())
	}
	slot := ctx.Value(transferKey[[]string]{}).(*transfer[[]string])
	if slot.value != nil || slot.present {
		t.Fatal("consumed history retained")
	}
	if _, ok := TakeTransfer[[]byte](ctx); ok {
		t.Fatal("wrong type consumed")
	}
}
