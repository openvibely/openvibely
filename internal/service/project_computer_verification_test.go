package service

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

const loginTestPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC1lEQVR42mP8/x8AAusB9Wl6iWQAAAAASUVORK5CYII="

func TestComputerVisualVerificationUsesTaskModelAndKeepsUncertainLoginPaused(t *testing.T) {
	db, computer, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"verified": false, "url": "https://example.test/account", "screenshot": loginTestPNG})
	})
	configs := db.LLMConfigs
	config := &models.LLMConfig{Name: "visual fixture", Provider: models.ProviderTest, Model: "test", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, configs.Create(t.Context(), config))
	err := db.Tasks.UpdateAgentID(t.Context(), task.ID, config.ID)
	require.NoError(t, err)
	llm := NewLLMService(configs, db.Executions, db.Tasks, db.Projects, nil, nil)
	require.NotNil(t, llm.ProjectComputerService().verifyLoginVisual)
	mock := testutil.NewMockLLMCaller()
	llm.SetLLMCaller(mock)
	computer.verifyLoginVisual = llm.verifyComputerLoginVisual
	require.NoError(t, computer.repo.RequestHandoff(t.Context(), p.ID, "request_browser_login", "https://example.test/account", "Read account", "Account dashboard"))
	var screenshotPath string
	mock.OnCall = func(ctx context.Context, call testutil.MockLLMCall) {
		require.Equal(t, config.ID, call.Agent.ID)
		require.Len(t, call.Attachments, 1)
		screenshotPath = call.Attachments[0].FilePath
		data, err := os.ReadFile(screenshotPath)
		require.NoError(t, err)
		require.NotEmpty(t, data)
		require.Contains(t, call.Prompt, "untrusted")
	}
	mock.TextOnly = `{"verified":false}`
	mock.Response = mock.TextOnly
	require.Error(t, computer.CompleteLogin(t.Context(), p.ID))
	state, err := computer.Get(t.Context(), p.ID)
	require.NoError(t, err)
	require.True(t, state.Held)
	require.NoError(t, func() error { _, err := os.Stat(screenshotPath); require.True(t, os.IsNotExist(err)); return nil }())
	mock.TextOnly = `{"verified":true}`
	mock.Response = mock.TextOnly
	require.NoError(t, computer.CompleteLogin(t.Context(), p.ID))
	require.Len(t, mock.AgentRequests, 2)
	for _, request := range mock.AgentRequests {
		require.True(t, request.DisableTools)
	}
}
func TestComputerVisualVerificationRejectsForeignOrInvalidScreenshot(t *testing.T) {
	s := &ProjectComputerService{verifyLoginVisual: func(context.Context, string, string, string, []byte) (bool, error) {
		t.Fatal("invalid evidence reached model")
		return true, nil
	}}
	state := &models.ProjectComputer{HandoffURL: "https://example.test/account"}
	for _, result := range []map[string]any{{"url": "https://other.test", "screenshot": loginTestPNG}, {"url": state.HandoffURL, "screenshot": base64.StdEncoding.EncodeToString([]byte("not png"))}} {
		data, _ := json.Marshal(result)
		require.Error(t, s.verifyLoginResult(t.Context(), state, data))
	}
}
func TestComputerChromeExtendedGrantDurations(t *testing.T) {
	_, s, p, _ := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{}`)) })
	require.NoError(t, s.repo.RequestHandoff(t.Context(), p.ID, "request_chrome_login", "https://example.test", "", ""))
	picks := []models.ChromeCookieSelection{{Profile: "Default", Domain: "example.test", Name: "session", Path: "/"}}
	for _, expires := range []int64{-1, 0, time.Now().Add(30 * 24 * time.Hour).Unix()} {
		g, err := s.repo.CreateChromeGrant(t.Context(), p.ID, "project", picks, expires)
		require.NoError(t, err)
		require.NoError(t, s.repo.RecordChromeInstall(t.Context(), p.ID, g.ID))
		if expires == -1 {
			require.True(t, g.OneTime)
			require.Equal(t, "task", g.Scope)
			require.Error(t, s.repo.RecordChromeInstall(t.Context(), p.ID, g.ID))
		} else {
			require.NoError(t, s.repo.RecordChromeInstall(t.Context(), p.ID, g.ID))
		}
		expired, err := s.repo.ExpiredChromeInstallations(t.Context(), p.ID)
		require.NoError(t, err)
		require.Empty(t, expired)
		require.NoError(t, s.repo.RevokeChromeGrant(t.Context(), p.ID, g.ID))
		expired, err = s.repo.ExpiredChromeInstallations(t.Context(), p.ID)
		require.NoError(t, err)
		require.Len(t, expired, 1)
		require.NoError(t, s.repo.ClearChromeInstall(t.Context(), p.ID, g.ID))
	}
	_, err := s.repo.CreateChromeGrant(t.Context(), p.ID, "task", picks, time.Now().Add(31*24*time.Hour).Unix())
	require.Error(t, err)
}
