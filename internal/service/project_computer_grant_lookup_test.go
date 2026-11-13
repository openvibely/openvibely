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
	require.NoError(t, svc.repo.RequestLogin(t.Context(), project.ID, "request_chrome_login", "https://example.test"))
	picks := []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}}
	old, err := svc.repo.CreateChromeGrant(t.Context(), project.ID, "task", picks, 0)
	require.NoError(t, err)
	require.NoError(t, svc.repo.RecordChromeInstall(t.Context(), project.ID, old.ID))
	for i := 0; i < 201; i++ {
		g, err := svc.repo.CreateChromeGrant(t.Context(), project.ID, "task", picks, time.Now().Add(time.Hour).Unix())
		require.NoError(t, err)
		if i%2 == 0 {
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
	found, err := svc.repo.ChromeGrant(t.Context(), project.ID, old.ID)
	require.NoError(t, err)
	require.Equal(t, picks, found.Selections)
	require.NoError(t, svc.RevokeChromeGrant(t.Context(), project.ID, old.ID))
	require.Greater(t, cleared, 0, "installed cookies must still be cleared")
	found, err = svc.repo.ChromeGrant(t.Context(), project.ID, old.ID)
	require.NoError(t, err)
	require.True(t, found.Revoked)
	require.Error(t, svc.repo.RecordChromeInstall(t.Context(), project.ID, old.ID))

	other := &models.Task{ProjectID: project.ID, Title: "Other desktop", Status: models.StatusPending, Category: models.CategoryBacklog, Priority: 2}
	require.NoError(t, db.Tasks.Create(t.Context(), other))
	scoped, err := svc.ForTask(t.Context(), project.ID, other.ID)
	require.NoError(t, err)
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
}
