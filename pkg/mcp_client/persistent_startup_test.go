package mcpclient

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

// TestEnsurePersistentServers_StartsColdServersConcurrently verifies that independent
// cold persistent servers are started in parallel rather than one after another.
func TestEnsurePersistentServers_StartsColdServersConcurrently(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)

	var inFlight, maxInFlight atomic.Int32
	var mu sync.Mutex
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, workDir string) (*MCPServer, error) {
		n := inFlight.Add(1)
		mu.Lock()
		if n > maxInFlight.Load() {
			maxInFlight.Store(n)
		}
		mu.Unlock()
		time.Sleep(100 * time.Millisecond)
		inFlight.Add(-1)
		return &MCPServer{name: cfg.Name}, nil
	})

	configs := []models.MCPServerConfig{
		{Name: "svc-a", Command: []string{"svc-a"}},
		{Name: "svc-b", Command: []string{"svc-b"}},
		{Name: "svc-c", Command: []string{"svc-c"}},
		{Name: "svc-d", Command: []string{"svc-d"}},
	}
	if err := EnsurePersistentServers(context.Background(), configs, "."); err != nil {
		t.Fatalf("EnsurePersistentServers: %v", err)
	}
	if got := maxInFlight.Load(); got < 2 {
		t.Fatalf("persistent cold startup was serial: max concurrent starts = %d, want >= 2", got)
	}
}
