package service

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestComputerChromeRevokeGrantOutsideListWindow(t *testing.T) {
	cleared := 0
	db, svc, project, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var args map[string]any
		_ = json.NewDecoder(r.Body).Decode(&args)
		if args["action"] == "clear_cookies" {
			cleared++
		}
		io.WriteString(w, `{}`)
	})
	require.NoError(t, svc.repo.RequestHandoff(t.Context(), project.ID, "request_chrome_login", "https://example.test", "", ""))
	picks := []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}}
	old, err := svc.repo.CreateChromeGrant(t.Context(), project.ID, "task", picks, 0)
	require.NoError(t, err)
	require.NoError(t, svc.repo.RecordChromeInstall(t.Context(), project.ID, old.ID))
	for i := 0; i < 201; i++ {
		g, err := svc.repo.CreateChromeGrant(t.Context(), project.ID, "task", picks, time.Now().Add(time.Hour).Unix())
		require.NoError(t, err)
		if i%2 == 0 {
			require.NoError(t, svc.repo.RecordChromeInstall(t.Context(), project.ID, g.ID))
			require.NoError(t, svc.repo.RevokeChromeGrant(t.Context(), project.ID, g.ID))
		} else {
			require.NoError(t, db.Fixtures.ExpireComputerChromeGrant(t.Context(), g.ID))
		}
	}
	listed, err := svc.repo.ChromeGrants(t.Context(), project.ID)
	require.NoError(t, err)
	require.Len(t, listed, 200)
	for _, g := range listed {
		require.NotEqual(t, old.ID, g.ID)
	}
	first, more, err := svc.ChromeGrantsPage(t.Context(), project.ID, 0)
	require.NoError(t, err)
	require.True(t, more)
	require.Len(t, first, 200)
	second, more, err := svc.ChromeGrantsPage(t.Context(), project.ID, 200)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, second, 2)
	seen := map[string]bool{}
	for _, g := range append(first, second...) {
		require.False(t, seen[g.ID])
		seen[g.ID] = true
	}
	require.True(t, seen[old.ID], "non-expiring grant must be accessible through pagination")
	repeat, _, err := svc.ChromeGrantsPage(t.Context(), project.ID, 0)
	require.NoError(t, err)
	require.Equal(t, first, repeat, "expiry ties need stable ordering")
	found, err := svc.repo.ChromeGrant(t.Context(), project.ID, old.ID)
	require.NoError(t, err)
	require.Equal(t, picks, found.Selections)
	require.NoError(t, svc.RevokeChromeGrant(t.Context(), project.ID, old.ID))
	require.Greater(t, cleared, 0, "installed cookies must still be cleared")
	found, err = svc.repo.ChromeGrant(t.Context(), project.ID, old.ID)
	require.NoError(t, err)
	require.True(t, found.Revoked)
	remaining, _, err := svc.ChromeGrantsPage(t.Context(), project.ID, 0)
	require.NoError(t, err)
	for _, grant := range remaining {
		require.NotEqual(t, old.ID, grant.ID, "fully cleared revoked permission must not offer retry")
	}
	require.Error(t, svc.repo.RecordChromeInstall(t.Context(), project.ID, old.ID))

	other := &models.Task{ProjectID: project.ID, Title: "Other desktop", Status: models.StatusPending, Category: models.CategoryBacklog, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), other))
	scoped, err := svc.ForTask(t.Context(), project.ID, other.ID)
	require.NoError(t, err)
	if err := scoped.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	_, err = scoped.repo.ChromeGrant(t.Context(), project.ID, old.ID)
	require.Error(t, err)
	_, err = svc.repo.ChromeGrant(t.Context(), "another-project", old.ID)
	require.Error(t, err)
	_, err = svc.repo.ChromeGrant(t.Context(), project.ID, "missing")
	require.Error(t, err)
	shared, err := svc.repo.CreateChromeGrant(t.Context(), project.ID, "project", picks, 0)
	require.NoError(t, err)
	found, err = scoped.repo.ChromeGrant(t.Context(), project.ID, shared.ID)
	require.NoError(t, err)
	require.Equal(t, shared.ID, found.ID)
	visible, more, err := scoped.ChromeGrantsPage(t.Context(), project.ID, 0)
	require.NoError(t, err)
	require.False(t, more)
	require.Len(t, visible, 1)
	require.Equal(t, shared.ID, visible[0].ID)
	visible, _, err = scoped.ChromeGrantsPage(t.Context(), "another-project", 0)
	require.NoError(t, err)
	require.Empty(t, visible)
}
