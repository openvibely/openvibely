package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

func TestMemberChannelsOmitsCredentials(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "audit-member", "long-password-value", "")
	require.NoError(t, err)
	secrets := map[string]string{
		service.TelegramSettingBotToken:      "telegram-private-secret",
		service.GitHubSettingPAT:             "github-private-secret",
		service.GitHubSettingAppPrivateKey:   "github-key-private-secret",
		service.SlackSettingClientSecret:     "slack-client-private-secret",
		service.SlackSettingAppToken:         "slack-app-private-secret",
		service.SlackSettingBotTokenOverride: "slack-bot-private-secret",
		service.DiscordSettingBotToken:       "discord-private-secret",
		service.EmailSettingPassword:         "email-private-secret",
	}
	require.NoError(t, tc.stores.Settings.SetMany(ctx, secrets))
	app := echo.New()
	app.Use(h.projectAccessBoundary)
	app.GET("/channels", h.handleChannels)
	for _, fragment := range []bool{false, true} {
		for _, admin := range []bool{false, true} {
			principal := user.Principal
			principal.Role = "owner" // Current database membership, not stale claims, controls secrets.
			if admin {
				principal = models.Principal{UserID: models.BootstrapUserID}
			}
			req := httptest.NewRequest("GET", "/channels?project_id=default&connection_state=configured", nil)
			req = req.WithContext(models.WithPrincipal(ctx, principal))
			if fragment {
				req.Header.Set("HX-Request", "true")
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			require.Equal(t, 200, rec.Code)
			card := regexp.MustCompile(`(?s)<div\s+[^>]*data-channel-type="telegram"[^>]*>`).FindString(rec.Body.String())
			require.NotEmpty(t, card)
			require.NotContains(t, card, " hidden", "redacting the token must not hide a configured bot")
			for key, secret := range secrets {
				if admin {
					require.Contains(t, rec.Body.String(), secret, key)
				} else {
					require.NotContains(t, rec.Body.String(), secret, key)
				}
			}
		}
	}

}

func TestAllProjectsExcludesDeniedTasks(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	ctx := t.Context()
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetProjectAccessService(service.NewProjectAccessService(stores.ProjectAccess))
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "audit-reader", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Restricted audit project")
	policy, err := stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	task := &models.Task{ProjectID: project.ID, Title: "AUDIT_RESTRICTED_TASK_TITLE", Category: models.CategoryBacklog, Status: models.StatusPending, Priority: 2}
	require.NoError(t, stores.Tasks.Create(ctx, task))
	call := func(scope string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", "/tasks/sidebar?project_id="+scope, nil)
		req = req.WithContext(models.WithPrincipal(ctx, user.Principal))
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, 404, call(project.ID).Code)
	rec := call(models.AllProjectsID)
	require.Equal(t, 200, rec.Code)
	require.NotContains(t, rec.Body.String(), task.Title)
}

func TestAllProjectsCannotDeleteViewerAlert(t *testing.T) {
	h, app, _, stores := setupTestHandlerWithStores(t)
	ctx := t.Context()
	h.SetAuthMode(auth.AuthModeLocal)
	org := service.NewOrganizationService(stores.Organizations, "secret")
	h.SetOrganizationService(org)
	h.SetProjectAccessService(service.NewProjectAccessService(stores.ProjectAccess))
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "audit-viewer", "long-password-value", "")
	require.NoError(t, err)
	project := createProject(t, h, "Viewer-only project")
	policy, err := stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.OrgRole = "viewer"
	require.NoError(t, stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	alert := &models.Alert{ProjectID: project.ID, Title: "Protected alert", Type: "custom", Severity: "info"}
	require.NoError(t, h.alertSvc.Create(ctx, alert))
	call := func(scope string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", "/alerts/"+alert.ID+"?project_id="+scope, nil)
		req = req.WithContext(models.WithPrincipal(ctx, user.Principal))
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		return rec
	}
	require.Equal(t, 404, call(project.ID).Code)
	rec := call(models.AllProjectsID)
	require.Equal(t, 404, rec.Code)
	_, err = h.alertSvc.GetByID(ctx, project.ID, alert.ID)
	require.NoError(t, err)
}

type revokingProjectLookup struct {
	persistence.ProjectStore
	once  sync.Once
	after func()
}

func (r *revokingProjectLookup) GetByID(ctx context.Context, id string) (*models.Project, error) {
	p, e := r.ProjectStore.GetByID(ctx, id)
	if e == nil && p != nil {
		r.once.Do(r.after)
	}
	return p, e
}
func TestRevocationDuringSubscriptionAdmissionBlocksOutput(t *testing.T) {
	tc, app := organizationTestApp(t, true)
	h := tc.handler
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	h.broadcaster = events.NewBroadcaster()
	access := service.NewProjectAccessService(tc.stores.ProjectAccess)
	h.SetProjectAccessService(access)
	org := h.organizationSvc
	invite, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, invite, "race-reader", "long-password-value", "")
	require.NoError(t, err)
	token, err := org.NewSession(ctx, user.UserID, time.Hour)
	require.NoError(t, err)
	project := &models.Project{Name: "Race fixture"}
	require.NoError(t, tc.stores.Projects.Create(ctx, project))
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, access.SetMember(owner, project.ID, user.UserID, "viewer"))
	h.projectRepo = &revokingProjectLookup{ProjectStore: h.projectRepo, after: func() { require.NoError(t, access.SetMember(owner, project.ID, user.UserID, "")) }}
	server := httptest.NewServer(app)
	defer server.Close()
	conn, _, err := websocket.Dial(ctx, strings.Replace(server.URL, "http", "ws", 1)+"/events/ws", &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {server.URL}, "Cookie": {auth.DefaultCookieName + "=" + token}}})
	require.NoError(t, err)
	defer conn.CloseNow()
	var f liveFrame
	require.NoError(t, wsjson.Read(ctx, conn, &f))
	require.Equal(t, "ready", f.Event)
	require.NoError(t, wsjson.Write(ctx, conn, liveCommand{Op: "live", ID: "watch", ProjectID: project.ID}))
	require.NoError(t, wsjson.Read(ctx, conn, &f))
	require.Equal(t, "error", f.Event)
	require.Equal(t, "access blocked", f.Data)
	role, err := tc.stores.ProjectAccess.Role(ctx, user.UserID, project.ID)
	require.NoError(t, err)
	require.Empty(t, role)

}
