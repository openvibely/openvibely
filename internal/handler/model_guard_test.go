package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestHandlerHasConfiguredModelsUsesExistenceQuery(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	h := &Handler{llmConfigRepo: repo}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	counter.Reset()
	counter.SetEnabled(true)
	hasModels, err := h.hasConfiguredModels(c)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("hasConfiguredModels: %v", err)
	}
	if !hasModels {
		t.Fatal("hasConfiguredModels = false, want true for seeded test model")
	}

	statements := counter.Statements()
	if len(statements) != 1 {
		t.Fatalf("statements = %#v, want exactly one model availability query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	if stmt != "select exists(select 1 from agent_configs)" {
		t.Fatalf("hasConfiguredModels query = %q, want SELECT EXISTS", statements[0])
	}
	for _, forbidden := range []string{"api_key", "oauth_access_token", "oauth_refresh_token", "oauth_client_secret", "extra_body_json", "custom_auth_state_json", "order by"} {
		if strings.Contains(stmt, forbidden) {
			t.Fatalf("hasConfiguredModels query selected forbidden data %q: %s", forbidden, statements[0])
		}
	}
}

func TestHandlerHasConfiguredModelsNilRepoErrors(t *testing.T) {
	h := &Handler{}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	hasModels, err := h.hasConfiguredModels(c)
	if err == nil {
		t.Fatal("hasConfiguredModels nil repo error = nil")
	}
	if hasModels {
		t.Fatal("hasConfiguredModels nil repo = true, want false")
	}
	if !strings.Contains(err.Error(), "model repository is not configured") {
		t.Fatalf("hasConfiguredModels nil repo error = %q", err.Error())
	}
}

func TestHandlerHasConfiguredModelsEmptyRepo(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	if _, err := db.ExecContext(context.Background(), `DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	h := &Handler{llmConfigRepo: repo}
	c := echo.New().NewContext(httptest.NewRequest(http.MethodGet, "/", nil), httptest.NewRecorder())

	hasModels, err := h.hasConfiguredModels(c)
	if err != nil {
		t.Fatalf("hasConfiguredModels empty repo: %v", err)
	}
	if hasModels {
		t.Fatal("hasConfiguredModels empty repo = true, want false")
	}
}
