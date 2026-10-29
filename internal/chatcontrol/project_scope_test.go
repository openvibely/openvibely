package chatcontrol

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestProjectSelectionSchemaIsContextual(t *testing.T) {
	for _, surface := range []Surface{SurfaceWeb, SurfaceAPI, SurfaceSlack, SurfaceEmail} {
		defs := ToolDefsForContext(models.ChatModeOrchestrate, surface, true)
		found := false
		for _, def := range defs {
			if def.Name != "list_tasks" {
				continue
			}
			found = true
			if surface == SurfaceWeb || surface == SurfaceAPI {
				require.NotNil(t, def.Schema.Properties["project_id"])
				require.NotContains(t, def.Schema.Required, "project_id")
			} else {
				require.Nil(t, def.Schema.Properties["project_id"])
			}
		}
		require.True(t, found)
	}
	require.Nil(t, Get("list_tasks").Schema.Properties["project_id"], "contextual generation must not mutate the registry")
}
