package handler

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestComputerWorkflowStreamsAndCancelsProvider(t *testing.T) {
	t.Setenv("OPENVIBELY_ALLOW_PRIVATE_MODEL_ENDPOINTS", "true")
	cancelled := make(chan struct{}, 1)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		require.NoError(t, json.NewDecoder(r.Body).Decode(&request))
		require.Equal(t, true, request["stream"])
		require.Empty(t, request["tools"])
		w.Header().Set("Content-Type", "text/event-stream")
		chunk, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]string{"content": "{\"name\":\"Report"}}}})
		fmt.Fprintf(w, "data: %s\n\n", chunk)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	defer provider.Close()
	db := teststores.New(t)
	config := &models.LLMConfig{Name: "Fixture streaming", Provider: models.ProviderOpenAICompatible, Model: "fixture", AuthMethod: models.AuthMethodAPIKey, APIKey: "fixture-only", BaseURL: provider.URL + "/v1/", Transport: "chat_completions", PresetSlug: "vllm"}
	require.NoError(t, db.LLMConfigs.Create(t.Context(), config))
	p := &models.Project{Name: "Stream", DefaultAgentConfigID: &config.ID}
	require.NoError(t, db.Projects.Create(t.Context(), p))
	svc := service.NewProjectComputerService(db.Projects, liveComputerFixture{})
	require.NoError(t, db.Projects.Computers().Configure(t.Context(), p.ID, "local"))
	require.NoError(t, db.Projects.Computers().Hold(t.Context(), p.ID, true))
	llm := service.NewLLMService(db.LLMConfigs, db.Executions, db.Tasks, db.Projects, db.Schedules, db.Attachments)
	h := &Handler{projectRepo: db.Projects, computerSvc: svc, llmSvc: llm}
	e := echo.New()
	e.POST("/projects/:id/computer/teach", h.ProjectComputerTeach)
	server := httptest.NewServer(e)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", server.URL+"/projects/"+p.ID+"/computer/teach", bytes.NewBufferString(`{"goal":"Report","steps":["Open reports"]}`))
	require.NoError(t, err)
	req.Header.Set("Accept", "application/x-ndjson")
	res, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer res.Body.Close()
	scanner := bufio.NewScanner(res.Body)
	require.True(t, scanner.Scan())
	require.Contains(t, scanner.Text(), `"text"`)
	require.Contains(t, scanner.Text(), "Report")
	cancel()
	select {
	case <-cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("closing generation did not cancel provider request")
	}
}
