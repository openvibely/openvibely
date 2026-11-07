package handler

import (
	"github.com/a-h/templ"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestTaskThreadResponseShowsSendingAccount(t *testing.T) {
	tc := NewStoreTestContext(t)
	h, e := tc.handler, tc.echo
	h.SetAuthConfig(auth.Config{Enabled: true})
	agent := createAgent(t, tc.llmConfigRepo)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	principals := map[string]models.Principal{}
	for _, username := range []string{"alice", "testuser1"} {
		invite, err := org.Invite(t.Context(), models.BootstrapUserID, "member")
		require.NoError(t, err)
		member, err := org.Accept(t.Context(), invite, username, "long-test-password", "")
		require.NoError(t, err)
		principals[username] = member.Principal
	}
	project := createProject(t, h, "Author labels")
	for _, principal := range principals {
		require.NoError(t, tc.stores.ProjectAccess.SetMember(t.Context(), models.BootstrapUserID, project.ID, principal.UserID, "editor"))
	}
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
		req = req.WithContext(models.WithPrincipal(req.Context(), principals[username]))
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		// Name appears in the label as well as in the message text.
		require.GreaterOrEqual(t, strings.Count(rec.Body.String(), username), 2, rec.Body.String())
	}
}

func TestMessageAuthorLabelsFollowAuthMode(t *testing.T) {
	for _, mode := range []auth.AuthMode{auth.AuthModeDisabled, auth.AuthModeLocal, auth.AuthModeHostedSSO} {
		t.Run(string(mode), func(t *testing.T) {
			h := &Handler{authMode: mode}
			e := echo.New()
			rec := httptest.NewRecorder()
			c := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)
			c.Request().Header.Set("HX-Request", "true")
			c.Set("handler", h)
			message := templ.Join(
				components.ChatQueuedInputRow("input", "queued message", "/steer", false, "local"),
				components.ChatSteeringInputRow("steering", "steer message", false, "local"),
				components.ChatBubble("User", "sent message", "local"),
				components.ChatBubbleWithAttachments("User", "attachment message", nil, "default", "local"),
			)
			require.NoError(t, render(c, http.StatusOK, message))
			if mode == auth.AuthModeDisabled {
				require.NotContains(t, rec.Body.String(), "local")
			} else {
				require.Equal(t, 4, strings.Count(rec.Body.String(), "local"))
			}
			require.Contains(t, rec.Body.String(), "queued message")
			require.Contains(t, rec.Body.String(), "Steering pending")
		})
	}
}
