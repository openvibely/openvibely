package service

import "sync/atomic"

// localServiceGate prevents settings reloads from restarting paused listeners.
// A nil gate preserves server-mode behavior.
type localServiceGate struct{ gate atomic.Pointer[func() bool] }

func (g *localServiceGate) SetStartGate(allowed func() bool) { g.gate.Store(&allowed) }
func (g *localServiceGate) startAllowed() bool {
	f := g.gate.Load()
	return f == nil || *f == nil || (*f)()
}
