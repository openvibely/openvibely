package service

import (
	"errors"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"net/http"
	"testing"
)

func TestComputerChromeImportResumesProfiles(t *testing.T) {
	writes := 0
	_, svc, p, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { writes++; w.Write([]byte(`{}`)) })
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
	pending, err := svc.PendingChromeImport(t.Context(), p.ID)
	require.NoError(t, err)
	require.Equal(t, g.ID, pending.ID)
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
