package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/auth"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/pageintegration"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/stretchr/testify/require"
)

func setupPageAccess(t *testing.T) (*pluginTestEnv, models.Principal) {
	t.Helper()
	env := setupPluginPages(t)
	// Seed a transcript before installing the authenticated request harness.
	sendPageTest(t, env, env.inA, "existing conversation")
	require.NoError(t, env.h.execRepo.Complete(t.Context(), env.starts[0].ExecID, models.ExecCompleted, "answer", "", 0, 0))
	require.NoError(t, env.h.taskRepo.UpdateStatus(t.Context(), env.starts[0].TaskID, models.StatusCompleted))
	org := service.NewOrganizationService(env.stores.Organizations, "page-access-secret")
	token, err := org.Invite(t.Context(), models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(t.Context(), token, "page-reader", "long-password-value", "")
	require.NoError(t, err)
	env.h.SetOrganizationService(org)
	env.h.SetProjectAccessService(service.NewProjectAccessService(env.stores.ProjectAccess))
	env.h.SetAuthMode(auth.AuthModeLocal)
	// The actual authorization middleware and handlers, with a pre-authenticated principal.
	env.e = echo.New()
	env.e.Use(env.h.projectAccessBoundary)
	env.h.registerPluginPageRoutes(env.e)
	return env, user.Principal
}

func pageAccessRequest(t *testing.T, env *pluginTestEnv, p models.Principal, method, target, body, contentType string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body)).WithContext(models.WithPrincipal(t.Context(), p))
	req.Header.Set("HX-Request", "true")
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	env.e.ServeHTTP(rec, req)
	return rec
}

func TestPageProjectPermissionMatrix(t *testing.T) {
	env, member := setupPageAccess(t)
	for _, role := range []string{"", "viewer", "editor", "admin"} {
		t.Run(role, func(t *testing.T) {
			p := member
			if role == "admin" {
				p = models.Principal{UserID: models.BootstrapUserID, Role: "owner"}
			} else {
				require.NoError(t, env.stores.ProjectAccess.SetMember(t.Context(), models.BootstrapUserID, env.projA.ID, p.UserID, role))
			}
			readStatus := http.StatusOK
			if role == "" {
				readStatus = http.StatusNotFound
			}
			for _, endpoint := range []string{"/p/" + env.inA.ID + "?project_id=" + env.projA.ID, pluginAPI(env.inA.ID, env.projA.ID, "/chat/thread"), pluginAPI(env.inA.ID, env.projA.ID, "/chat/thread/pending-inputs"), pluginAPI(env.inA.ID, env.projA.ID, "/chat/thread/composer-action")} {
				rec := pageAccessRequest(t, env, p, "GET", endpoint, "", "")
				require.Equal(t, readStatus, rec.Code, rec.Body.String())
			}
			nav := pageAccessRequest(t, env, p, "GET", "/plugin-pages/nav?project_id="+models.AllProjectsID, "", "")
			require.Equal(t, 200, nav.Code)
			if role == "" {
				require.NotContains(t, nav.Body.String(), env.inA.ID)
			} else {
				require.Contains(t, nav.Body.String(), env.inA.ID)
			}
			if role != "admin" {
				require.NotContains(t, nav.Body.String(), env.inB.ID)
			}
			resolved, err := env.mgr.Resolve(t.Context(), env.inA.ID, env.projA.ID)
			require.NoError(t, err)
			hd, err := env.mgr.IssueHandle(resolved, "user:"+p.UserID)
			require.NoError(t, err)
			asset := pageAccessRequest(t, env, p, "GET", "/plugin-assets/"+hd.Token+"/index.html", "", "")
			require.Equal(t, readStatus, asset.Code)
			bridge := func(kind, payload string) *httptest.ResponseRecorder {
				body := fmt.Sprintf(`{"v":1,"id":"request","handle":%q,"type":%q,"payload":%s}`, hd.Token, kind, payload)
				return pageAccessRequest(t, env, p, "POST", pluginAPI(env.inA.ID, env.projA.ID, "/bridge"), body, "application/json")
			}
			rec := bridge("op.invoke", `{"operation":"get_report"}`)
			require.Equal(t, readStatus, rec.Code, rec.Body.String())
			for _, op := range []string{"set_target", "refresh_data"} {
				input := `{}`
				if op == "set_target" {
					input = `{"target":190000}`
				}
				rec = bridge("op.invoke", fmt.Sprintf(`{"operation":%q,"input":%s}`, op, input))
				if role == "viewer" || role == "" {
					require.NotEqual(t, 200, rec.Code)
				} else {
					require.Equal(t, 200, rec.Code, rec.Body.String())
				}
			}
			for _, suffix := range []string{"/chat", "/chat/stop", "/chat/thread/steer", "/chat/thread/queued/fake/steer"} {
				rec = pageAccessRequest(t, env, p, "POST", pluginAPI(env.inA.ID, env.projA.ID, suffix), "message=hello", "application/x-www-form-urlencoded")
				if role == "" {
					require.Equal(t, 404, rec.Code)
				} else if role == "viewer" {
					require.Equal(t, 403, rec.Code)
					require.Equal(t, "true", rec.Header().Get("X-OpenVibely-Permission-Denied"))
				} else {
					if suffix == "/chat" || suffix == "/chat/stop" {
						require.Equal(t, 200, rec.Code, rec.Body.String())
					} else {
						require.NotEqual(t, 403, rec.Code)
					}
				}
			}
			rec = bridge("analyze.request", `{"prompt":"analyze"}`)
			if role == "viewer" || role == "" {
				require.NotEqual(t, 200, rec.Code)
			}
			if role != "admin" {
				rec = pageAccessRequest(t, env, p, "POST", "/settings/plugin-pages/install?project_id="+env.projA.ID, "", "")
				require.Equal(t, 403, rec.Code)
			}
		})
	}
}

func TestPageRevocationBlocksQueueAndExistingTools(t *testing.T) {
	env, p := setupPageAccess(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projA.ID, p.UserID, "editor"))
	send := func(message string) {
		rec := pageAccessRequest(t, env, p, "POST", pluginAPI(env.inA.ID, env.projA.ID, "/chat"), url.Values{"message": {message}}.Encode(), "application/x-www-form-urlencoded")
		require.Equal(t, 200, rec.Code, rec.Body.String())
	}
	send("running")
	active := env.starts[len(env.starts)-1]
	send("queued")
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projA.ID, p.UserID, "viewer"))
	_, handled, failed, err := active.RuntimeTools.Executor(t.Context(), "page_set_target", json.RawMessage(`{"target":3}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.True(t, failed)
	// A viewer still has access to read-only tools, using the saved sender identity.
	_, handled, failed, err = active.RuntimeTools.Executor(t.Context(), "page_get_report", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, failed)
	require.NoError(t, env.h.execRepo.Complete(t.Context(), active.ExecID, models.ExecCompleted, "done", "", 0, 0))
	require.NoError(t, env.h.taskRepo.UpdateStatus(t.Context(), active.TaskID, models.StatusCompleted))
	restartPageTest(t, env)
	env.h.RecoverQueuedInputs(t.Context())
	require.Len(t, env.starts, 2)
	pending, err := env.h.threadInputRepo.ListPendingForTask(t.Context(), active.TaskID)
	require.NoError(t, err)
	require.Empty(t, pending)
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projA.ID, p.UserID, ""))
	_, _, failed, err = active.RuntimeTools.Executor(t.Context(), "page_get_report", json.RawMessage(`{}`))
	require.NoError(t, err)
	require.True(t, failed)
	require.ErrorIs(t, env.h.requirePageAccess(models.WithPrincipal(t.Context(), p), env.projA.ID, authz.View), authz.ErrDenied)
}

func TestPageEventsCloseOnProjectRevocation(t *testing.T) {
	env, p := setupPageAccess(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projA.ID, p.UserID, "viewer"))
	resolved, err := env.mgr.Resolve(t.Context(), env.inA.ID, env.projA.ID)
	require.NoError(t, err)
	hd, err := env.mgr.IssueHandle(resolved, "user:"+p.UserID)
	require.NoError(t, err)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.e.ServeHTTP(w, r.WithContext(models.WithPrincipal(r.Context(), p)))
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", server.URL+pluginAPI(env.inA.ID, env.projA.ID, "/events")+"&handle="+hd.Token, nil)
	require.NoError(t, err)
	response, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, 200, response.StatusCode)
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projA.ID, p.UserID, ""))
	body, err := io.ReadAll(response.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "event: closed")
	_, ok := env.mgr.LookupHandle(hd.Token)
	require.False(t, ok)
}

// The sample's read operation must not persist initial data for a viewer.
func TestPageViewerReadDoesNotWriteState(t *testing.T) {
	env, p := setupPageAccess(t)
	require.NoError(t, env.stores.ProjectAccess.SetMember(t.Context(), models.BootstrapUserID, env.projA.ID, p.UserID, "viewer"))
	resolved, err := env.mgr.Resolve(t.Context(), env.inA.ID, env.projA.ID)
	require.NoError(t, err)
	before, err := env.stores.PagePlugins.GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	_, err = pageintegration.Run(t.Context(), env.mgr.Integration(resolved), pageintegration.Caller{PrincipalID: "user:" + p.UserID}, "get_report", json.RawMessage(`{}`))
	require.NoError(t, err)
	after, err := env.stores.PagePlugins.GetInstance(t.Context(), env.inA.ID)
	require.NoError(t, err)
	require.Equal(t, before.StateJSON, after.StateJSON)
}

func TestPageInstallationRecoveryRechecksProjectAccess(t *testing.T) {
	env, p := setupPageAccess(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projA.ID, p.UserID, "editor"))
	response := pageAccessRequest(t, env, p, "POST", pluginAPI(env.inA.ID, env.projA.ID, "/chat"), "message=install", "application/x-www-form-urlencoded")
	require.Equal(t, 200, response.Code, response.Body.String())
	original := env.starts[len(env.starts)-1]
	question, err := env.h.chatInputRequests.create(env.projA.ID, original.ExecID, []chatInputRequestQuestion{{ID: "install", Question: "Install dependencies?", Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}, &persistence.ChatInstallationRequest{Manifest: json.RawMessage(`{}`), ChatMode: string(models.ChatModeOrchestrate)})
	require.NoError(t, err)
	require.NoError(t, env.h.persistChatInputRequest(t.Context(), question))
	record, err := env.h.chatInputRequestRepo.Resolve(t.Context(), question.ID, env.projA.ID, `[{"question_id":"install","label":"Not now"}]`)
	require.NoError(t, err)
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projA.ID, p.UserID, "viewer"))
	restartPageTest(t, env)
	require.Error(t, env.h.startInstallationContinuation(t.Context(), *record))
	require.Len(t, env.starts, 2)
}

func TestPageNewViewerAndRevokedAsset(t *testing.T) {
	env, p := setupPageAccess(t)
	owner := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projB.ID, p.UserID, "viewer"))
	response := pageAccessRequest(t, env, p, "GET", "/p/"+env.inB.ID+"?project_id="+env.projB.ID, "", "")
	require.Equal(t, 200, response.Code, response.Body.String())
	match := handlePattern.FindStringSubmatch(response.Body.String())
	require.Len(t, match, 2)
	assetURL := "/plugin-assets/" + match[1] + "/index.html"
	require.Equal(t, 200, pageAccessRequest(t, env, p, "GET", assetURL, "", "").Code)
	require.NoError(t, env.h.projectAccessSvc.SetMember(owner, env.projB.ID, p.UserID, ""))
	require.Equal(t, 404, pageAccessRequest(t, env, p, "GET", assetURL, "", "").Code)
}
