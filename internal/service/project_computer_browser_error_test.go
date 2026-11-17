package service

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestProjectBrowserUnavailableReachesAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/browser") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadGateway)
			io.WriteString(w, `{"error":{"code":"browser_unavailable","message":"internal detail must not be forwarded"}}`)
			return
		}
		io.WriteString(w, `{"ready":true}`)
	}))
	defer server.Close()
	db := teststores.New(t)
	project := &models.Project{Name: "Browser error", RepoPath: t.TempDir()}
	require.NoError(t, db.Projects.Create(t.Context(), project))
	task := &models.Task{ProjectID: project.ID, Title: "Browser", Category: models.CategoryActive, Priority: 1, Status: models.StatusPending}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	svc := NewProjectComputerService(db.Projects, &fakeComputerProvisioner{connection: ComputerConnection{Endpoint: server.URL, Token: "secret"}})
	require.NoError(t, svc.SetEnabled(t.Context(), project.ID, true))
	require.NoError(t, svc.Start(t.Context(), project.ID))
	enableComputerTaskForTest(t, svc, project.ID, task.ID)
	tools := svc.RuntimeTools(t.Context(), project.ID, task.ID)
	require.NotNil(t, tools)
	_, handled, failed, err := tools.Executor(t.Context(), "project_browser", json.RawMessage(`{"action":"read"}`))
	require.True(t, handled)
	require.True(t, failed)
	require.EqualError(t, err, "Browser unavailable. Could not connect to Chromium within 10 seconds.")
}

func TestComputerUnrecognizedErrorsRemainGeneric(t *testing.T) {
	for _, body := range []string{`{"error":{"code":"unknown","message":"private"}}`, `Browser unavailable`, `{"error":`} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadGateway)
				io.WriteString(w, body)
			}))
			defer server.Close()
			svc := &ProjectComputerService{client: server.Client()}
			_, err := svc.request(t.Context(), ComputerConnection{Endpoint: server.URL}, "POST", "/browser", []byte(`{}`))
			require.EqualError(t, err, "computer operation failed (502)")
		})
	}
}
