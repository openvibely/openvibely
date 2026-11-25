package pages

import (
	"bytes"
	"context"
	"net/url"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/stretchr/testify/require"
)

func TestPluginNavPreservesWorkspaceScope(t *testing.T) {
	for _, scope := range []string{models.AllProjectsID, "shared:repository"} {
		t.Run(scope, func(t *testing.T) {
			ctx := layout.WithWorkspaceProject(context.Background(), scope)
			entries := []PluginNavEntry{{InstanceID: "local-page", ProjectID: "local-project", Title: "Local page"}, {InstanceID: "remote-page", ProjectID: "remote-project", Title: "Remote page"}}
			var out bytes.Buffer
			require.NoError(t, PluginNav(entries, true).Render(ctx, &out))
			for _, entry := range entries {
				require.Contains(t, out.String(), "/p/"+entry.InstanceID+"?project_id="+url.QueryEscape(scope))
			}
		})
	}
}
