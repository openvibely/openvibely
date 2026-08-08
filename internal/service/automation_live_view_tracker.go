package service

import (
	"sync"
	"time"
)

// AutomationLiveViewTracker records which Automations currently have their
// Live/Preview page open in a browser, based on recent page loads and the
// page's own htmx background poll (every 20s while open). It lets background
// jobs like the external GitHub state refresh scope their work to automations
// someone is actually looking at, instead of scanning every automation in
// every project.
type AutomationLiveViewTracker struct {
	mu     sync.Mutex
	seenAt map[string]time.Time
}

func NewAutomationLiveViewTracker() *AutomationLiveViewTracker {
	return &AutomationLiveViewTracker{seenAt: make(map[string]time.Time)}
}

func automationLiveViewKey(projectID, automationID string) string {
	return projectID + "|" + automationID
}

// MarkViewed records that the given Automation's Live page was just loaded or
// polled.
func (t *AutomationLiveViewTracker) MarkViewed(projectID, automationID string) {
	if t == nil || projectID == "" || automationID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.seenAt[automationLiveViewKey(projectID, automationID)] = time.Now().UTC()
}

// IsRecentlyViewed reports whether the given Automation's Live page was seen
// within the supplied window.
func (t *AutomationLiveViewTracker) IsRecentlyViewed(projectID, automationID string, within time.Duration) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	seen, ok := t.seenAt[automationLiveViewKey(projectID, automationID)]
	if !ok {
		return false
	}
	if time.Since(seen) > within {
		delete(t.seenAt, automationLiveViewKey(projectID, automationID))
		return false
	}
	return true
}
