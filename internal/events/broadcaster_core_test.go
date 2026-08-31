package events

import (
	"sync"
	"testing"
)

type broadcasterCoreTestEvent struct {
	Value int
}

type broadcasterCoreTestSubscriber chan broadcasterCoreTestEvent

func TestBroadcasterCore_Lifecycle(t *testing.T) {
	b := newBroadcaster[broadcasterCoreTestEvent, broadcasterCoreTestSubscriber](2)

	sub, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	if got := cap(sub); got != 2 {
		t.Fatalf("subscriber capacity = %d, want 2", got)
	}

	b.Publish(broadcasterCoreTestEvent{Value: 1})
	b.Publish(broadcasterCoreTestEvent{Value: 2})
	b.Publish(broadcasterCoreTestEvent{Value: 3})
	if got := len(sub); got != 2 {
		t.Fatalf("buffered events = %d, want 2 after dropping a full-buffer event", got)
	}
	for want := 1; want <= 2; want++ {
		if got := (<-sub).Value; got != want {
			t.Errorf("received event value = %d, want %d", got, want)
		}
	}

	b.Unsubscribe(sub)
	b.Unsubscribe(sub)
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount after double unsubscribe = %d, want 0", got)
	}
}

func TestBroadcasterCore_MaxSubscribersAndRecovery(t *testing.T) {
	b := newBroadcaster[broadcasterCoreTestEvent, broadcasterCoreTestSubscriber](1)
	subs := make([]broadcasterCoreTestSubscriber, 0, MaxSubscribers)
	for i := 0; i < MaxSubscribers; i++ {
		sub, err := b.Subscribe()
		if err != nil {
			t.Fatalf("Subscribe #%d: %v", i, err)
		}
		subs = append(subs, sub)
	}

	if _, err := b.Subscribe(); err != ErrMaxSubscribers {
		t.Fatalf("Subscribe beyond limit error = %v, want %v", err, ErrMaxSubscribers)
	}

	b.Unsubscribe(subs[0])
	replacement, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe after freeing a slot: %v", err)
	}
	b.Unsubscribe(replacement)
	for _, sub := range subs[1:] {
		b.Unsubscribe(sub)
	}
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount after cleanup = %d, want 0", got)
	}
}

func TestBroadcasterCore_ConcurrentPublishUnsubscribe(t *testing.T) {
	b := newBroadcaster[broadcasterCoreTestEvent, broadcasterCoreTestSubscriber](10)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				b.Publish(broadcasterCoreTestEvent{Value: j})
			}
		}()
	}
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				sub, err := b.Subscribe()
				if err != nil {
					continue
				}
				select {
				case <-sub:
				default:
				}
				b.Unsubscribe(sub)
				b.Unsubscribe(sub)
			}
		}()
	}

	wg.Wait()
	if got := b.SubscriberCount(); got != 0 {
		t.Fatalf("SubscriberCount after concurrent lifecycle = %d, want 0", got)
	}
}

func TestTypedBroadcasters_UseConfiguredBufferCapacities(t *testing.T) {
	task := NewBroadcaster()
	taskSub, err := task.Subscribe()
	if err != nil {
		t.Fatalf("task Subscribe: %v", err)
	}
	defer task.Unsubscribe(taskSub)
	if got := cap(taskSub); got != 10 {
		t.Fatalf("task subscriber capacity = %d, want 10", got)
	}

	chat := NewChatBroadcaster()
	chatSub, err := chat.Subscribe()
	if err != nil {
		t.Fatalf("chat Subscribe: %v", err)
	}
	defer chat.Unsubscribe(chatSub)
	if got := cap(chatSub); got != 10 {
		t.Fatalf("chat subscriber capacity = %d, want 10", got)
	}

	fileChanges := NewFileChangeBroadcaster()
	fileSub, err := fileChanges.Subscribe()
	if err != nil {
		t.Fatalf("file-change Subscribe: %v", err)
	}
	defer fileChanges.Unsubscribe(fileSub)
	if got := cap(fileSub); got != 50 {
		t.Fatalf("file-change subscriber capacity = %d, want 50", got)
	}
}
