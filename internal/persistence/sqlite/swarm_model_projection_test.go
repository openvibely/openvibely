package sqlite_test

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func TestSwarmModelResolutionQueriesExcludeCredentials(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	stores := sqlite.NewStores(db, nil)
	ctx := t.Context()
	model := models.LLMConfig{Name: "Swarm projection", Provider: models.ProviderTest, Model: "test", IsDefault: true}
	require.NoError(t, stores.LLMConfigs.Create(ctx, &model))
	project := models.Project{Name: "Swarm projection", DefaultAgentConfigID: &model.ID}
	require.NoError(t, stores.Projects.Create(ctx, &project))
	counter.Reset()
	counter.SetEnabled(true)
	id, err := stores.Projects.GetDefaultAgentConfigID(ctx, project.ID)
	require.NoError(t, err)
	require.NotNil(t, id)
	require.Equal(t, model.ID, *id)
	exists, err := stores.LLMConfigs.ExistsByID(ctx, *id)
	require.NoError(t, err)
	require.True(t, exists)
	counter.SetEnabled(false)
	require.Len(t, counter.Statements(), 2)
	for _, statement := range counter.Statements() {
		for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json"} {
			require.NotContains(t, strings.ToLower(statement), forbidden, "compact resolver SQL selected a forbidden column")
		}
	}
}
