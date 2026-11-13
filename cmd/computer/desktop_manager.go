//go:build linux

package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

type desktopStart struct {
	done chan struct{}
	err  error
}

type desktopManager struct {
	mu       *sync.Mutex
	sessions map[string]*desktopProcess
	starting map[string]*desktopStart
	deleting map[string]bool
	stopping bool
	start    func(string) (*desktopProcess, error)
}

// Reserve capacity under the shared lock, then perform slow startup outside it.
// Callers for the same desktop share the result; unrelated desktops proceed.
func (m *desktopManager) ensure(ctx context.Context, name string, activity bool) (*desktopProcess, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if m.stopping || m.deleting[name] {
			return nil, fmt.Errorf("Desktop is stopping; retry shortly")
		}
		if pending := m.starting[name]; pending != nil {
			if !activity {
				return nil, nil
			}
			m.mu.Unlock()
			select {
			case <-pending.done:
			case <-ctx.Done():
				m.mu.Lock()
				return nil, ctx.Err()
			}
			m.mu.Lock()
			if pending.err != nil {
				return nil, pending.err
			}
			continue
		}
		p := m.sessions[name]
		if p != nil && p.reaping != nil {
			if !activity {
				return nil, nil
			}
			ready := p.reaping
			m.mu.Unlock()
			select {
			case <-ready:
			case <-ctx.Done():
				m.mu.Lock()
				return nil, ctx.Err()
			}
			m.mu.Lock()
			continue
		}
		if p != nil {
			select {
			case <-p.done:
				delete(m.sessions, name)
			default:
				p.requests++
				if activity {
					p.lastActivity = time.Now()
				}
				return p, nil
			}
		}
		if !activity {
			return nil, nil
		}
		for key, p := range m.sessions {
			select {
			case <-p.done:
				delete(m.sessions, key)
			default:
			}
		}
		if len(m.sessions)+len(m.starting) >= 32 {
			return nil, fmt.Errorf("All 32 desktop slots are occupied. Close unused viewers or wait for the idle timeout; active desktops are kept running.")
		}
		uid := desktopUID(name)
		for key, p := range m.sessions {
			if key != name && p.uid == uid {
				return nil, fmt.Errorf("desktop identity collision")
			}
		}
		for key := range m.starting {
			if key != name && desktopUID(key) == uid {
				return nil, fmt.Errorf("desktop identity collision")
			}
		}
		pending := &desktopStart{done: make(chan struct{})}
		m.starting[name] = pending
		m.mu.Unlock()
		p, err := m.start(name)
		m.mu.Lock()
		delete(m.starting, name)
		pending.err = err
		if err == nil {
			p.uid = uid
			p.lastActivity = time.Now()
			m.sessions[name] = p
		}
		close(pending.done)
		if err != nil {
			return nil, err
		}
		// Recheck cancellation and shutdown after publishing. Shutdown can now stop
		// the process even if the request that initiated it has disconnected.
	}
}

// Admission is closed by the caller before waiting. Never hold the shared lock
// while waiting for startup, so existing desktops can still serve requests.
func (m *desktopManager) waitStarts(ctx context.Context, name string) error {
	for {
		m.mu.Lock()
		var done <-chan struct{}
		for key, p := range m.starting {
			if name == "" || key == name {
				done = p.done
				break
			}
		}
		m.mu.Unlock()
		if done == nil {
			return nil
		}
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
