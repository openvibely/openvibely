package events

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
)

const executionStreamPublishBatchSize = 64

// benchmarkExecutionStreamPublish measures steady-state Publish fan-out while
// deterministically keeping every subscriber channel drained. Each timed batch
// is smaller than the subscriber channel capacity. After the batch, timing is
// stopped and every expected delivery is synchronously consumed before the
// next batch starts. A dropped delivery therefore fails the benchmark instead
// of silently producing a faster result.
func benchmarkExecutionStreamPublish(b *testing.B, subscribers int) {
	hub := NewExecutionStreamHub()
	subs := make([]ExecutionStreamSubscriber, 0, subscribers)
	unsubs := make([]func(), 0, subscribers)
	for i := 0; i < subscribers; i++ {
		sub, unsub, err := hub.Subscribe("exec")
		if err != nil {
			b.Fatalf("subscribe %d: %v", i, err)
		}
		subs = append(subs, sub)
		unsubs = append(unsubs, unsub)
	}
	event := ExecutionStreamEvent{ExecID: "exec", Type: ExecutionStreamDelta, Delta: "token", Offset: 1}

	// Build the lazy snapshot before measurement and restore drained channels.
	hub.Publish(event)
	for _, sub := range subs {
		<-sub
	}

	b.ReportAllocs()
	b.ResetTimer()
	published := 0
	for published < b.N {
		batchSize := min(executionStreamPublishBatchSize, b.N-published)
		for i := 0; i < batchSize; i++ {
			hub.Publish(event)
		}
		published += batchSize

		b.StopTimer()
		for subscriber, sub := range subs {
			for i := 0; i < batchSize; i++ {
				select {
				case <-sub:
				default:
					b.Fatalf("subscriber %d received %d/%d deliveries; Publish entered the drop path", subscriber, i, batchSize)
				}
			}
		}
		if published < b.N {
			b.StartTimer()
		}
	}

	for _, unsub := range unsubs {
		unsub()
	}
}

func BenchmarkExecutionStreamPublish1Subscriber(b *testing.B) {
	benchmarkExecutionStreamPublish(b, 1)
}

func BenchmarkExecutionStreamPublish50Subscribers(b *testing.B) {
	benchmarkExecutionStreamPublish(b, 50)
}

// BenchmarkExecutionStreamSubscribeUnsubscribeChurn measures the connect/
// disconnect path so that the copy-on-write snapshot optimization does not
// merely shift unbounded cost onto SSE connection churn.
func BenchmarkExecutionStreamSubscribeUnsubscribeChurn(b *testing.B) {
	hub := NewExecutionStreamHub()
	// Hold a steady baseline of subscribers so each churned subscribe/unsubscribe
	// rebuilds a non-trivial snapshot, matching a busy execution.
	const baseline = 10
	baseUnsubs := make([]func(), 0, baseline)
	for i := 0; i < baseline; i++ {
		_, unsub, err := hub.Subscribe("exec")
		if err != nil {
			b.Fatalf("baseline subscribe %d: %v", i, err)
		}
		baseUnsubs = append(baseUnsubs, unsub)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, unsub, err := hub.Subscribe("exec")
		if err != nil {
			b.Fatalf("churn subscribe: %v", err)
		}
		unsub()
	}
	b.StopTimer()

	for _, unsub := range baseUnsubs {
		unsub()
	}
}

// TestExecutionStreamHubPublishZeroAllocations asserts that steady-state
// Publish performs no heap allocations for both the 1- and 50-subscriber
// fixtures, guarding against reintroducing a per-chunk snapshot allocation.
func TestExecutionStreamHubPublishZeroAllocations(t *testing.T) {
	for _, subscribers := range []int{1, 50} {
		subscribers := subscribers
		t.Run(fmt.Sprintf("%d-subscribers", subscribers), func(t *testing.T) {
			hub := NewExecutionStreamHub()
			subs := make([]ExecutionStreamSubscriber, 0, subscribers)
			unsubs := make([]func(), 0, subscribers)
			for i := 0; i < subscribers; i++ {
				sub, unsub, err := hub.Subscribe("exec")
				if err != nil {
					t.Fatalf("subscribe %d: %v", i, err)
				}
				subs = append(subs, sub)
				unsubs = append(unsubs, unsub)
			}
			defer func() {
				for _, unsub := range unsubs {
					unsub()
				}
			}()
			event := ExecutionStreamEvent{ExecID: "exec", Type: ExecutionStreamDelta, Delta: "token", Offset: 1}

			allocs := testing.AllocsPerRun(1000, func() {
				hub.Publish(event)
				// Drain inline so channels never fill and the drop path never runs.
				for _, sub := range subs {
					select {
					case <-sub:
					default:
					}
				}
			})
			if allocs != 0 {
				t.Fatalf("expected 0 allocations per Publish for %d subscribers, got %v", subscribers, allocs)
			}
		})
	}
}

// TestExecutionStreamHubConcurrentPublishUnsubscribeClose exercises overlapping
// Publish/Unsubscribe/Close to catch send-on-closed-channel panics and stale
// snapshot races under the race detector.
func TestExecutionStreamHubConcurrentPublishUnsubscribeClose(t *testing.T) {
	hub := NewExecutionStreamHub()
	const workers = 8
	var wg sync.WaitGroup
	var stop atomic.Bool

	// Publishers keep publishing to a set of executions.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				exec := fmt.Sprintf("exec-%d", i%4)
				hub.Publish(ExecutionStreamEvent{ExecID: exec, Type: ExecutionStreamDelta, Delta: "x", Offset: i})
			}
		}(w)
	}

	// Subscribers churn subscribe/unsubscribe and drain.
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				exec := fmt.Sprintf("exec-%d", i%4)
				sub, unsub, err := hub.Subscribe(exec)
				if err != nil {
					continue
				}
				select {
				case <-sub:
				default:
				}
				unsub()
			}
		}(w)
	}

	// Closers periodically terminate executions.
	for w := 0; w < 2; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; !stop.Load(); i++ {
				exec := fmt.Sprintf("exec-%d", i%4)
				hub.Close(exec, ExecutionStreamEvent{Type: ExecutionStreamDone, Status: "completed"})
			}
		}(w)
	}

	// Let the goroutines interleave for a bounded number of iterations.
	for i := 0; i < 20000; i++ {
		hub.Publish(ExecutionStreamEvent{ExecID: "exec-0", Type: ExecutionStreamDelta, Delta: "y", Offset: i})
	}
	stop.Store(true)
	wg.Wait()
}
