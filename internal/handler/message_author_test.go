package handler

import (
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTaskThreadResponseShowsSendingAccount(t *testing.T) {
	h, e, configs := setupTestHandler(t)
	agent := createAgent(t, configs)
	project := createProject(t, h, "Author labels")
	task := createTask(t, h, project.ID, "Shared thread", func(task *models.Task) {
		task.Category = models.CategoryCompleted
		task.Status = models.StatusCompleted
		task.AgentID = &agent.ID
	})
	for _, username := range []string{"alice", "testuser1"} {
		form := url.Values{"message": {"message from " + username}}
		req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/thread", strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("HX-Request", "true")
		req = req.WithContext(models.WithPrincipal(req.Context(), models.Principal{UserID: username, Username: username, OrgID: models.DefaultOrgID, Role: "member"}))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		// Name appears in the label as well as in the message text.
		require.GreaterOrEqual(t, strings.Count(rec.Body.String(), username), 2, rec.Body.String())
	}
}
