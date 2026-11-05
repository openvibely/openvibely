package pageplugins

import (
	"errors"
	"time"
)

const StartupTimeout = 30 * time.Second

// StartupStatus is an observation of the latest browser load, not a functional
// test of the page. Reports are ephemeral and reset when OV restarts.
type StartupStatus struct {
	State       string    `json:"state"`
	Error       string    `json:"error,omitempty"`
	ContentHash string    `json:"content_hash"`
	ObservedAt  time.Time `json:"observed_at,omitempty"`
	handle      string
}

func (m *Manager) StartupStatus(instanceID, contentHash string) StartupStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.startups[instanceID]
	if !ok || s.ContentHash != contentHash {
		return StartupStatus{State: "not_opened", ContentHash: contentHash}
	}
	if s.State == "loading" && m.now().Sub(s.ObservedAt) >= StartupTimeout {
		s.State = "failed"
		s.Error = "Page did not confirm startup within 30 seconds."
		m.startups[instanceID] = s
	}
	return s
}

// ReportStartup is called only after request/handle authorization. Older tabs
// cannot overwrite the latest load's result; failures cannot be hidden by a late
// success message from the same load.
func (m *Manager) ReportStartup(token, state, detail string) error {
	if state != "loaded" && state != "failed" {
		return errors.New("invalid startup state")
	}
	if len(detail) > 2048 {
		return errors.New("startup error exceeds 2048 bytes")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	hd, ok := m.handles[token]
	if !ok || !m.now().Before(hd.ExpiresAt) {
		return ErrNotFound
	}
	s, ok := m.startups[hd.InstanceID]
	if !ok || s.handle != token {
		return nil
	}
	if s.State == "loading" && m.now().Sub(s.ObservedAt) >= StartupTimeout {
		s.State = "failed"
		s.Error = "Page did not confirm startup within 30 seconds."
		m.startups[hd.InstanceID] = s
	}
	if s.State == "failed" {
		return nil
	}
	s.State = state
	s.Error = detail
	s.ObservedAt = m.now().UTC()
	if state == "loaded" {
		s.Error = ""
	}
	m.startups[hd.InstanceID] = s
	return nil
}
