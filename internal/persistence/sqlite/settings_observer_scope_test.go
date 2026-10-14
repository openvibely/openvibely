package sqlite_test

import (
	"context"
	"github.com/openvibely/openvibely/internal/persistence/sqlite"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"time"
)

func TestSettingsQueryAcquiredObserverRunsWhileConnectionIsHeld(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	settingsRepo := sqlite.NewSettingsRepo(db)
	require.NoError(t, settingsRepo.Set(ctx, "email_address", "bot@example.com"))

	acquired := make(chan struct{})
	release := make(chan struct{})
	settingsRepo.SetQueryAcquiredObserver(func(query string) {
		if strings.Contains(query, "FROM app_settings") {
			close(acquired)
			<-release
		}
	})

	loadDone := make(chan error, 1)
	go func() {
		_, err := settingsRepo.GetMany(ctx, []string{"email_address"})
		loadDone <- err
	}()
	<-acquired

	waitCount := db.Stats().WaitCount
	competingDone := make(chan error, 1)
	go func() {
		var value string
		competingDone <- db.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key = ?", "email_address").Scan(&value)
	}()
	require.Eventually(t, func() bool { return db.Stats().WaitCount > waitCount }, time.Second, time.Millisecond,
		"competing query must queue behind the settings statement before the observer releases it")
	close(release)
	require.NoError(t, <-loadDone)
	require.NoError(t, <-competingDone)
}
