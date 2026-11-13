package pageplugins

import (
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func issueTestHandle(t *testing.T, m *Manager, project string) Handle {
	t.Helper()
	h, err := m.IssueHandle(&Resolved{Instance: models.PagePluginInstance{ID: project + "-page", ProjectID: project}}, "user")
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func requireRetiredHandle(t *testing.T, m *Manager, h Handle) {
	t.Helper()
	if _, ok := m.LookupHandle(h.Token); ok {
		t.Error("retired handle is usable")
	}
	select {
	case <-h.Done():
	default:
		t.Error("retired handle's subscriptions remain open")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.handles[h.Token]; ok {
		t.Error("retired handle retained in map")
	}
}

func TestHandleExpiryBeforeTimer(t *testing.T) {
	m := NewManager(nil, "")
	h := issueTestHandle(t, m, "project")
	// Prevent timer execution: lookup must enforce expiry independently.
	h.timer.Stop()
	m.now = func() time.Time { return h.ExpiresAt }
	requireRetiredHandle(t, m, h)
}

func TestHandleRetirementAndProjectRelease(t *testing.T) {
	m := NewManager(nil, "")
	first := issueTestHandle(t, m, "first")
	second := issueTestHandle(t, m, "first")
	other := issueTestHandle(t, m, "other")
	defer m.RetireHandle(other.Token)
	m.RetireHandle(first.Token)
	m.RetireHandle(first.Token)
	requireRetiredHandle(t, m, first)
	m.ReleaseProject("first")
	requireRetiredHandle(t, m, second)
	if _, ok := m.LookupHandle(other.Token); !ok {
		t.Error("release retired unrelated project handle")
	}
}

func TestHandleDelayedTimerCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(nil, "")
		h := issueTestHandle(t, m, "project")
		// Hold the manager lock through expiry to delay the real timer callback.
		m.mu.Lock()
		time.Sleep(HandleTTL)
		select {
		case <-h.Done():
			t.Error("timer retired handle without manager lock")
		default:
		}
		m.mu.Unlock()
		synctest.Wait()
		// No lookup or further issuance is needed to reclaim the expired entry.
		m.mu.Lock()
		_, retained := m.handles[h.Token]
		m.mu.Unlock()
		if retained {
			t.Fatal("delayed timer did not reclaim handle")
		}
		requireRetiredHandle(t, m, h)
	})
}

func TestHandleTimerLookupRetirementRaces(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		m := NewManager(nil, "")
		handles := make([]Handle, 100)
		for i := range handles {
			handles[i] = issueTestHandle(t, m, "expired")
		}
		m.mu.Lock()
		time.Sleep(HandleTTL)
		var wg sync.WaitGroup
		for _, h := range handles {
			wg.Add(3)
			go func() { defer wg.Done(); requireRetiredHandle(t, m, h) }()
			go func() { defer wg.Done(); m.RetireHandle(h.Token) }()
			go func() {
				defer wg.Done()
				fresh := issueTestHandle(t, m, "fresh")
				if _, ok := m.LookupHandle(fresh.Token); !ok {
					t.Error("fresh handle unavailable")
				}
				m.RetireHandle(fresh.Token)
			}()
		}
		wg.Add(1)
		go func() { defer wg.Done(); m.ReleaseProject("expired") }()
		m.mu.Unlock()
		wg.Wait()
		synctest.Wait()
		for _, h := range handles {
			requireRetiredHandle(t, m, h)
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if len(m.handles) != 0 {
			t.Errorf("%d handles remain after cleanup", len(m.handles))
		}
	})
}
