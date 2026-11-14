package service

import (
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
	"time"
)

func TestComputerChromeImportResumesProfiles(t *testing.T) {
	writes := 0
	db, svc, p, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { writes++; w.Write([]byte(`{}`)) })
	require.NoError(t, svc.repo.RequestHandoff(t.Context(), p.ID, "request_chrome_login", "https://example.test", "", ""))
	picks := []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}, {Profile: "Profile 1", Domain: "example.test", Name: "session", Path: "/"}}
	g, err := svc.repo.CreateChromeGrant(t.Context(), p.ID, "task", picks, -1)
	require.NoError(t, err)
	load := func(profile string) ([]map[string]any, error) {
		if profile == "Profile 1" {
			return nil, errors.New("unavailable")
		}
		return []map[string]any{{"name": "session", "value": "fixture"}}, nil
	}
	count, err := svc.installChromeProfiles(t.Context(), p.ID, g, map[string]int{}, false, load)
	require.Equal(t, 1, count)
	var partial *ChromeImportIncomplete
	require.ErrorAs(t, err, &partial)
	require.Equal(t, g.ID, partial.GrantID)
	// Completed, expired and revoked grants can push this one-time import out
	// of the old 200-row list, but must not hide its saved recovery state.
	for i := 0; i < 303; i++ {
		other, err := svc.repo.CreateChromeGrant(t.Context(), p.ID, "task", picks, time.Now().Add(24*time.Hour).Unix())
		require.NoError(t, err)
		require.NoError(t, svc.repo.RecordChromeProfile(t.Context(), p.ID, other.ID, "Default", 1))
		switch i % 3 {
		case 0:
			require.NoError(t, svc.repo.RecordChromeProfile(t.Context(), p.ID, other.ID, "Profile 1", 1))
		case 1:
			require.NoError(t, svc.repo.RevokeChromeGrant(t.Context(), p.ID, other.ID))
		case 2:
			require.NoError(t, db.Fixtures.ExpireComputerChromeGrant(t.Context(), other.ID))
		}
	}
	listed, err := svc.repo.ChromeGrants(t.Context(), p.ID)
	require.NoError(t, err)
	require.Len(t, listed, 200)
	for _, item := range listed {
		require.NotEqual(t, g.ID, item.ID)
	}
	pending, err := svc.PendingChromeImport(t.Context(), p.ID)
	require.NoError(t, err)
	require.Equal(t, g.ID, pending.ID)
	// Recovery belongs to the originating task, not every task that can list
	// project-wide permissions, and never crosses project boundaries.
	otherTask := &models.Task{ProjectID: p.ID, Title: "Other recovery task", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), otherTask))
	otherService, err := svc.ForTask(t.Context(), p.ID, otherTask.ID)
	require.NoError(t, err)
	require.NoError(t, otherService.SetTaskEnabled(t.Context(), p.ID, true))
	absent, err := otherService.PendingChromeImport(t.Context(), p.ID)
	require.NoError(t, err)
	require.Nil(t, absent)
	absent, err = svc.PendingChromeImport(t.Context(), "another-project")
	require.NoError(t, err)
	require.Nil(t, absent)

	progress, err := svc.repo.ChromeImportProgress(t.Context(), p.ID, g.ID)
	require.NoError(t, err)
	require.Equal(t, 1, progress["Default"])
	before := writes
	count, err = svc.installChromeProfiles(t.Context(), p.ID, g, progress, true, func(profile string) ([]map[string]any, error) {
		require.Equal(t, "Profile 1", profile)
		return []map[string]any{{"name": "session", "value": "fixture"}}, nil
	})
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.Equal(t, before+1, writes)
	pending, err = svc.PendingChromeImport(t.Context(), p.ID)
	require.NoError(t, err)
	require.Nil(t, pending)
	require.NoError(t, svc.repo.RevokeChromeGrant(t.Context(), p.ID, g.ID))
	_, err = svc.installChromeGrant(t.Context(), p.ID, g)
	require.Error(t, err)
}
