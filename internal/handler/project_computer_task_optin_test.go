package handler

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestNewTaskComputerOptInIsSavedBeforeExecution(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "default off", true: "explicit opt-in"}[enabled], func(t *testing.T) {
			h, e, _ := setupTestHandler(t)
			project := createProject(t, h, "Task computer opt-in")
			h.computerSvc = service.NewProjectComputerService(h.projectRepo, handlerComputerProvisioner{})
			require.NoError(t, h.computerSvc.SetEnabled(t.Context(), project.ID, true))
			form := url.Values{"message": {"Do later"}, "category": {"backlog"}}
			if enabled {
				form.Set("computer_enabled", "on")
			}
			req := httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=new&thread=1", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			req.Header.Set("HX-Request", "true")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, 200, rec.Code, rec.Body.String())
			tasks, err := h.taskRepo.ListByProject(t.Context(), project.ID, "")
			require.NoError(t, err)
			require.Len(t, tasks, 1)
			scoped, err := h.computerSvc.ForTask(t.Context(), project.ID, tasks[0].ID)
			require.NoError(t, err)
			state, err := scoped.Get(t.Context(), project.ID)
			require.NoError(t, err)
			require.Equal(t, enabled, state.TaskEnabled)
			executions, err := h.execRepo.ListByTask(t.Context(), tasks[0].ID)
			require.NoError(t, err)
			require.Empty(t, executions)
		})
	}
}
