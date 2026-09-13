package events

import (
	"sync"
	"testing"
)

type broadcasterCoreTestEvent struct {
	Value int
}

type broadcasterCoreTestSubscriber chan broadcasterCoreTestEvent

func TestBroadcasterCore_ScopedPublishOnlyOffersMatchingAndGlobalSubscribers(t *testing.T) {
	b := newBroadcaster[broadcasterCoreTestEvent, broadcasterCoreTestSubscriber](1)

	global, err := b.Subscribe()
	if err != nil {
		t.Fatalf("Subscribe global: %v", err)
	}
	matching, err := b.SubscribeScoped("project-1")
	if err != nil {
		t.Fatalf("SubscribeScoped matching: %v", err)
	}
	nonMatching, err := b.SubscribeScoped("project-2")
	if err != nil {
		t.Fatalf("SubscribeScoped nonmatching: %v", err)
	}
	defer b.Unsubscribe(global)
	defer b.Unsubscribe(matching)
	defer b.Unsubscribe(nonMatching)

	b.PublishScoped("project-1", broadcasterCoreTestEvent{Value: 1})

	for name, sub := range map[string]broadcasterCoreTestSubscriber{
		"global":   global,
		"matching": matching,
	} {
		select {
		case event := <-sub:
			if event.Value != 1 {
				t.Fatalf("%s event value = %d, want 1", name, event.Value)
			}
		default:
			t.Fatalf("%s subscriber did not receive matching scoped event", name)
		}
	}
	select {
	case event := <-nonMatching:
		t.Fatalf("nonmatching subscriber received event: %#v", event)
	default:
	}

	if got := b.DeliveryAttempts(); got != 2 {
		t.Fatalf("delivery attempts = %d, want 2", got)
	}
}

func TestBroadcasterCore_RepeatedScopedPublishesReuseSubscriberSnapshot(t *testing.T) {
	tests := []struct {
		name            string
		subscriberCount int
	}{
		{name: "one matching", subscriberCount: 1},
		{name: "five matching", subscriberCount: 5},
		{name: "fifty matching", subscriberCount: MaxSubscribers},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			b := newBroadcaster[broadcasterCoreTestEvent, broadcasterCoreTestSubscriber](1)
			subs := make([]broadcasterCoreTestSubscriber, 0, test.subscriberCount)
			for i := 0; i < test.subscriberCount; i++ {
				sub, err := b.SubscribeScoped("project-1")
				if err != nil {
					t.Fatalf("SubscribeScoped #%d: %v", i, err)
				}
				subs = append(subs, sub)
			}
			defer func() {
				for _, sub := range subs {
					b.Unsubscribe(sub)
				}
			}()

			allocs := testing.AllocsPerRun(100, func() {
				b.PublishScoped("project-1", broadcasterCoreTestEvent{Value: 1})
				for _, sub := range subs {
					<-sub
				}
			})
			if allocs != 0 {
				t.Fatalf("steady-state scoped publish allocations = %v, want 0", allocs)
			}
		})
	}
}

func TestBroadcasterCore_MembershipChangesRebuildSubscriberSnapshot(t *testing.T) {
	b := newBroadcaster[broadcasterCoreTestEvent, broadcasterCoreTestSubscriber](1)
	first, err := b.SubscribeScoped("project-1")
	if err != nil {
		t.Fatalf("SubscribeScoped first: %v", err)
	}
	defer b.Unsubscribe(first)

	b.PublishScoped("project-1", broadcasterCoreTestEvent{Value: 1})
	if got := (<-first).Value; got != 1 {
		t.Fatalf("first event value = %d, want 1", got)
	}

	second, err := b.SubscribeScoped("project-1")
	if err != nil {
		t.Fatalf("SubscribeScoped second: %v", err)
	}
	defer b.Unsubscribe(second)

	b.PublishScoped("project-1", broadcasterCoreTestEvent{Value: 2})
	for name, sub := range map[string]broadcasterCoreTestSubscriber{"first": first, "second": second} {
		if got := (<-sub).Value; got != 2 {
			t.Fatalf("%s event value = %d, want 2", name, got)
		}
	}

	b.Unsubscribe(first)
	b.PublishScoped("project-1", broadcasterCoreTestEvent{Value: 3})
	if got := (<-second).Value; got != 3 {
		t.Fatalf("second event value = %d, want 3", got)
	}
	select {
	case _, ok := <-first:
		if ok {
			t.Fatal("unsubscribed subscriber received an event")
		}
	default:
		t.Fatal("unsubscribed subscriber channel was not closed")
	}
}

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
