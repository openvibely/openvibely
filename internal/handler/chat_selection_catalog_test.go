package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestChatAutoSelectionCatalogFallback(t *testing.T) {
	for _, retired := range []models.LLMConfig{
		{Name: "Retired", Provider: models.ProviderAnthropic, Model: "claude-3-opus", AuthMethod: models.AuthMethodAPIKey, IsDefault: true},
		{Name: "OAuth retired", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth, IsDefault: true},
	} {
		t.Run(retired.Name, func(t *testing.T) {
			h, _, repo, db := setupTestHandlerWithDB(t)
			ctx := context.Background()
			_, err := db.ExecContext(ctx, `DELETE FROM agent_configs`)
			require.NoError(t, err)
			require.NoError(t, repo.Create(ctx, &retired))
			for _, images := range []bool{false, true} {
				selected, err := h.autoSelectAgent(ctx, "hello", images)
				require.ErrorContains(t, err, "no supported models configured")
				require.Nil(t, selected)
			}
			supported := &models.LLMConfig{Name: "Supported text model", Provider: models.ProviderOpenAICompatible, Model: "custom", AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key", BaseURL: "https://example.invalid/v1"}
			require.NoError(t, repo.Create(ctx, supported))
			for _, images := range []bool{false, true} {
				selected, err := h.autoSelectAgent(ctx, "hello", images)
				require.NoError(t, err)
				require.Equal(t, supported.ID, selected.ID)
				require.Equal(t, supported.APIKey, selected.APIKey)
				require.Equal(t, supported.BaseURL, selected.BaseURL)
			}
			saved, err := repo.GetByID(ctx, retired.ID)
			require.NoError(t, err)
			require.Equal(t, retired.Model, saved.Model)
			require.True(t, saved.IsDefault)
		})
	}
}

func TestAPIChatRejectsUnsupportedOnlyCatalogBeforeSideEffects(t *testing.T) {
	for _, retired := range []models.LLMConfig{
		{Name: "Retired", Provider: models.ProviderOpenAI, Model: "gpt-5.2-codex", AuthMethod: models.AuthMethodAPIKey},
		{Name: "OAuth retired", Provider: models.ProviderOpenAI, Model: "gpt-5.3-codex", AuthMethod: models.AuthMethodOAuth},
	} {
		t.Run(retired.Name, func(t *testing.T) {
			h, e, repo, db := setupTestHandlerWithDB(t)
			ctx := context.Background()
			_, err := db.ExecContext(ctx, `DELETE FROM agent_configs`)
			require.NoError(t, err)
			require.NoError(t, repo.Create(ctx, &retired))
			project := createProject(t, h, "Unsupported API chat")
			form := url.Values{"project_id": {project.ID}, "message": {"hello"}}
			req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			require.Contains(t, rec.Body.String(), "no supported models configured")
			tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
			require.NoError(t, err)
			require.Empty(t, tasks)
			executions, err := h.execRepo.ListByProject(ctx, project.ID, 50)
			require.NoError(t, err)
			require.Empty(t, executions)
			inputs, err := h.threadInputRepo.ListPendingForChat(ctx, project.ID)
			require.NoError(t, err)
			require.Empty(t, inputs)
		})
	}
}
