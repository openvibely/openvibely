package service

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestComputerProjectGrantSurvivesOriginTaskDeletion(t *testing.T) {
	for _, revokeFrom := range []string{"project", "other-task"} {
		t.Run(revokeFrom, func(t *testing.T) {
			offline := false
			cleared := map[string]int{}
			db, origin, project, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if offline {
					http.Error(w, "offline", 503)
					return
				}
				var args map[string]any
				_ = json.NewDecoder(r.Body).Decode(&args)
				if args["action"] == "clear_cookies" {
					cleared[r.URL.Path]++
				}
				io.WriteString(w, `{}`)
			})
			root := NewProjectComputerService(db.Projects, origin.provisioner)
			require.NoError(t, origin.repo.RequestHandoff(t.Context(), project.ID, "request_chrome_login", "https://example.test", "", ""))
			picks := []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}}
			shared, err := origin.repo.CreateChromeGrant(t.Context(), project.ID, "project", picks, 0)
			require.NoError(t, err)
			private, err := origin.repo.CreateChromeGrant(t.Context(), project.ID, "task", picks, 0)
			require.NoError(t, err)
			require.NoError(t, origin.repo.RecordChromeInstall(t.Context(), project.ID, shared.ID))
			other := &models.Task{ProjectID: project.ID, Title: "Other", Status: models.StatusCompleted, Category: models.CategoryCompleted, Priority: 1}
			require.NoError(t, db.Tasks.Create(t.Context(), other))
			scoped, err := root.ForTask(t.Context(), project.ID, other.ID)
			require.NoError(t, err)
			if err := scoped.SetEnabled(t.Context(), project.ID, true); err != nil {
				t.Fatal(err)
			}
			require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
			require.NoError(t, scoped.repo.RecordChromeInstall(t.Context(), project.ID, shared.ID))
			require.NoError(t, NewTaskService(db.Tasks, nil, nil).Delete(t.Context(), task.ID))
			for _, svc := range []*ProjectComputerService{root, scoped} {
				grants, _, err := svc.ChromeGrantsPage(t.Context(), project.ID, 0)
				require.NoError(t, err)
				require.Len(t, grants, 1)
				require.Equal(t, shared.ID, grants[0].ID)
				require.Empty(t, grants[0].TaskID)
			}
			_, err = origin.repo.ChromeGrant(t.Context(), project.ID, private.ID)
			require.Error(t, err, "task-specific grant must be deleted")
			installed, err := root.repo.ChromeInstallations(t.Context(), project.ID, shared.ID)
			require.NoError(t, err)
			require.Equal(t, []string{other.ID}, installed)
			revoker := root
			if revokeFrom == "other-task" {
				revoker = scoped
			}
			offline = true
			require.Error(t, revoker.RevokeChromeGrant(t.Context(), project.ID, shared.ID))
			installed, err = root.repo.ChromeInstallations(t.Context(), project.ID, shared.ID)
			require.NoError(t, err)
			require.Equal(t, []string{other.ID}, installed, "retain cleanup tracking while offline")
			require.Error(t, scoped.repo.RecordChromeInstall(t.Context(), project.ID, shared.ID), "revoked grants cannot be reused")
			offline = false
			_, err = scoped.Connection(t.Context(), project.ID)
			require.NoError(t, err)
			require.Equal(t, 1, cleared["/desktops/"+ComputerDesktopID(other.ID)+"/browser"])
			installed, err = root.repo.ChromeInstallations(t.Context(), project.ID, shared.ID)
			require.NoError(t, err)
			require.Empty(t, installed)
			require.NoError(t, NewTaskService(db.Tasks, nil, nil).Delete(t.Context(), other.ID))
			grants, _, err := root.ChromeGrantsPage(t.Context(), project.ID, 0)
			require.NoError(t, err)
			require.Len(t, grants, 1)
			require.NoError(t, root.RevokeChromeGrant(t.Context(), project.ID, shared.ID), "project revocation works without tasks")
			require.Error(t, root.RevokeChromeGrant(t.Context(), "another-project", shared.ID))
		})
	}
}

func TestComputerGrantRevocationContinuesPastUnavailableDesktop(t *testing.T) {
	blockedPath := ""
	cleared := map[string]bool{}
	db, origin, project, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == blockedPath {
			http.Error(w, "offline", 503)
			return
		}
		cleared[r.URL.Path] = true
		io.WriteString(w, `{}`)
	})
	require.NoError(t, origin.repo.RequestHandoff(t.Context(), project.ID, "request_chrome_login", "https://example.test", "", ""))
	grant, err := origin.repo.CreateChromeGrant(t.Context(), project.ID, "project", []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}}, 0)
	require.NoError(t, err)
	require.NoError(t, origin.repo.RecordChromeInstall(t.Context(), project.ID, grant.ID))
	other := &models.Task{ProjectID: project.ID, Title: "Other", Status: models.StatusCompleted, Category: models.CategoryCompleted, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), other))
	scoped, err := origin.ForTask(t.Context(), project.ID, other.ID)
	require.NoError(t, err)
	if err := scoped.SetEnabled(t.Context(), project.ID, true); err != nil {
		t.Fatal(err)
	}
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	require.NoError(t, scoped.repo.RecordChromeInstall(t.Context(), project.ID, grant.ID))
	installs, err := origin.repo.ChromeInstallations(t.Context(), project.ID, grant.ID)
	require.NoError(t, err)
	require.Len(t, installs, 2)
	blockedPath = "/desktops/" + ComputerDesktopID(installs[0]) + "/browser"
	require.Error(t, origin.RevokeChromeGrant(t.Context(), project.ID, grant.ID))
	require.True(t, cleared["/desktops/"+ComputerDesktopID(installs[1])+"/browser"])
	remaining, err := origin.repo.ChromeInstallations(t.Context(), project.ID, grant.ID)
	require.NoError(t, err)
	require.Equal(t, installs[:1], remaining)
	blockedPath = ""
	require.NoError(t, origin.RevokeChromeGrant(t.Context(), project.ID, grant.ID))
	require.True(t, cleared["/desktops/"+ComputerDesktopID(task.ID)+"/browser"])
}
