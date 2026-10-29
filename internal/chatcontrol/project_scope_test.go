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

func TestExistingProjectAssertionsHaveContextualDescriptions(t *testing.T) {
	for _, name := range []string{"create_agent", "update_agent", "update_project_settings", "list_automations", "get_automation"} {
		t.Run(name, func(t *testing.T) {
			original := Get(name)
			originalSchema := original.ParameterSchema()
			for _, surface := range []Surface{SurfaceWeb, SurfaceAPI, SurfaceSlack, SurfaceEmail} {
				for _, def := range ToolDefsForContext(models.ChatModeOrchestrate, surface, true) {
					if def.Name != name {
						continue
					}
					if surface == SurfaceWeb || surface == SurfaceAPI {
						require.Equal(t, projectSelectionHelp, def.Schema.Properties["project_id"].Description)
						require.NotSame(t, original.Schema.Properties["project_id"], def.Schema.Properties["project_id"])
						require.NotContains(t, def.Description, "current-project assertions")
						if name == "update_project_settings" {
							require.NotContains(t, def.Description, "project_id/project_name are")
							require.Contains(t, def.Schema.Properties["project_name"].Description, "selected project's name")
						}
					} else {
						require.Equal(t, original.Description, def.Description)
						require.Equal(t, original.Schema, def.Schema)
					}
				}
			}
			require.Equal(t, originalSchema, original.ParameterSchema(), "generating web/API definitions must not mutate shared registry schemas")
		})
	}
}
