package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestSettingsRepoCompareAndSetRequiresExpectedValueAndGuards(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewSettingsRepo(db)
	require.NoError(t, repo.SetMany(ctx, map[string]string{"cursor": "10", "account": "one"}))

	updated, err := repo.CompareAndSet(ctx, "cursor", "10", "20", map[string]string{"account": "one"})
	require.NoError(t, err)
	require.True(t, updated)
	updated, err = repo.CompareAndSet(ctx, "cursor", "10", "30", map[string]string{"account": "one"})
	require.NoError(t, err)
	require.False(t, updated)
	updated, err = repo.CompareAndSet(ctx, "cursor", "20", "30", map[string]string{"account": "two"})
	require.NoError(t, err)
	require.False(t, updated)
	cursor, err := repo.Get(ctx, "cursor")
	require.NoError(t, err)
	require.Equal(t, "20", cursor)
}

func TestSettingsRepoSetManyRollsBackWholeSnapshot(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	repo := NewSettingsRepo(db)
	require.NoError(t, repo.Set(ctx, "first", "old-first"))
	require.NoError(t, repo.Set(ctx, "fail", "old-fail"))
	_, err := db.ExecContext(ctx, `CREATE TRIGGER reject_failed_setting BEFORE UPDATE ON app_settings
		WHEN NEW.key = 'fail' BEGIN SELECT RAISE(ABORT, 'forced settings failure'); END`)
	require.NoError(t, err)

	err = repo.SetMany(ctx, map[string]string{"first": "new-first", "fail": "new-fail"})
	require.Error(t, err)
	values, err := repo.GetMany(ctx, []string{"first", "fail"})
	require.NoError(t, err)
	require.Equal(t, "old-first", values["first"])
	require.Equal(t, "old-fail", values["fail"])
}

func TestSettingsRepoRemoveChannelsBulkRollsBackWebhookAndSettings(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := NewSettingsRepo(db)
	webhookRepo := NewWebhookRepo(db)
	webhook := &models.WebhookEndpoint{ProjectID: "default", Name: "Rollback webhook", Enabled: true}
	require.NoError(t, webhookRepo.Create(ctx, webhook))
	require.NoError(t, settingsRepo.Set(ctx, "provider_secret", "keep"))
	_, err := db.ExecContext(ctx, `CREATE TRIGGER reject_channel_removal BEFORE UPDATE ON app_settings
		WHEN NEW.key = 'provider_secret' BEGIN SELECT RAISE(ABORT, 'forced channel removal failure'); END`)
	require.NoError(t, err)

	err = settingsRepo.RemoveChannelsBulk(ctx, "default", map[string]string{"provider_secret": ""}, []string{webhook.ID}, false)
	require.Error(t, err)
	secret, err := settingsRepo.Get(ctx, "provider_secret")
	require.NoError(t, err)
	require.Equal(t, "keep", secret)
	retained, err := webhookRepo.GetByID(ctx, webhook.ID)
	require.NoError(t, err)
	require.NotNil(t, retained)
}
