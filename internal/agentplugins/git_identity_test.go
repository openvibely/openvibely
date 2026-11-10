package agentplugins

import (
	"context"
	"errors"
	"testing"

	"github.com/openvibely/openvibely/pkg/gitidentity"
)

func TestBackgroundPluginGitRequiresResolvedIdentity(t *testing.T) {
	sentinel := errors.New("no acting user")
	SetGitContextResolver(func(ctx context.Context) context.Context {
		return gitidentity.WithResolver(ctx, func(context.Context) (gitidentity.Identity, error) {
			return gitidentity.Identity{}, sentinel
		})
	})
	t.Cleanup(func() { SetGitContextResolver(nil) })
	if err := runCommandCombined(context.Background(), "git", "--version"); !errors.Is(err, sentinel) {
		t.Fatalf("Git escaped identity resolution: %v", err)
	}
}
