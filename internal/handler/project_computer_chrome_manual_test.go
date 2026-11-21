package handler

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerChromeProfilesWithoutHandoff(t *testing.T) {
	// Never enumerate the developer's real browser profiles.
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CHROME_USER_DATA_DIR", "")
	t.Setenv("CHROME_CONFIG_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	db := teststores.New(t)
	project := &models.Project{Name: "Manual import"}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	require.NoError(t, db.Projects.Computers().Configure(t.Context(), project.ID, "local"))
	task := &models.Task{ProjectID: project.ID, Title: "Task", Status: models.StatusCompleted, Category: models.CategoryCompleted, Priority: 1}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	svc := service.NewProjectComputerService(db.Projects, handlerComputerProvisioner{})
	scoped, err := svc.ForTask(t.Context(), project.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetEnabled(t.Context(), project.ID, true))
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, true))
	h := &Handler{projectRepo: db.Projects, computerSvc: svc}
	e := echo.New()
	e.GET("/projects/:id/computer/chrome", h.ProjectComputerChrome)
	e.GET("/projects/:id/computer/chrome-grants", h.ProjectComputerChromeGrants)
	base := "/projects/" + project.ID + "/computer"
	get := func(path, origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", base+path, nil)
		req.Header.Set("Origin", origin)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	query := "?task_id=" + task.ID
	require.Equal(t, 403, get("/chrome"+query, "https://elsewhere.test").Code)
	require.Equal(t, 409, get("/chrome", "").Code)
	require.Contains(t, get("/chrome-grants"+query, "").Body.String(), `"chrome_available":`)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		res := get("/chrome"+query, "")
		require.Equal(t, 400, res.Code)
		require.Contains(t, res.Body.String(), "local macOS or Linux")
		require.Contains(t, get("/chrome-grants"+query, "").Body.String(), `"chrome_available":false`)
		return
	}
	missing := get("/chrome"+query, "")
	require.Equal(t, 400, missing.Code)
	require.Contains(t, missing.Body.String(), "No local Chrome profiles")
	profileRoot := filepath.Join(home, "Library/Application Support/Google/Chrome")
	if runtime.GOOS == "linux" {
		profileRoot = filepath.Join(home, ".config/google-chrome")
	}
	require.NoError(t, os.MkdirAll(filepath.Join(profileRoot, "Default"), 0700))
	res := get("/chrome"+query, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	require.Contains(t, res.Body.String(), `"profiles":["Default"]`)
	require.False(t, strings.Contains(res.Body.String(), "awaiting approval"))
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), project.ID, false))
	require.Equal(t, 409, get("/chrome"+query, "").Code)
}
