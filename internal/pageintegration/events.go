package pageintegration

import (
	"sync"
	"time"
)

const subscriberBuffer = 32

// Hub fans out refresh events per page instance. Closing an instance ends
// every subscription so revoked or disabled pages stop receiving data.
type Hub struct {
	mu   sync.Mutex
	subs map[string]map[chan Event]struct{}
	max  int
}

func NewHub() *Hub { return &Hub{subs: map[string]map[chan Event]struct{}{}, max: 64} }

// Subscribe returns a channel closed by unsubscribe or CloseInstance.
func (h *Hub) Subscribe(instanceID string) (<-chan Event, func(), bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	set := h.subs[instanceID]
	if set == nil {
		set = map[chan Event]struct{}{}
		h.subs[instanceID] = set
	}
	if len(set) >= h.max {
		return nil, func() {}, false
	}
	ch := make(chan Event, subscriberBuffer)
	set[ch] = struct{}{}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if s := h.subs[instanceID]; s != nil {
				if _, ok := s[ch]; ok {
					delete(s, ch)
					close(ch)
				}
			}
		})
	}, true
}

// Publish delivers without blocking; slow subscribers drop events and re-sync on the next one.
func (h *Hub) Publish(instanceID string, ev Event) {
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[instanceID] {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (h *Hub) CloseInstance(instanceID string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs[instanceID] {
		close(ch)
	}
	delete(h.subs, instanceID)
}

func (h *Hub) SubscriberCount(instanceID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs[instanceID])
}
