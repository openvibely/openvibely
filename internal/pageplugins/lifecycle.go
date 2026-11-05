package pageplugins

import (
	"context"
	"time"
)

type instanceLifetime struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// BeginInstance serializes admission with disable and binds work to the current
// enabled lifetime. The caller must release before returning. A queued request
// from a disabled lifetime cannot become valid again after re-enabling.
func (m *Manager) BeginInstance(ctx context.Context, id string) (context.Context, func(), error) {
	m.mu.Lock()
	life := m.lifetimes[id]
	if life == nil {
		lifetimeCtx, cancel := context.WithCancel(context.Background())
		life = &instanceLifetime{ctx: lifetimeCtx, cancel: cancel}
		m.lifetimes[id] = life
	}
	m.mu.Unlock()
	workCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(life.ctx, cancel)
	lock := m.instanceLock(id)
	lock.Lock()
	done := func() { stop(); cancel(); lock.Unlock() }
	if life.ctx.Err() != nil {
		done()
		return nil, nil, ErrDisabled
	}
	in, err := m.store.GetInstance(workCtx, id)
	if err != nil {
		done()
		return nil, nil, err
	}
	if in == nil || !in.Enabled {
		done()
		return nil, nil, ErrDisabled
	}
	return workCtx, done, nil
}

func (m *Manager) deactivate(ctx context.Context, id string, remove bool) error {
	// Once shutdown begins it must finish even if the browser disconnects.
	ctx, cleanup := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cleanup()
	// Interrupt startup even when Enable currently owns the lifecycle lock.
	m.mu.Lock()
	if life := m.lifetimes[id]; life != nil {
		life.cancel()
	}
	m.mu.Unlock()
	lifecycle := m.instanceLock("lifecycle:" + id)
	lifecycle.Lock()
	defer lifecycle.Unlock()
	// Close admission and cancel active operations BEFORE waiting for their lock.
	m.mu.Lock()
	life := m.lifetimes[id]
	if life == nil {
		lifetimeCtx, cancel := context.WithCancel(context.Background())
		life = &instanceLifetime{ctx: lifetimeCtx, cancel: cancel}
		m.lifetimes[id] = life
	}
	life.cancel()
	m.mu.Unlock()
	lock := m.instanceLock(id)
	lock.Lock()
	defer lock.Unlock()
	if err := m.store.SetInstanceEnabled(ctx, id, false); err != nil {
		return err
	}
	var cancelErr error
	if m.OnDisable != nil {
		cancelErr = m.OnDisable(ctx, id)
	}
	m.releaseInstance(id)
	if cancelErr != nil {
		return cancelErr
	}
	if remove {
		return m.store.DeleteInstance(ctx, id)
	}
	return nil
}

// LifetimeContext is captured while admission is held and used by asynchronous
// host work. It is cancelled on disable and never reused by a later enable.
func (m *Manager) LifetimeContext(id string) context.Context {
	m.mu.Lock()
	defer m.mu.Unlock()
	if life := m.lifetimes[id]; life != nil {
		return life.ctx
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
