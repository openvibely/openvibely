//go:build linux

package main

import (
	"context"
	"errors"
	"sync"

	"github.com/openvibely/openvibely/pkg/agenttools"
)

type computerExecution struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// Each desktop keeps cancellation tombstones until it exits. A cancellation
// that arrives before its shell request must still prevent that late launch.
// Bound storage and fail closed rather than evicting tombstones and reviving work.
type computerExecutions struct {
	mu      sync.Mutex
	entries map[string]*computerExecution
	manager *agenttools.ProcessManager
}

func (e *computerExecutions) get(id string) (*computerExecution, error) {
	if id == "" || len(id) > 256 {
		return nil, errors.New("execution_id is required (maximum 256 bytes)")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if v := e.entries[id]; v != nil {
		return v, nil
	}
	if len(e.entries) >= 4096 {
		return nil, errors.New("desktop execution limit reached; restart this desktop")
	}
	ctx, cancel := context.WithCancel(context.Background())
	v := &computerExecution{ctx: ctx, cancel: cancel}
	e.entries[id] = v
	return v, nil
}
func (e *computerExecutions) owner(id string) (context.Context, error) {
	v, err := e.get(id)
	if err != nil {
		return nil, err
	}
	if err := v.ctx.Err(); err != nil {
		return nil, err
	}
	return v.ctx, nil
}
func (e *computerExecutions) cancel(ctx context.Context, id string) error {
	v, err := e.get(id)
	if err != nil {
		return err
	}
	v.cancel()
	return e.manager.WaitForOwner(ctx, v.ctx)
}
