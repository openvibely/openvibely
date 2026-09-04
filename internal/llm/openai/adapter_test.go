package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

func TestRuntimeToolHelperMappingFilteringAndExecution(t *testing.T) {
	if got := applyOpenAIOAuthSystemPrompt("base", models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey}); got != "base" {
		t.Fatalf("non-OAuth prompt changed: %q", got)
	}
	if got := applyOpenAIOAuthSystemPrompt("base", models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth}); !strings.Contains(got, "base") || got == "base" {
		t.Fatalf("OAuth prompt missing wrapper guidance: %q", got)
	}

	mapped := map[string]string{"read_file": "Read", "write_file": "Write", "edit_file": "Edit", "bash": "Bash", "list_files": "Glob", "grep_search": "Grep", "web_search_preview": "WebSearch", "unknown": ""}
	for name, want := range mapped {
		if got := mapBuiltInToolName(" " + name + " "); got != want {
			t.Fatalf("mapBuiltInToolName(%q)=%q want %q", name, got, want)
		}
	}

	if !agentAllowsBuiltInTool(nil, "bash") {
		t.Fatal("nil agent should allow built-in tools")
	}
	if agentAllowsBuiltInTool(&models.Agent{ToolConfig: models.AgentToolConfig{SkipDefaultTools: true}}, "bash") {
		t.Fatal("SkipDefaultTools should deny built-in tools")
	}
	if !agentAllowsBuiltInTool(&models.Agent{Tools: []string{" read "}}, "read_file") {
		t.Fatal("explicit Read grant should allow read_file")
	}
	if agentAllowsBuiltInTool(&models.Agent{Tools: []string{" read "}}, "bash") {
		t.Fatal("missing Bash grant should deny bash")
	}
	if !agentAllowsBuiltInTool(&models.Agent{Tools: []string{" read "}}, "custom_runtime_tool") {
		t.Fatal("unmapped non-built-in tools should pass through")
	}

	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{
		{Name: " write_task ", Description: " write ", Access: llmcontracts.RuntimeToolAccessWrite, Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "read_task", Description: "read", Access: llmcontracts.RuntimeToolAccessRead},
		{Name: "   ", Description: "ignored"},
	}}
	openAITools := runtimeOpenAITools(rt)
	if len(openAITools) != 2 || openAITools[0].Name != "write_task" || openAITools[0].Description != "write" {
		t.Fatalf("unexpected OpenAI tools: %#v", openAITools)
	}
	if runtimeOpenAITools(nil) != nil {
		t.Fatal("nil runtime tools should produce no OpenAI tools")
	}

	baseExec := func(_ context.Context, name string, _ json.RawMessage) (string, bool, error) {
		return "base:" + name, false, nil
	}
	runtimeExec := composeRuntimeToolExecutor(baseExec, &llmcontracts.RuntimeTools{Executor: func(_ context.Context, name string, _ json.RawMessage) (string, bool, bool, error) {
		if name == "handled" {
			return "runtime handled", true, false, nil
		}
		return "", false, false, nil
	}})
	out, isErr, err := runtimeExec(context.Background(), "handled", nil)
	if out != "runtime handled" || isErr || err != nil {
		t.Fatalf("runtime handled output=%q isErr=%v err=%v", out, isErr, err)
	}
	out, isErr, err = runtimeExec(context.Background(), "fallback", nil)
	if out != "base:fallback" || isErr || err != nil {
		t.Fatalf("fallback output=%q isErr=%v err=%v", out, isErr, err)
	}
	missingExec := composeRuntimeToolExecutor(nil, &llmcontracts.RuntimeTools{Executor: func(context.Context, string, json.RawMessage) (string, bool, bool, error) {
		return "", false, false, nil
	}})
	_, isErr, err = missingExec(context.Background(), "missing", nil)
	if !isErr || err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("missing executor isErr=%v err=%v", isErr, err)
	}
	plainExec := composeRuntimeToolExecutor(baseExec, nil)
	out, _, err = plainExec(context.Background(), "plain", nil)
	if out != "base:plain" || err != nil {
		t.Fatalf("plain executor output=%q err=%v", out, err)
	}

}

func TestCallDirectUsesResponsesAPIWithAttachmentsAndUsage(t *testing.T) {
	attachmentPath := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(attachmentPath, []byte("coverage notes"), 0o644); err != nil {
		t.Fatal(err)
	}
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path=%q want /v1/responses", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatalf("unexpected authorization header %q", r.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-test","output":[{"content":[{"type":"output_text","text":"direct result"}]}],"usage":{"input_tokens":12,"output_tokens":5,"input_tokens_details":{"cached_tokens":3},"output_tokens_details":{"reasoning_tokens":2}}}`))
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	adapter := New(nil, nil, nil)
	text, usage, err := adapter.CallDirect(context.Background(), "Summarize", []models.Attachment{{FileName: "notes.txt", FilePath: attachmentPath, MediaType: "text/plain"}}, models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key", ReasoningEffort: "high"}, "", "project instructions", false, false, false)
	if err != nil {
		t.Fatalf("CallDirect: %v", err)
	}
	if text != "direct result" || usage.InputTokens != 12 || usage.OutputTokens != 5 || usage.CachedInputTokens != 3 || usage.ReasoningTokens != 2 {
		t.Fatalf("unexpected response text=%q usage=%+v", text, usage)
	}
	if gotBody["model"] != "gpt-test" || gotBody["max_output_tokens"] == nil || gotBody["instructions"] == nil {
		t.Fatalf("request missing expected fields: %#v", gotBody)
	}
	input, ok := gotBody["input"].([]any)
	if !ok || len(input) == 0 {
		t.Fatalf("request missing input items: %#v", gotBody["input"])
	}
	encodedInput, _ := json.Marshal(input)
	if !strings.Contains(string(encodedInput), "Attached files") || !strings.Contains(string(encodedInput), "notes.txt") {
		t.Fatalf("input missing attachment prompt context: %s", encodedInput)
	}
}

func TestCallCompletionsStreamingTaskWithRuntimeActionsUsesToolModePrompt(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"Task complete.\\n[STATUS: SUCCESS]\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{
			Name: "create_task", Description: "create a task", Parameters: json.RawMessage(`{"type":"object"}`), Access: llmcontracts.RuntimeToolAccessWrite,
		}},
	})
	adapter := New(nil, nil, nil)
	_, _, _, err := adapter.CallCompletionsStreaming(ctx, "Investigate the issue", nil, models.LLMConfig{
		Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key",
	}, "", ".", "", nil)
	if err != nil {
		t.Fatalf("CallCompletionsStreaming: %v", err)
	}

	messages, ok := gotBody["messages"].([]any)
	if !ok || len(messages) == 0 {
		t.Fatalf("messages = %#v", gotBody["messages"])
	}
	user, ok := messages[len(messages)-1].(map[string]any)
	if !ok {
		t.Fatalf("user message = %#v", messages[len(messages)-1])
	}
	content, _ := user["content"].(string)
	if !strings.Contains(content, "TASK CREATION TOOL MODE") || !strings.Contains(content, "Available runtime task tools: create_task") {
		t.Fatalf("task prompt missing runtime tool guidance: %q", content)
	}
	if strings.Contains(content, "This is the ONLY way to create a task") || strings.Contains(content, "To create a task, output this format") {
		t.Fatalf("task prompt retains marker-only guidance: %q", content)
	}
}

func TestAppendToolModeSystemPromptCoversTaskFollowupsAndPreservesPlan(t *testing.T) {
	followup := appendToolModeSystemPrompt("base", nil, models.ChatModeOrchestrate)
	if !strings.Contains(followup, llmprompt.ChatActionUnavailableInstructions) {
		t.Fatalf("no-tool follow-up prompt missing capability limitation: %q", followup)
	}

	rt := &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "create_task"}}}
	capable := appendToolModeSystemPrompt("base", rt, models.ChatModeOrchestrate)
	if !strings.Contains(capable, llmprompt.ChatActionToolModeInstructions) || !strings.Contains(capable, "Available action tools: create_task") {
		t.Fatalf("tool-capable follow-up prompt missing concrete runtime guidance: %q", capable)
	}

	plan := appendToolModeSystemPrompt("base", nil, models.ChatModePlan)
	if plan != "base" {
		t.Fatalf("Plan prompt received action-mode guidance: %q", plan)
	}
}

func TestToolSecondaryInfo_LongBashPreservesLaterContext(t *testing.T) {
	input := map[string]any{
		"command": "cd /Users/dubee/go/src/github.com/openvibely/openvibely/.worktrees/task_6a40e9f8fefa53ac8d203aa3fd3a70be && rg -n \"toolSecondaryInfo|truncateToolSecondary|task thread\" internal pkg web/templates/components/chat_shared.templ",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("bash", raw)
	if !strings.HasPrefix(got, "$ cd ") {
		t.Fatalf("expected bash detail prefix, got %q", got)
	}
	if !strings.Contains(got, "chat_shared.templ") {
		t.Fatalf("expected later command context to survive truncation, got %q", got)
	}
}

func TestToolSecondaryInfo_LongGrepPreservesLaterPatternContext(t *testing.T) {
	input := map[string]any{
		"pattern": "len\\(cmd\\) >|len\\(p\\) >|toolSecondaryInfo|truncateToolSecondary|task thread|chat_shared\\.templ|stream/events\\.go",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("grep_search", raw)
	if !strings.Contains(got, "chat_shared") {
		t.Fatalf("expected later grep context to survive truncation, got %q", got)
	}
}

func TestToolSecondaryInfo_WebSearchUsesURLFallback(t *testing.T) {
	input := map[string]any{
		"url": "https://www.crunchydata.com/blog/postgres-is-out-of-disk-and-how-to-recover-the-dos-and-donts",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("web_search", raw)
	if !strings.Contains(got, "crunchydata.com/blog/postgres-is-out-of-disk") {
		t.Fatalf("expected web_search secondary to include url, got %q", got)
	}
}

func TestToolSecondaryInfo_WebSearchFindInPageDetail(t *testing.T) {
	input := map[string]any{
		"action":  "findInPage",
		"pattern": "WAL files",
		"url":     "https://www.crunchydata.com/blog/postgres-is-out-of-disk-and-how-to-recover-the-dos-and-donts",
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}

	got := toolSecondaryInfo("web_search", raw)
	if !strings.Contains(got, "'WAL files' in https://www.crunchydata.com/blog/") {
		t.Fatalf("expected find-in-page detail, got %q", got)
	}
}

func TestReasoningEffortUsesModelDefaults(t *testing.T) {
	for _, tc := range []struct {
		model string
		value string
		want  string
	}{
		{model: "gpt-5.6-sol", want: "medium"},
		{model: "gpt-5.6-terra", want: "medium"},
		{model: "gpt-5.6-luna", want: "medium"},
		{model: "gpt-5.5", want: "medium"},
		{model: "gpt-5.5-pro", want: "medium"},
		{model: "gpt-5.6-sol", value: "max", want: "max"},
		{model: "gpt-5.4-mini", value: "max", want: "medium"},
		{model: "gpt-5.4-mini", value: "xhigh", want: "xhigh"},
		{model: "gpt-5.3-codex-spark", value: "xhigh", want: "xhigh"},
	} {
		if got := reasoningEffort(tc.model, tc.value); got != tc.want {
			t.Errorf("reasoningEffort(%q, %q) = %q, want %q", tc.model, tc.value, got, tc.want)
		}
	}
}

func TestResponsesTransportStateScopedByConversation(t *testing.T) {
	adapter := New(nil, nil, nil)
	agent := models.LLMConfig{ID: "cfg-1", Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol"}
	first, releaseFirst := adapter.acquireResponsesTransportState(agent, "chat:one")
	second, releaseSecond := adapter.acquireResponsesTransportState(agent, "chat:one")
	releaseFirst()
	releaseSecond()
	if first != second {
		t.Fatal("expected transport state to be reused for the same conversation")
	}
	other, releaseOther := adapter.acquireResponsesTransportState(agent, "chat:two")
	releaseOther()
	if first == other {
		t.Fatal("expected separate transport state for a different conversation")
	}
	agent.ID = "cfg-2"
	otherConfig, releaseOtherConfig := adapter.acquireResponsesTransportState(agent, "chat:one")
	releaseOtherConfig()
	if first == otherConfig {
		t.Fatal("expected separate transport state for a different model config")
	}
}

func TestResponsesTransportStateCacheEvictsOldestEntry(t *testing.T) {
	adapter := New(nil, nil, nil)
	agent := models.LLMConfig{ID: "cfg-1", Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol"}
	oldest, releaseOldest := adapter.acquireResponsesTransportState(agent, "chat:oldest")
	releaseOldest()
	adapter.transportMu.Lock()
	for key, entry := range adapter.transportStates {
		if entry.state == oldest {
			entry.lastUsed = time.Time{}
			adapter.transportStates[key] = entry
			break
		}
	}
	adapter.transportMu.Unlock()
	for i := 0; i < responsesTransportMax; i++ {
		_, release := adapter.acquireResponsesTransportState(agent, fmt.Sprintf("chat:%d", i))
		release()
	}
	if got := len(adapter.transportStates); got != responsesTransportMax {
		t.Fatalf("transport cache size = %d, want %d", got, responsesTransportMax)
	}
	replacement, releaseReplacement := adapter.acquireResponsesTransportState(agent, "chat:oldest")
	releaseReplacement()
	if replacement == oldest {
		t.Fatal("expected oldest transport state to be evicted")
	}
}

func TestResponsesTransportStateCacheDoesNotEvictLeasedEntry(t *testing.T) {
	adapter := New(nil, nil, nil)
	agent := models.LLMConfig{ID: "cfg-1", Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol"}
	active, releaseActive := adapter.acquireResponsesTransportState(agent, "chat:active")
	for i := 0; i < responsesTransportMax; i++ {
		_, release := adapter.acquireResponsesTransportState(agent, fmt.Sprintf("chat:%d", i))
		release()
	}
	stillActive, releaseAgain := adapter.acquireResponsesTransportState(agent, "chat:active")
	if stillActive != active {
		t.Fatal("leased transport state was evicted")
	}
	releaseAgain()
	releaseActive()
}

func TestResponsesTransportStateCacheExpiresIdleEntriesOnHit(t *testing.T) {
	adapter := New(nil, nil, nil)
	agent := models.LLMConfig{ID: "cfg-1", Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol"}
	stale, releaseStale := adapter.acquireResponsesTransportState(agent, "chat:stale")
	releaseStale()
	_, releaseCurrent := adapter.acquireResponsesTransportState(agent, "chat:current")
	releaseCurrent()
	adapter.transportMu.Lock()
	for _, entry := range adapter.transportStates {
		if entry.state == stale {
			entry.lastUsed = time.Time{}
		}
	}
	adapter.transportMu.Unlock()
	_, releaseHit := adapter.acquireResponsesTransportState(agent, "chat:current")
	releaseHit()
	replacement, releaseReplacement := adapter.acquireResponsesTransportState(agent, "chat:stale")
	releaseReplacement()
	if replacement == stale {
		t.Fatal("idle entry was not expired during cache hit")
	}
}

func TestResponsesTransportStateChangesWithCredentials(t *testing.T) {
	adapter := New(nil, nil, nil)
	agent := models.LLMConfig{
		ID: "cfg-1", Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol",
		AuthMethod: models.AuthMethodAPIKey, APIKey: "first-key",
	}
	first, releaseFirst := adapter.acquireResponsesTransportState(agent, "chat:one")
	releaseFirst()
	agent.APIKey = "second-key"
	second, releaseSecond := adapter.acquireResponsesTransportState(agent, "chat:one")
	releaseSecond()
	if first == second {
		t.Fatal("expected API key rotation to use a new transport state")
	}

	agent.AuthMethod = models.AuthMethodOAuth
	agent.OAuthAccountID = "account-one"
	oauthFirst, releaseOAuthFirst := adapter.acquireResponsesTransportState(agent, "chat:one")
	releaseOAuthFirst()
	agent.OAuthAccessToken = "refreshed-token"
	oauthRefreshed, releaseOAuthRefreshed := adapter.acquireResponsesTransportState(agent, "chat:one")
	releaseOAuthRefreshed()
	if oauthFirst != oauthRefreshed {
		t.Fatal("expected token refresh for the same OAuth account to reuse transport state")
	}
	agent.OAuthAccountID = "account-two"
	otherAccount, releaseOtherAccount := adapter.acquireResponsesTransportState(agent, "chat:one")
	releaseOtherAccount()
	if oauthFirst == otherAccount {
		t.Fatal("expected OAuth account change to use a new transport state")
	}
}

func TestPrivateResponsesTransportClosesWhenReleased(t *testing.T) {
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		completed := `{"type":"response.completed","response":{"id":"resp-1","status":"completed","model":"gpt-5.6-sol","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(completed)); err != nil {
			t.Errorf("write response: %v", err)
			return
		}
		_, _, _ = conn.Read(r.Context())
		close(closed)
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	adapter := New(nil, nil, nil)
	agent := models.LLMConfig{
		ID: "cfg-1", Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol",
		AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key",
	}
	client, release, err := adapter.getClient(context.Background(), agent, "")
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	response, err := client.Send(context.Background(), "hello", &openaiclient.SendOptions{Model: agent.Model})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if response.Text != "ok" {
		t.Fatalf("response text = %q", response.Text)
	}
	release()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("private WebSocket remained open after transport release")
	}
}

func TestLeasedResponsesTransportSurvivesConcurrentCachePressure(t *testing.T) {
	started := make(chan struct{})
	finish := make(chan struct{})
	closed := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept websocket: %v", err)
			return
		}
		if _, _, err := conn.Read(r.Context()); err != nil {
			t.Errorf("read request: %v", err)
			return
		}
		close(started)
		<-finish
		completed := `{"type":"response.completed","response":{"id":"resp-1","status":"completed","model":"gpt-5.6-sol","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}}`
		if err := conn.Write(r.Context(), websocket.MessageText, []byte(completed)); err != nil {
			t.Errorf("write response: %v", err)
			return
		}
		_, _, _ = conn.Read(r.Context())
		close(closed)
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	adapter := New(nil, nil, nil)
	agent := models.LLMConfig{
		ID: "cfg-1", Provider: models.ProviderOpenAI, Model: "gpt-5.6-sol",
		AuthMethod: models.AuthMethodAPIKey, APIKey: "test-key",
	}
	client, releaseActive, err := adapter.getClient(context.Background(), agent, "chat:active")
	if err != nil {
		t.Fatalf("getClient: %v", err)
	}
	adapter.transportMu.Lock()
	var expectedActive *openaiclient.ResponsesTransportState
	for _, entry := range adapter.transportStates {
		if entry.leases == 1 {
			expectedActive = entry.state
			break
		}
	}
	adapter.transportMu.Unlock()
	sendDone := make(chan error, 1)
	go func() {
		_, sendErr := client.Send(context.Background(), "hello", &openaiclient.SendOptions{Model: agent.Model})
		sendDone <- sendErr
	}()
	<-started
	for i := 0; i < responsesTransportMax; i++ {
		_, release := adapter.acquireResponsesTransportState(agent, fmt.Sprintf("chat:pressure-%d", i))
		release()
	}
	activeAgain, releaseAgain := adapter.acquireResponsesTransportState(agent, "chat:active")
	if activeAgain != expectedActive {
		t.Fatal("active transport state was replaced under cache pressure")
	}
	releaseAgain()
	close(finish)
	if err := <-sendDone; err != nil {
		t.Fatalf("Send: %v", err)
	}
	releaseActive()

	adapter.transportMu.Lock()
	for _, entry := range adapter.transportStates {
		if entry.state == activeAgain {
			entry.lastUsed = time.Time{}
		}
	}
	adapter.transportMu.Unlock()
	_, releaseTrigger := adapter.acquireResponsesTransportState(agent, "chat:trigger-cleanup")
	releaseTrigger()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("evicted leased WebSocket was not closed after its final release")
	}
}

func TestInitialTaskAndFollowupShareTransportScope(t *testing.T) {
	db := testutil.NewTestDB(t)
	taskRepo := repository.NewTaskRepo(db, nil)
	execRepo := repository.NewExecutionRepo(db)
	ctx := context.Background()
	task := &models.Task{
		ProjectID: "default", Title: "Transport reuse", Category: models.CategoryActive,
		Status: models.StatusPending, Prompt: "test",
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning, PromptSent: "initial"}
	if err := execRepo.Create(ctx, execution); err != nil {
		t.Fatalf("create execution: %v", err)
	}

	adapter := New(nil, execRepo, nil)
	initialScope := adapter.taskTransportScope(ctx, execution.ID)
	followupScope := "task:" + task.ID
	if initialScope != "task:"+task.ID || followupScope != initialScope {
		t.Fatalf("scopes initial=%q followup=%q, want task scope", initialScope, followupScope)
	}
	agent := models.LLMConfig{ID: "cfg-1", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, APIKey: "key"}
	initialState, releaseInitial := adapter.acquireResponsesTransportState(agent, initialScope)
	releaseInitial()
	followupState, releaseFollowup := adapter.acquireResponsesTransportState(agent, followupScope)
	releaseFollowup()
	if initialState != followupState {
		t.Fatal("initial task and follow-up did not reuse the same transport state")
	}
}

func TestTaskStreamingRuntimeToolComposition_AllowsScopedFilesRuntimeTools(t *testing.T) {
	rt := &llmcontracts.RuntimeTools{
		SkipDefaultTools: true,
		Definitions: []llmcontracts.RuntimeToolDefinition{
			{Name: "list_files"},
		},
		Filter: func(name string) (bool, bool) {
			if name == "list_files" {
				return true, true
			}
			return false, false
		},
	}

	extraTools := runtimeOpenAITools(rt)
	if len(extraTools) != 1 || extraTools[0].Name != "list_files" {
		t.Fatalf("expected runtime tool definition to be exposed, got %#v", extraTools)
	}

}

func TestApplyOpenAIOAuthSystemPrompt_OAuthAppendsWorkingSection(t *testing.T) {
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth}
	base := "base system prompt"

	got := applyOpenAIOAuthSystemPrompt(base, agent)

	if !strings.Contains(got, base) {
		t.Fatalf("expected base prompt to be preserved")
	}
	if !strings.Contains(got, "# Working with the user") {
		t.Fatalf("expected oauth prompt to include working-with-user section, got %q", got)
	}
	if !strings.Contains(got, "Share intermediary updates in `commentary` channel.") {
		t.Fatalf("expected oauth prompt to include intermediary update guidance")
	}
}

func TestApplyOpenAIOAuthSystemPrompt_APIKeyDoesNotAppendWorkingSection(t *testing.T) {
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey}
	base := "base system prompt"

	got := applyOpenAIOAuthSystemPrompt(base, agent)
	if got != base {
		t.Fatalf("expected api key prompt to remain unchanged, got %q", got)
	}
}

func TestApplyOpenAIOAuthSystemPrompt_OAuthNoDuplicateAppend(t *testing.T) {
	agent := models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodOAuth}
	base := applyOpenAIOAuthSystemPrompt("base system prompt", agent)

	got := applyOpenAIOAuthSystemPrompt(base, agent)

	if strings.Count(got, "# Working with the user") != 1 {
		t.Fatalf("expected working-with-user section to appear once, got %d", strings.Count(got, "# Working with the user"))
	}
}

// A lifecycle hook on the OpenAI direct path must receive its own agent prompt
// (the provider wrapper folds the agent definition into ProjectInstructions)
// while skipping the shared coding-agent framing. Before this, CallDirect built
// its system prompt from "" and system agents ran with no identity at all.
func TestCallDirectLifecycleHookSendsAgentPromptWithoutCodingFraming(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","status":"completed","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"{}"}]}]}`))
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	adapter := New(nil, nil, nil)
	_, _, err := adapter.CallDirect(context.Background(), "HOOK PROMPT", nil, models.LLMConfig{
		Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key",
	}, ".", "SENTINEL_AGENT_PROMPT", true, false, true)
	if err != nil {
		t.Fatalf("CallDirect: %v", err)
	}

	payload, _ := json.Marshal(gotBody)
	body := string(payload)
	if !strings.Contains(body, "SENTINEL_AGENT_PROMPT") {
		t.Fatalf("lifecycle hook lost its agent prompt: %s", body)
	}
	if strings.Contains(body, "expert software engineer") {
		t.Fatalf("lifecycle hook must not receive the coding-agent system prompt: %s", body)
	}
}

// Ordinary direct calls keep both the agent prompt and the coding-agent framing.
func TestCallDirectNonLifecycleKeepsCodingFraming(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","status":"completed","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}]}`))
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	adapter := New(nil, nil, nil)
	_, _, err := adapter.CallDirect(context.Background(), "DO WORK", nil, models.LLMConfig{
		Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key",
	}, ".", "SENTINEL_AGENT_PROMPT", true, false, false)
	if err != nil {
		t.Fatalf("CallDirect: %v", err)
	}

	payload, _ := json.Marshal(gotBody)
	body := string(payload)
	for _, want := range []string{"SENTINEL_AGENT_PROMPT", "expert software engineer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("ordinary direct call missing %q: %s", want, body)
		}
	}
}

func TestCallDirectRawPromptOmitsInteractiveAgentFraming(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"resp-1","status":"completed","model":"gpt-test","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Centralize channel mutation responses"}]}]}`))
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	adapter := New(nil, nil, nil)
	_, _, err := adapter.CallDirect(context.Background(), "COMMIT SUMMARY PROMPT", nil, models.LLMConfig{
		Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key",
	}, ".", "SENTINEL_PROJECT_INSTRUCTIONS", true, true, false)
	if err != nil {
		t.Fatalf("CallDirect: %v", err)
	}

	payload, _ := json.Marshal(gotBody)
	body := string(payload)
	for _, unwanted := range []string{"expert software engineer", "SENTINEL_PROJECT_INSTRUCTIONS", "Working with the user", "Intermediary updates"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("raw direct request contains interactive framing %q: %s", unwanted, body)
		}
	}
	if !strings.Contains(body, "COMMIT SUMMARY PROMPT") {
		t.Fatalf("raw direct request lost caller prompt: %s", body)
	}
}

func TestCallStreamingUsesAgenticResponsesCallbacksAndUsage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Fatalf("path=%q want /v1/responses", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"type":"response.reasoning_summary_text.delta","delta":"thinking"}` + "\n\n" +
				`data: {"type":"response.output_text.delta","delta":"Task done."}` + "\n\n" +
				`data: {"type":"response.completed","response":{"id":"resp_stream","status":"completed","model":"gpt-test","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Task done."}]}],"usage":{"input_tokens":21,"output_tokens":9,"input_tokens_details":{"cached_tokens":4},"output_tokens_details":{"reasoning_tokens":3}}}}` + "\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = oldBaseURL }()

	adapter := New(nil, nil, nil)
	out, textOnly, usage, err := adapter.CallStreaming(context.Background(), "Finish this", nil, models.LLMConfig{
		Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key", ReasoningEffort: "medium",
	}, "exec-agentic-stream", "", "project instructions", nil)
	if err != nil {
		t.Fatalf("CallStreaming: %v", err)
	}
	if !strings.Contains(out, "Task done.") || textOnly != "Task done." {
		t.Fatalf("unexpected streaming output=%q textOnly=%q", out, textOnly)
	}
	if usage.InputTokens != 21 || usage.OutputTokens != 9 || usage.CachedInputTokens != 4 || usage.ReasoningTokens != 3 {
		t.Fatalf("unexpected usage: %+v", usage)
	}
	if gotBody["model"] != "gpt-test" || gotBody["instructions"] == nil || gotBody["tools"] == nil {
		t.Fatalf("request missing expected agentic fields: %#v", gotBody)
	}
	encodedInput, _ := json.Marshal(gotBody["input"])
	if !strings.Contains(string(encodedInput), "Finish this") || !strings.Contains(string(encodedInput), "STATUS: SUCCESS") {
		t.Fatalf("input missing task/status prompt: %s", encodedInput)
	}
}

func TestCallChatStreamingUsesHistoryRuntimeAndDisableToolsPolicy(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			`data: {"type":"response.reasoning_summary_text.delta","delta":"reviewing"}` + "\n\n" +
				`data: {"type":"response.output_text.delta","delta":"Chat answer"}` + "\n\n" +
				`data: {"type":"response.completed","response":{"id":"resp_chat","status":"completed","model":"gpt-test","output":[{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"output_text","text":"Chat answer"}]}],"usage":{"input_tokens":13,"output_tokens":7}}}` + "\n\n",
		))
	}))
	defer srv.Close()

	oldBaseURL := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = oldBaseURL }()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "list_tasks", Description: "list", Access: llmcontracts.RuntimeToolAccessRead}}})
	adapter := New(nil, nil, nil)
	out, usage, err := adapter.CallChatStreaming(ctx, "What changed?", nil, models.LLMConfig{
		Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key",
	}, "exec-chat-stream", "transport-scope", []models.Execution{{PromptSent: "previous prompt", Output: "previous output", Status: models.ExecCompleted}}, "project chat context", false, models.ChatModePlan, "", nil)
	if err != nil {
		t.Fatalf("CallChatStreaming: %v", err)
	}
	if !strings.Contains(out, "Chat answer") || usage.InputTokens != 13 || usage.OutputTokens != 7 {
		t.Fatalf("unexpected chat output=%q usage=%+v", out, usage)
	}
	if gotBody["model"] != "gpt-test" || gotBody["instructions"] == nil || gotBody["tools"] == nil {
		t.Fatalf("chat request missing model/instructions/tools: %#v", gotBody)
	}
	if gotBody["tool_choice"] != nil {
		t.Fatalf("plan-mode runtime tools should not disable tools: %#v", gotBody)
	}
	encoded, _ := json.Marshal(gotBody["input"])
	if !strings.Contains(string(encoded), "What changed?") || !strings.Contains(string(encoded), "previous prompt") {
		t.Fatalf("chat input missing message/history: %s", encoded)
	}
}

func TestCallCompletionsChatStreamingUsesHistoryRuntimeAndUsage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte(
			"data: {\"choices\":[{\"delta\":{\"content\":\"chat chunk\"}}]}\n\n" +
				"data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":13,\"completion_tokens\":6,\"prompt_tokens_details\":{\"cached_tokens\":4},\"completion_tokens_details\":{\"reasoning_tokens\":2}}}\n\n" +
				"data: [DONE]\n\n",
		))
	}))
	defer srv.Close()

	original := openaiclient.OpenAIAPIBaseURL
	openaiclient.OpenAIAPIBaseURL = srv.URL + "/v1/"
	defer func() { openaiclient.OpenAIAPIBaseURL = original }()

	ctx := llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "list_tasks", Description: "List tasks", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	adapter := New(nil, nil, nil)
	output, usage, err := adapter.CallCompletionsChatStreaming(ctx, "What changed?", nil, models.LLMConfig{
		Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-test", APIKey: "test-key",
	}, "exec-completions-chat", []models.Execution{{PromptSent: "Earlier prompt", Output: "Earlier answer", Status: models.ExecCompleted}}, "CHAT_CONTEXT_SENTINEL", true, models.ChatModeOrchestrate, "/repo/worktree", nil)
	if err != nil {
		t.Fatalf("CallCompletionsChatStreaming: %v", err)
	}
	if !strings.Contains(output, "chat chunk") {
		t.Fatalf("output = %q", output)
	}
	if usage.InputTokens != 13 || usage.OutputTokens != 6 || usage.CachedInputTokens != 4 || usage.ReasoningTokens != 2 {
		t.Fatalf("usage = %#v", usage)
	}
	payload := fmt.Sprint(gotBody)
	for _, want := range []string{"Earlier prompt", "Earlier answer", "What changed?", "CHAT_CONTEXT_SENTINEL", "list_tasks"} {
		if !strings.Contains(payload, want) {
			t.Fatalf("request body missing %q: %#v", want, gotBody)
		}
	}
	if !strings.Contains(payload, llmprompt.ChatActionToolModeInstructions) {
		t.Fatalf("chat completions prompt missing runtime action guidance: %#v", gotBody)
	}
}
