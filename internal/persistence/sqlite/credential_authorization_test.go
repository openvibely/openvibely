package sqlite

import (
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCredentialWritesRecheckActorInTransaction(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewLLMConfigRepo(db)
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('credential-admin','credential-admin'); INSERT INTO org_memberships VALUES('default','credential-admin','admin')`)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "credential-admin", Role: "owner"})
	for _, provider := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAICompatible} {
		config := &models.LLMConfig{Name: "credential-" + string(provider), Provider: provider, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "original"}
		require.NoError(t, repo.Create(ctx, config))
		saved, err := repo.GetByID(ctx, config.ID)
		require.NoError(t, err)
		writes := []func() (bool, error){}
		if provider == models.ProviderAnthropic {
			writes = append(writes, func() (bool, error) {
				return repo.UpdateStandardOAuthConnectionIfRevision(actor, saved.ID, saved.OAuthConfigRevision, provider, "changed", "", 0, "")
			})
			writes = append(writes, func() (bool, error) {
				return repo.ReplaceLinkedOAuthConnectionWithProfileIfRevision(actor, saved.ID, saved.OAuthConnectionID, saved.OAuthConfigRevision, provider, "changed", "", 0, "", "", "")
			})
		} else {
			writes = append(writes, func() (bool, error) {
				return repo.UpdateCustomOAuthConnectionIfRevision(actor, saved.ID, saved.OAuthConfigRevision, "changed", "", 0, "{}")
			})
		}
		_, err = db.Exec(`UPDATE org_memberships SET role='member' WHERE user_id='credential-admin'`)
		require.NoError(t, err)
		for _, write := range writes {
			changed, err := write()
			require.ErrorIs(t, err, persistence.ErrProjectDenied)
			require.False(t, changed)
		}
		unchanged, err := repo.GetByID(ctx, saved.ID)
		require.NoError(t, err)
		require.Equal(t, "original", unchanged.OAuthAccessToken)
		_, err = db.Exec(`UPDATE org_memberships SET role='admin' WHERE user_id='credential-admin'`)
		require.NoError(t, err)
		changed, err := writes[0]()
		require.NoError(t, err)
		require.True(t, changed)
	}
}

func TestModelCreationAndUpdateRecheckActor(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := t.Context()
	repo := NewLLMConfigRepo(db)
	_, err := db.Exec(`INSERT INTO users(id,username) VALUES('model-admin','model-admin'); INSERT INTO org_memberships VALUES('default','model-admin','admin')`)
	require.NoError(t, err)
	actor := models.WithPrincipal(ctx, models.Principal{UserID: "model-admin", Role: "owner"})
	original := &models.LLMConfig{Name: "Original", Provider: models.ProviderAnthropic, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "original-token"}
	require.NoError(t, repo.Create(actor, original))
	var modelCount, connectionCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM agent_configs`).Scan(&modelCount))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM oauth_connections`).Scan(&connectionCount))
	_, err = db.Exec(`UPDATE org_memberships SET role='member' WHERE user_id='model-admin'`)
	require.NoError(t, err)
	newConfig := &models.LLMConfig{Name: "Denied", Provider: models.ProviderAnthropic, Model: "test", AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "new-token"}
	require.ErrorIs(t, repo.Create(actor, newConfig), persistence.ErrProjectDenied)
	require.Empty(t, newConfig.ID)
	var count int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM agent_configs`).Scan(&count))
	require.Equal(t, modelCount, count)
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM oauth_connections`).Scan(&count))
	require.Equal(t, connectionCount, count)
	original.Name = "Changed"
	original.APIKey = "changed-key"
	require.ErrorIs(t, repo.Update(actor, original), persistence.ErrProjectDenied)
	stored, err := repo.GetByID(ctx, original.ID)
	require.NoError(t, err)
	require.Equal(t, "Original", stored.Name)
	require.Equal(t, "original-token", stored.OAuthAccessToken)
	require.Empty(t, stored.APIKey)
	_, err = db.Exec(`UPDATE org_memberships SET role='admin' WHERE user_id='model-admin'`)
	require.NoError(t, err)
	require.NoError(t, repo.Create(actor, newConfig))
	require.NoError(t, repo.Update(actor, original))
}
