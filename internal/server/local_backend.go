package server

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/openvibely/openvibely/internal/config"
	"github.com/openvibely/openvibely/internal/service"
)

const localBackendEnv = "OPENVIBELY_LOCAL_BACKEND"

// localBackend pauses Local execution and its channel/automation services.
// Active work drains before dependent services stop; UI and remote connections
// remain available. Lifecycle transitions are serialized by mu.
type localBackend struct {
	mu        sync.Mutex
	enabled   atomic.Bool
	ctx       context.Context
	scheduler interface {
		Start(context.Context)
		Stop()
	}
	worker         *service.WorkerService
	persist        func(enabled bool) error
	startServices  func()
	resumeServices func()
	resumeWork     func()
	stopServices   func()
	active         func() int
	running        bool
	stopping       atomic.Bool
	generation     uint64
}

func localBackendEnabledFromEnv() bool {
	return !strings.EqualFold(strings.TrimSpace(os.Getenv(localBackendEnv)), "off")
}

func persistDesktopLocalBackend(enabled bool) error {
	value := "on"
	if !enabled {
		value = "off"
	}
	if err := config.SetEnvFileValue(config.DesktopConfigFilePath(), localBackendEnv, value); err != nil {
		return err
	}
	return os.Setenv(localBackendEnv, value)
}

func (l *localBackend) Enabled() bool { return l.enabled.Load() }

func (l *localBackend) Mode() string {
	if l.stopping.Load() {
		return "stopping"
	}
	if l.Enabled() {
		return "on"
	}
	return "off"
}

func (l *localBackend) start() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Enabled() {
		l.startLocked()
	}
}

func (l *localBackend) startLocked() {
	if l.running {
		return
	}
	if l.startServices != nil {
		l.startServices()
	}
	l.scheduler.Start(l.ctx)
	l.running = true
}

func (l *localBackend) stopLocked() {
	if l.running && l.stopServices != nil {
		l.stopServices()
	}
	l.running = false
	l.stopping.Store(false)
}

func (l *localBackend) shutdown() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.generation++
	l.enabled.Store(false)
	l.scheduler.Stop()
	l.stopLocked()
}

func (l *localBackend) SetEnabled(enabled bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if enabled == l.Enabled() {
		return nil
	}
	if l.persist != nil {
		if err := l.persist(enabled); err != nil {
			return err
		}
	}
	l.generation++
	generation := l.generation
	l.enabled.Store(enabled)
	if enabled {
		l.stopping.Store(false)
		if l.running { // Scheduler was stopped while other services drained.
			if l.resumeServices != nil {
				l.resumeServices()
			}
			l.scheduler.Start(l.ctx)
		} else {
			l.startLocked()
		}
		go func() {
			if l.worker != nil {
				l.worker.ResumeDispatch()
			}
			if l.resumeWork != nil {
				l.resumeWork()
			}
		}()
	} else {
		l.scheduler.Stop()
		l.stopping.Store(true)
		go l.finishStopping(generation)
	}
	return nil
}

func (l *localBackend) finishStopping(generation uint64) {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-l.ctx.Done():
			return
		case <-ticker.C:
			l.mu.Lock()
			if generation != l.generation {
				l.mu.Unlock()
				return
			}
			if l.active == nil || l.active() == 0 {
				l.stopLocked()
				l.mu.Unlock()
				return
			}
			l.mu.Unlock()
		}
	}
}
