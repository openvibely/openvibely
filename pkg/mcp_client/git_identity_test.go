package mcpclient

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/gitidentity"
)

func TestPersonalGitIdentitySeparatesLocalMCPProcesses(t *testing.T) {
	resetSharedStateForTests()
	t.Cleanup(resetSharedStateForTests)
	starts := 0
	withStartServerFn(t, func(ctx context.Context, cfg models.MCPServerConfig, _ string) (*MCPServer, error) {
		starts++
		if cfg.Env["OV_GIT_IDENTITY_SCOPE"] == "" {
			t.Fatal("missing credential scope")
		}
		return &MCPServer{name: cfg.Name}, nil
	})
	config := models.MCPServerConfig{Name: "local", Type: "stdio", Env: map[string]string{"EXISTING": "value"}}
	open := func(user, token string) *MCPManager {
		t.Helper()
		ctx := gitidentity.WithResolver(context.Background(), func(context.Context) (gitidentity.Identity, error) {
			return gitidentity.Identity{Scope: user, Token: token}, nil
		})
		m, err := NewMCPManager(ctx, []models.MCPServerConfig{config}, ".")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { m.Close() })
		return m
	}
	a := open("alice", "token-a")
	same := open("alice", "token-a")
	b := open("bob", "token-b")
	rotated := open("alice", "token-new")
	if starts != 3 {
		t.Fatalf("started %d processes, want three isolated identities", starts)
	}
	if a.servers[0].srv != same.servers[0].srv || a.servers[0].srv == b.servers[0].srv || a.servers[0].srv == rotated.servers[0].srv {
		t.Fatal("incorrect process reuse")
	}
	if len(config.Env) != 1 {
		t.Fatal("mutated shared configuration")
	}
}
