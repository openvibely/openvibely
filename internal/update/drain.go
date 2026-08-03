// Package update owns application update state and workload quiescing.
package update

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	DrainStateIdle     = "idle"
	DrainStateDraining = "draining"
	DrainStateReady    = "ready"
)

var ErrDraining = errors.New("OpenVibely is preparing for an update; retry later")

type ActiveWork struct {
	TaskExecutions       int `json:"task_executions"`
	ChatExecutions       int `json:"chat_executions"`
	WorkflowExecutions   int `json:"workflow_executions"`
	AutomationActivities int `json:"automation_activities"`
}

func (a ActiveWork) Total() int {
	return a.TaskExecutions + a.ChatExecutions + a.WorkflowExecutions + a.AutomationActivities
}

type WorkClass string

const (
	WorkTask       WorkClass = "task"
	WorkChat       WorkClass = "chat"
	WorkWorkflow   WorkClass = "workflow"
	WorkAutomation WorkClass = "automation"
)

type WorkTracker struct {
	mu     sync.Mutex
	open   bool
	active ActiveWork
}

func NewWorkTracker() *WorkTracker { return &WorkTracker{open: true} }

func (t *WorkTracker) Start(class WorkClass) (func(), error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.open {
		return nil, ErrDraining
	}
	switch class {
	case WorkTask:
		t.active.TaskExecutions++
	case WorkChat:
		t.active.ChatExecutions++
	case WorkWorkflow:
		t.active.WorkflowExecutions++
	case WorkAutomation:
		t.active.AutomationActivities++
	default:
		return nil, errors.New("unknown active work class")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			t.mu.Lock()
			defer t.mu.Unlock()
			switch class {
			case WorkTask:
				t.active.TaskExecutions--
			case WorkChat:
				t.active.ChatExecutions--
			case WorkWorkflow:
				t.active.WorkflowExecutions--
			case WorkAutomation:
				t.active.AutomationActivities--
			}
		})
	}, nil
}

func (t *WorkTracker) Active() ActiveWork {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.active
}
func (t *WorkTracker) Open()       { t.mu.Lock(); t.open = true; t.mu.Unlock() }
func (t *WorkTracker) Close()      { t.mu.Lock(); t.open = false; t.mu.Unlock() }
func (t *WorkTracker) Admit() bool { t.mu.Lock(); defer t.mu.Unlock(); return t.open }

type DrainRequest struct{ Lease time.Duration }

type DrainStatus struct {
	State       string     `json:"state"`
	Generation  string     `json:"generation,omitempty"`
	ExpiresAt   time.Time  `json:"expires_at,omitempty"`
	Active      ActiveWork `json:"active"`
	QueuedTotal int        `json:"queued_total"`
}

type DrainManager struct {
	mu                 sync.Mutex
	active             func() ActiveWork
	queued             func() int
	quietInterval      time.Duration
	now                func() time.Time
	state              string
	generation         string
	expiresAt          time.Time
	zeroSince          time.Time
	owned              bool
	persistence        string
	stateWriter        func(string, []byte) error
	tracker            *WorkTracker
	reopened           chan struct{}
	supervisorInterval time.Duration
	expiryOnce         sync.Once
}

func NewDrainManager(active func() ActiveWork, queued func() int, quiet time.Duration, now func() time.Time) *DrainManager {
	if active == nil {
		active = func() ActiveWork { return ActiveWork{} }
	}
	if queued == nil {
		queued = func() int { return 0 }
	}
	if now == nil {
		now = time.Now
	}
	return &DrainManager{active: active, queued: queued, quietInterval: quiet, now: now, state: DrainStateIdle, reopened: make(chan struct{}, 1)}
}

// Reopened reports successful transitions back to idle so durable queued work
// can be offered for admission again. Signals are coalesced because recovery
// scans all recoverable queues.
func (m *DrainManager) Reopened() <-chan struct{} { return m.reopened }

// StartExpirySupervisor autonomously reconciles persisted unowned drains. This
// is required because some execution ingresses consult WorkTracker directly and
// therefore cannot be relied on to call Status or Admit after a restart.
func (m *DrainManager) StartExpirySupervisor(ctx context.Context) {
	if ctx == nil {
		return
	}
	m.expiryOnce.Do(func() {
		go func() {
			interval := m.supervisorInterval
			if interval <= 0 {
				interval = 250 * time.Millisecond
			}
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				m.mu.Lock()
				m.expireLocked()
				m.mu.Unlock()
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
				}
			}
		}()
	})
}

func (m *DrainManager) SetWorkTracker(tracker *WorkTracker) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.tracker = tracker
	if tracker != nil && m.state != DrainStateIdle {
		tracker.Close()
	}
}

func (m *DrainManager) SetPersistence(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.persistence = path
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var state struct {
		State      string    `json:"state"`
		Generation string    `json:"generation"`
		ExpiresAt  time.Time `json:"expires_at"`
		ZeroSince  time.Time `json:"zero_since"`
		Owned      bool      `json:"owned"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return fmt.Errorf("decode persisted drain state: %w", err)
	}
	if state.State != DrainStateDraining && state.State != DrainStateReady {
		return nil
	}
	m.state, m.generation, m.expiresAt, m.zeroSince, m.owned = state.State, state.Generation, state.ExpiresAt, state.ZeroSince, state.Owned
	m.expireLocked()
	if m.state != DrainStateIdle && m.tracker != nil {
		m.tracker.Close()
	}
	return nil
}

func atomicWriteState(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func (m *DrainManager) persistLocked() error {
	if m.persistence == "" {
		return nil
	}
	data, err := json.Marshal(struct {
		State      string    `json:"state"`
		Generation string    `json:"generation,omitempty"`
		ExpiresAt  time.Time `json:"expires_at,omitempty"`
		ZeroSince  time.Time `json:"zero_since,omitempty"`
		Owned      bool      `json:"owned,omitempty"`
	}{m.state, m.generation, m.expiresAt, m.zeroSince, m.owned})
	if err != nil {
		return err
	}
	writer := m.stateWriter
	if writer == nil {
		writer = atomicWriteState
	}
	return writer(m.persistence, data)
}

func (m *DrainManager) BeginDrain(req DrainRequest) (DrainStatus, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if req.Lease <= 0 {
		return DrainStatus{}, errors.New("positive drain lease required")
	}
	if m.state != DrainStateIdle {
		return DrainStatus{}, errors.New("update drain is already active")
	}
	m.state = DrainStateDraining // admission closes before the active snapshot below
	if m.tracker != nil {
		m.tracker.Close()
	}
	m.generation = randomGeneration()
	m.expiresAt = m.now().Add(req.Lease)
	m.zeroSince = time.Time{}
	m.owned = false
	if err := m.persistLocked(); err != nil {
		m.state, m.generation, m.expiresAt, m.zeroSince, m.owned = DrainStateIdle, "", time.Time{}, time.Time{}, false
		if m.tracker != nil {
			m.tracker.Open()
		}
		return DrainStatus{}, errors.Join(ErrUpdateRetryable, err)
	}
	return m.statusLocked(), nil
}

func (m *DrainManager) Admit() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	return m.state == DrainStateIdle
}

func (m *DrainManager) Status() DrainStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.expireLocked()
	return m.statusLocked()
}

func (m *DrainManager) statusLocked() DrainStatus {
	oldState, oldZeroSince := m.state, m.zeroSince
	active := m.active()
	if m.state != DrainStateIdle {
		if active.Total() != 0 {
			m.state = DrainStateDraining
			m.zeroSince = time.Time{}
		} else if m.zeroSince.IsZero() {
			m.zeroSince = m.now()
		} else if m.now().Sub(m.zeroSince) >= m.quietInterval {
			m.state = DrainStateReady
		}
	}
	if m.state != oldState || !m.zeroSince.Equal(oldZeroSince) {
		if err := m.persistLocked(); err != nil {
			m.state, m.zeroSince = oldState, oldZeroSince
		}
	}
	return DrainStatus{State: m.state, Generation: m.generation, ExpiresAt: m.expiresAt, Active: active, QueuedTotal: m.queued()}
}

func (m *DrainManager) TakeOwnership(generation string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation == "" || generation != m.generation || m.state == DrainStateIdle || m.owned {
		return false
	}
	m.owned = true
	if err := m.persistLocked(); err != nil {
		m.owned = false
		return false
	}
	return true
}

func (m *DrainManager) Owns(generation string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return generation != "" && generation == m.generation && m.state != DrainStateIdle && m.owned
}

func (m *DrainManager) Renew(generation string, lease time.Duration) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation == "" || generation != m.generation || lease <= 0 || m.state == DrainStateIdle {
		return false
	}
	previous := m.expiresAt
	m.expiresAt = m.now().Add(lease)
	if err := m.persistLocked(); err != nil {
		m.expiresAt = previous
		return false
	}
	return true
}

func (m *DrainManager) Release(generation string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation == "" || generation != m.generation || m.state == DrainStateIdle {
		return false
	}
	return m.resetLocked()
}

func (m *DrainManager) CancelDrain() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resetLocked()
}

func (m *DrainManager) expireLocked() {
	if m.state != DrainStateIdle && !m.owned && !m.expiresAt.IsZero() && !m.now().Before(m.expiresAt) {
		m.resetLocked()
	}
}

func (m *DrainManager) resetLocked() bool {
	previousState, previousGeneration := m.state, m.generation
	previousExpiresAt, previousZeroSince, previousOwned := m.expiresAt, m.zeroSince, m.owned
	m.state, m.generation, m.expiresAt, m.zeroSince, m.owned = DrainStateIdle, "", time.Time{}, time.Time{}, false
	if err := m.persistLocked(); err != nil {
		m.state, m.generation = previousState, previousGeneration
		m.expiresAt, m.zeroSince, m.owned = previousExpiresAt, previousZeroSince, previousOwned
		return false
	}
	if m.tracker != nil {
		m.tracker.Open()
	}
	select {
	case m.reopened <- struct{}{}:
	default:
	}
	return true
}

func randomGeneration() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return time.Now().UTC().Format("20060102T150405.000000000")
	}
	return hex.EncodeToString(b[:])
}
