package handler

import (
	"context"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

type handlerComputerProvisioner struct{}

func (handlerComputerProvisioner) Provider() string { return "local" }
func (handlerComputerProvisioner) Resolve(context.Context, string) (service.ComputerConnection, error) {
	return service.ComputerConnection{}, nil
}
func (handlerComputerProvisioner) Start(context.Context, string) (service.ComputerConnection, error) {
	return service.ComputerConnection{}, nil
}
func (handlerComputerProvisioner) Stop(context.Context, string) error { return nil }

func TestProjectComputerHandlersScopeAndTakeover(t *testing.T) {
	db := teststores.New(t)
	repo := db.Projects
	project := &models.Project{Name: "Viewer"}
	if err := repo.Create(t.Context(), project); err != nil {
		t.Fatal(err)
	}
	if err := repo.Computers().Configure(t.Context(), project.ID, "local"); err != nil {
		t.Fatal(err)
	}
	h := &Handler{projectRepo: repo, projectSvc: service.NewProjectService(repo), computerSvc: service.NewProjectComputerService(repo, handlerComputerProvisioner{})}
	e := echo.New()
	e.GET("/projects/:id/computer", h.ProjectComputerPage)
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	e.GET("/projects/:id/computer/stream/:mode", h.ProjectComputerStream)
	for _, tc := range []struct {
		method, path, origin string
		want                 int
	}{
		{"GET", "/projects/missing/computer", "", 404},
		{"GET", "/projects/" + project.ID + "/computer", "", 200},
		{"POST", "/projects/" + project.ID + "/computer/takeover", "https://evil.example", 403},
		{"POST", "/projects/" + project.ID + "/computer/takeover", "", 200},
		{"POST", "/projects/" + project.ID + "/computer/release", "", 200},
		// This fixture configures the computer but never enables it.
		{"GET", "/projects/" + project.ID + "/computer/stream/control", "", 503},
		{"GET", "/projects/" + project.ID + "/computer/vnc/other/file", "", 405},
	} {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Origin", tc.origin)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: %d want %d: %s", tc.path, rec.Code, tc.want, rec.Body.String()[:min(200, rec.Body.Len())])
		}
		if tc.method == http.MethodGet && rec.Code == 200 && !strings.Contains(rec.Body.String(), "data-computer-project=\""+project.ID+"\"") {
			t.Fatal("viewer missing project identity")
		}
	}
}

func TestProjectComputerTaskRoutesRejectCrossProjectAndUnapprovedImports(t *testing.T) {
	db := teststores.New(t)
	repo := db.Projects
	project := &models.Project{Name: "One"}
	other := &models.Project{Name: "Other"}
	for _, p := range []*models.Project{project, other} {
		if err := repo.Create(t.Context(), p); err != nil {
			t.Fatal(err)
		}
		if err := repo.Computers().Configure(t.Context(), p.ID, "local"); err != nil {
			t.Fatal(err)
		}
	}
	task := &models.Task{ProjectID: project.ID, Title: "Task", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := db.Tasks.Create(t.Context(), task); err != nil {
		t.Fatal(err)
	}
	h := &Handler{projectRepo: repo, computerSvc: service.NewProjectComputerService(repo, handlerComputerProvisioner{})}
	e := echo.New()
	e.GET("/projects/:id/computer/status", h.ProjectComputerStatus)
	e.POST("/projects/:id/computer/chrome-import", h.ProjectComputerChromeImport)
	e.POST("/projects/:id/computer/:action", h.ProjectComputerLifecycle)
	for _, tc := range []struct {
		method, path, body, origin string
		want                       int
	}{
		{"GET", "/projects/" + other.ID + "/computer/status?task_id=" + task.ID, "", "", 404},
		{"POST", "/projects/" + project.ID + "/computer/chrome-import?task_id=" + task.ID, `{"profile":"Default","sites":["example.test"]}`, "", 400},
		{"POST", "/projects/" + project.ID + "/computer/chrome-import?task_id=" + task.ID, `{}`, "https://other.example", 403},
		{"POST", "/projects/" + project.ID + "/computer/login-done?task_id=" + task.ID, `{}`, "", 400},
		{"POST", "/projects/" + project.ID + "/computer/start?task_id=" + task.ID, `{}`, "", 400},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Origin", tc.origin)
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s: %d %s", tc.path, rec.Code, rec.Body.String())
		}
	}
}
