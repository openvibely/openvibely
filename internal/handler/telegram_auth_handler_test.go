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
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func setupTelegramAuthHandler(t *testing.T) (*Handler, *echo.Echo, *repository.TelegramAuthRepo, string) {
	t.Helper()
	db := testutil.NewTestDB(t)

	projectRepo := repository.NewProjectRepo(db)
	telegramAuthRepo := repository.NewTelegramAuthRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	projectSvc := service.NewProjectService(projectRepo)

	h := New(
		projectSvc,
		nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		llmConfigRepo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	h.SetTelegramAuthRepo(telegramAuthRepo)

	e := echo.New()
	h.RegisterRoutes(e)

	// Create a project
	ctx := context.Background()
	project := &models.Project{Name: "Test Project"}
	if err := projectSvc.Create(ctx, project); err != nil {
		t.Fatalf("failed to create project: %v", err)
	}

	return h, e, telegramAuthRepo, project.ID
}

func TestListTelegramAuthorizedUsers_Empty(t *testing.T) {
	_, e, _, projectID := setupTelegramAuthHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/channels/telegram/authorized-users?project_id="+projectID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "No authorized users configured") {
		t.Error("expected empty state message")
	}
	idx := strings.Index(body, `name="user_id_or_username"`)
	if idx == -1 {
		t.Fatal("expected telegram user input")
	}
	end := idx + 240
	if end > len(body) {
		end = len(body)
	}
	if strings.Contains(body[idx:end], "required") {
		t.Fatal("expected telegram auth add input not to use required attribute")
	}
}

func TestAddTelegramAuthorizedUser_ByID(t *testing.T) {
	_, e, _, projectID := setupTelegramAuthHandler(t)

	form := url.Values{}
	form.Set("project_id", projectID)
	form.Set("user_id_or_username", "123456")

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/authorized-users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, ">123456<") {
		t.Error("expected numeric user ID display-name default")
	}
	if !strings.Contains(body, "ID: 123456") {
		t.Error("expected user ID to be shown")
	}
}

func TestAddTelegramAuthorizedUser_ByUsername(t *testing.T) {
	_, e, _, projectID := setupTelegramAuthHandler(t)

	form := url.Values{}
	form.Set("project_id", projectID)
	form.Set("user_id_or_username", "johndoe")

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/authorized-users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "@johndoe") {
		t.Error("expected username to appear in list")
	}
}

func TestAddTelegramAuthorizedUser_StripsAtPrefix(t *testing.T) {
	_, e, telegramAuthRepo, projectID := setupTelegramAuthHandler(t)

	form := url.Values{}
	form.Set("project_id", projectID)
	form.Set("user_id_or_username", "@JohnDoe")

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/authorized-users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify the username is stored without @ and lowercased
	users, err := telegramAuthRepo.ListByProject(context.Background(), projectID)
	if err != nil {
		t.Fatalf("ListByProject failed: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("expected 1 user, got %d", len(users))
	}
	if users[0].TelegramUsername != "johndoe" {
		t.Errorf("expected username 'johndoe' (stripped @ and lowercased), got %q", users[0].TelegramUsername)
	}
}

func TestAddTelegramAuthorizedUser_MissingProjectID(t *testing.T) {
	_, e, _, _ := setupTelegramAuthHandler(t)

	form := url.Values{}
	form.Set("user_id_or_username", "123456")

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/authorized-users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestAddTelegramAuthorizedUser_MissingUser(t *testing.T) {
	_, e, _, projectID := setupTelegramAuthHandler(t)

	form := url.Values{}
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/channels/telegram/authorized-users", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestRemoveTelegramAuthorizedUser(t *testing.T) {
	h, e, telegramAuthRepo, projectID := setupTelegramAuthHandler(t)
	otherProject := createProject(t, h, "Other Telegram Auth Project")
	ctx := context.Background()

	// Add a user first
	user := &models.TelegramAuthorizedUser{
		ProjectID:      projectID,
		TelegramUserID: 777,
		DisplayName:    "To Remove",
		AddedBy:        "web",
	}
	if err := telegramAuthRepo.Create(ctx, user); err != nil {
		t.Fatalf("failed to create user: %v", err)
	}
	otherUser := &models.TelegramAuthorizedUser{ProjectID: otherProject.ID, TelegramUserID: 888, DisplayName: "Other Telegram User", AddedBy: "web"}
	if err := telegramAuthRepo.Create(ctx, otherUser); err != nil {
		t.Fatalf("failed to create other user: %v", err)
	}

	req := httptest.NewRequest(http.MethodDelete, "/channels/telegram/authorized-users/"+user.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	// Verify the response reload keeps the deleted record's project context.
	body := rec.Body.String()
	if strings.Contains(body, "To Remove") {
		t.Errorf("expected removed user to disappear from response, got %q", body)
	}
	if !strings.Contains(body, "Other Telegram User") {
		t.Errorf("expected other system-level user to remain visible, got %q", body)
	}
	if !strings.Contains(body, `name="project_id" value="`+projectID+`"`) {
		t.Errorf("expected omitted project_id delete to reload with record project %q, got %q", projectID, body)
	}

	// Verify in DB
	deleted, err := telegramAuthRepo.GetByID(ctx, user.ID)
	if err != nil {
		t.Fatalf("GetByID deleted user failed: %v", err)
	}
	if deleted != nil {
		t.Fatalf("expected deleted user to be removed, got %#v", deleted)
	}
	remaining, err := telegramAuthRepo.GetByID(ctx, otherUser.ID)
	if err != nil {
		t.Fatalf("GetByID other user failed: %v", err)
	}
	if remaining == nil {
		t.Fatal("expected other project user to remain")
	}
}

func TestRemoveTelegramAuthorizedUser_NotFound(t *testing.T) {
	_, e, _, _ := setupTelegramAuthHandler(t)

	req := httptest.NewRequest(http.MethodDelete, "/channels/telegram/authorized-users/nonexistent", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestListTelegramAuthorizedUsers_MissingProjectID(t *testing.T) {
	_, e, _, _ := setupTelegramAuthHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/channels/telegram/authorized-users", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}
