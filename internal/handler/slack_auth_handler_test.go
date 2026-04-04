package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

func setupSlackAuthHandler(t *testing.T) (*Handler, *echo.Echo, *repository.SlackAuthRepo, string) {
	t.Helper()
	h, e, _, db := setupTestHandlerWithDB(t)
	slackAuthRepo := repository.NewSlackAuthRepo(db)
	h.SetSlackAuthRepo(slackAuthRepo)
	project := createProject(t, h, "Slack Auth Project")
	return h, e, slackAuthRepo, project.ID
}

func TestListSlackAuthorizedUsers_Empty(t *testing.T) {
	_, e, _, projectID := setupSlackAuthHandler(t)
	rec := htmxGet(e, "/channels/slack/authorized-users?project_id="+projectID)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "No authorized users configured")
	body := rec.Body.String()
	idx := strings.Index(body, `name="slack_user_id"`)
	if idx == -1 {
		t.Fatalf("expected slack user id input")
	}
	end := idx + 220
	if end > len(body) {
		end = len(body)
	}
	if strings.Contains(body[idx:end], "required") {
		t.Fatalf("expected slack auth add input not to use required attribute")
	}
}

func TestAddSlackAuthorizedUser(t *testing.T) {
	_, e, _, projectID := setupSlackAuthHandler(t)

	form := url.Values{}
	form.Set("project_id", projectID)
	form.Set("slack_user_id", "U12345")
	form.Set("display_name", "Alice")

	rec := postForm(e, "/channels/slack/authorized-users", form)
	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "Alice")
	assertContains(t, rec, "ID: U12345")
}

func TestRemoveSlackAuthorizedUser(t *testing.T) {
	_, e, repo, projectID := setupSlackAuthHandler(t)

	user := &models.SlackAuthorizedUser{
		ProjectID:   projectID,
		SlackUserID: "U777",
		DisplayName: "To Remove",
		AddedBy:     "web",
	}
	if err := repo.Create(context.Background(), user); err != nil {
		t.Fatalf("failed to create slack auth user: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/channels/slack/authorized-users/"+user.ID+"?project_id="+projectID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assertCode(t, rec, http.StatusOK)
	assertContains(t, rec, "No authorized users configured")

	users, err := repo.ListByProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("list slack auth users failed: %v", err)
	}
	if len(users) != 0 {
		t.Fatalf("expected 0 users after removal, got %d", len(users))
	}
}

func TestSlackAuthorizedUsers_PersistAfterChannelsReopen(t *testing.T) {
	_, e, _, projectID := setupSlackAuthHandler(t)

	form := url.Values{}
	form.Set("project_id", projectID)
	form.Set("slack_user_id", "U111")
	form.Set("display_name", "Alice Slack")
	addReq := httptest.NewRequest(http.MethodPost, "/channels/slack/authorized-users", strings.NewReader(form.Encode()))
	addReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	addRec := httptest.NewRecorder()
	e.ServeHTTP(addRec, addReq)
	assertCode(t, addRec, http.StatusOK)

	reopenReq := httptest.NewRequest(http.MethodGet, "/channels?project_id="+projectID, nil)
	reopenRec := httptest.NewRecorder()
	e.ServeHTTP(reopenRec, reopenReq)
	assertCode(t, reopenRec, http.StatusOK)
	assertContains(t, reopenRec, "Alice Slack")
	assertContains(t, reopenRec, "ID: U111")
}

func TestAddSlackAuthorizedUser_Validation(t *testing.T) {
	_, e, _, projectID := setupSlackAuthHandler(t)

	missingProject := url.Values{}
	missingProject.Set("slack_user_id", "U123")
	req := httptest.NewRequest(http.MethodPost, "/channels/slack/authorized-users", strings.NewReader(missingProject.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	assertCode(t, rec, http.StatusBadRequest)

	missingUser := url.Values{}
	missingUser.Set("project_id", projectID)
	req2 := httptest.NewRequest(http.MethodPost, "/channels/slack/authorized-users", strings.NewReader(missingUser.Encode()))
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec2 := httptest.NewRecorder()
	e.ServeHTTP(rec2, req2)
	assertCode(t, rec2, http.StatusBadRequest)
}
