package service

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestLLMService_ReconcileMissingTaskAttachmentsRemovesBrokenMetadata(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, events.NewBroadcaster())
	attachmentRepo := repository.NewAttachmentRepo(db)
	project := &models.Project{Name: "Broken attachment project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	task := &models.Task{ProjectID: project.ID, Title: "Task with historical broken screenshot", Prompt: "still execute", Category: models.CategoryBacklog, Status: models.StatusPending}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	missing := &models.Attachment{TaskID: task.ID, FileName: "Screenshot 2026-07-10 at 9.49.00\u202fPM.png", FilePath: filepath.Join(t.TempDir(), "missing.png"), MediaType: "image/png", FileSize: 10}
	if err := attachmentRepo.Create(ctx, missing); err != nil {
		t.Fatal(err)
	}

	svc := NewLLMService(repository.NewLLMConfigRepo(db), repository.NewExecutionRepo(db), taskRepo, projectRepo, nil, attachmentRepo)
	valid := svc.reconcileMissingTaskAttachments(ctx, task.ID, []models.Attachment{*missing})
	if len(valid) != 0 {
		t.Fatalf("expected broken attachment to be excluded from execution, got %#v", valid)
	}
	remaining, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("expected broken metadata cleanup, got %#v", remaining)
	}
}

func TestLLMService_ImageAttachments_TextOnlyAgentRoutesToVisionAgent(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	// Create repos
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Create a test project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	// Create a text-only agent (no vision support)
	textOnlyAgent := &models.LLMConfig{
		Name:       "Text Only Compatible",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-compatible-key",
		Model:      "text-only-compatible",
		MaxTokens:  4096,
	}
	if err := llmConfigRepo.Create(ctx, textOnlyAgent); err != nil {
		t.Fatalf("Failed to create text-only agent: %v", err)
	}

	// Create a vision-capable API agent
	apiAgent := &models.LLMConfig{
		Name:       "Claude API with Vision",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-api-key",
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
	}
	if err := llmConfigRepo.Create(ctx, apiAgent); err != nil {
		t.Fatalf("Failed to create API agent: %v", err)
	}

	// Create task with image attachment
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Analyze Screenshot",
		Prompt:    "What do you see in this screenshot?",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		AgentID:   &textOnlyAgent.ID, // Explicitly use text-only agent
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create a temporary PNG file for testing
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "screenshot.png")
	if err := os.WriteFile(imgPath, []byte("fake PNG content"), 0644); err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}

	// Attach the PNG
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "screenshot.png",
		FilePath:  imgPath,
		MediaType: "image/png",
		FileSize:  100,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	// Create LLM service
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)

	// Mock LLM caller
	mockCaller := testutil.NewMockLLMCaller()
	svc.SetLLMCaller(mockCaller)

	// Load the task with attachments
	loadedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load task: %v", err)
	}

	// Trigger vision routing
	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load attachments: %v", err)
	}

	visionDecision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "Test", task.ID)

	// Verify that the agent was switched to a vision-capable agent
	if !visionDecision.Changed {
		t.Errorf("Expected vision routing to switch agent, but it didn't. Reason: %s, Detail: %s", visionDecision.Reason, visionDecision.Detail)
	}

	if visionDecision.Agent.ID != apiAgent.ID {
		t.Errorf("Expected agent to be switched to API agent %s, but got %s", apiAgent.ID, visionDecision.Agent.ID)
	}

	// Now test the case where no vision agent is available
	// Delete the vision-capable agent
	if err := llmConfigRepo.Delete(ctx, apiAgent.ID); err != nil {
		t.Fatalf("Failed to delete API agent: %v", err)
	}

	// Clear any environment variable fallback
	oldAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	os.Setenv("ANTHROPIC_API_KEY", "")
	defer os.Setenv("ANTHROPIC_API_KEY", oldAPIKey)

	// Try vision routing again
	visionDecision2 := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "Test", task.ID)

	// Verify that no agent was switched but a warning reason is provided
	if visionDecision2.Changed {
		t.Errorf("Expected no agent switch when no vision agents available, but agent was switched")
	}

	if visionDecision2.Reason != "no_vision_fallback_available" {
		t.Errorf("Expected reason 'no_vision_fallback_available', got %s", visionDecision2.Reason)
	}

	// The detail should explain the situation
	if !strings.Contains(visionDecision2.Detail, "vision-capable") {
		t.Errorf("Expected detail to mention vision-capable agents, got: %s", visionDecision2.Detail)
	}
}

func TestLLMService_ImageAttachments_VisionRoutingKeepsNoQueryFastPaths(t *testing.T) {
	ctx := context.Background()
	db, counter := testutil.NewStatementCountingTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}
	textOnly := &models.LLMConfig{
		Name:       "Fast-path text model",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "text-key",
		Model:      "text-only-model",
	}
	vision := &models.LLMConfig{
		Name:       "Fast-path vision model",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "vision-key",
		Model:      "claude-sonnet-4-5-20250929",
	}
	for _, cfg := range []*models.LLMConfig{textOnly, vision} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	svc := NewLLMService(llmConfigRepo, nil, nil, nil, nil, nil)
	cases := []struct {
		name        string
		attachments []models.Attachment
		agent       models.LLMConfig
		reason      string
	}{
		{name: "no attachments", reason: "no_attachments", agent: *textOnly},
		{name: "non-image attachment", attachments: []models.Attachment{{MediaType: "text/plain"}}, reason: "no_image_attachments", agent: *textOnly},
		{name: "already vision capable", attachments: []models.Attachment{{MediaType: "image/png"}}, agent: *vision, reason: "agent_already_vision_capable"},
		{
			name: "already vision capable Anthropic OAuth",
			agent: models.LLMConfig{
				Name:             "Fast-path Anthropic OAuth model",
				Provider:         models.ProviderAnthropic,
				AuthMethod:       models.AuthMethodOAuth,
				OAuthAccessToken: "anthropic-access-token",
				Model:            "claude-sonnet-4-5-20250929",
			},
			attachments: []models.Attachment{{MediaType: "image/png"}},
			reason:      "agent_already_vision_capable",
		},
		{
			name: "already vision capable OpenAI API",
			agent: models.LLMConfig{
				Name:       "Fast-path OpenAI API model",
				Provider:   models.ProviderOpenAI,
				AuthMethod: models.AuthMethodAPIKey,
				APIKey:     "openai-api-key",
				Model:      "gpt-4o",
			},
			attachments: []models.Attachment{{MediaType: "image/png"}},
			reason:      "agent_already_vision_capable",
		},
		{
			name: "already vision capable OpenAI OAuth",
			agent: models.LLMConfig{
				Name:             "Fast-path OpenAI OAuth model",
				Provider:         models.ProviderOpenAI,
				AuthMethod:       models.AuthMethodOAuth,
				OAuthAccessToken: "openai-access-token",
				Model:            "gpt-4o",
			},
			attachments: []models.Attachment{{MediaType: "image/png"}},
			reason:      "agent_already_vision_capable",
		},
		{
			name: "already vision capable Ollama",
			agent: models.LLMConfig{
				Name:       "Fast-path Ollama model",
				Provider:   models.ProviderOllama,
				AuthMethod: models.AuthMethodAPIKey,
				Model:      "llava",
			},
			attachments: []models.Attachment{{MediaType: "image/png"}},
			reason:      "agent_already_vision_capable",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			decision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, "describe the attachment", tc.attachments, tc.agent, "Test", "")
			counter.SetEnabled(false)
			if decision.Reason != tc.reason {
				t.Fatalf("reason = %q, want %q", decision.Reason, tc.reason)
			}
			if decision.Changed || decision.Agent != tc.agent {
				t.Fatalf("fast path decision = %#v, want unchanged agent %#v", decision, tc.agent)
			}
			if statements := counter.Statements(); len(statements) != 0 {
				t.Fatalf("fast path executed model queries: %#v", statements)
			}
		})
	}
}

func TestLLMService_ImageAttachments_VisionRoutingPreservesCompactSelectionSemantics(t *testing.T) {
	ctx := context.Background()
	db, counter := testutil.NewStatementCountingTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	legacyCLI := &models.LLMConfig{
		Name:       "Legacy CLI Default",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodCLI,
		Model:      "claude-opus-5-20250929",
		IsDefault:  true,
	}
	apiComplex := &models.LLMConfig{
		Name:          "API Complex",
		Provider:      models.ProviderAnthropic,
		AuthMethod:    models.AuthMethodAPIKey,
		APIKey:        "api-complex-key",
		Model:         "claude-opus-5-20250929",
		ExtraBodyJSON: `{"selected":"api-complex"}`,
		BaseURL:       "https://anthropic.example/api-complex",
	}
	oauthModerate := &models.LLMConfig{
		Name:              "OAuth Moderate",
		Provider:          models.ProviderAnthropic,
		AuthMethod:        models.AuthMethodOAuth,
		OAuthAccessToken:  "oauth-moderate-access",
		OAuthRefreshToken: "oauth-moderate-refresh",
		Model:             "claude-sonnet-5-20250929",
		ExtraBodyJSON:     `{"selected":"oauth-moderate"}`,
	}
	apiSimple := &models.LLMConfig{
		Name:          "API Simple",
		Provider:      models.ProviderAnthropic,
		AuthMethod:    models.AuthMethodAPIKey,
		APIKey:        "api-simple-key",
		Model:         "claude-haiku-4-5-20250929",
		ExtraBodyJSON: `{"selected":"api-simple"}`,
	}
	for _, cfg := range []*models.LLMConfig{legacyCLI, apiComplex, oauthModerate, apiSimple} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	svc := NewLLMService(llmConfigRepo, nil, nil, nil, nil, nil)
	textOnly := models.LLMConfig{
		Name:       "Text-only current model",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "text-only-key",
		Model:      "text-only-model",
	}
	assertRoutingQueries := func(t *testing.T) {
		t.Helper()
		statements := counter.Statements()
		if len(statements) != 2 {
			t.Fatalf("vision routing statements = %#v, want compact selection plus one detail lookup", statements)
		}
		compactQueries, detailQueries := 0, 0
		for _, raw := range statements {
			stmt := strings.ToLower(strings.Join(strings.Fields(raw), " "))
			if strings.Contains(stmt, "order by is_default desc, name asc") {
				compactQueries++
				projection := strings.Split(stmt, " from agent_configs ")[0]
				for _, forbidden := range []string{"oauth_refresh_token", "oauth_client_secret", "base_url", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "created_at", "updated_at", "max_tokens", "temperature"} {
					if strings.Contains(projection, forbidden) {
						t.Fatalf("vision selection query selected forbidden column %q: %s", forbidden, raw)
					}
				}
			}
			if strings.Contains(stmt, "from agent_configs where id = ?") {
				detailQueries++
			}
		}
		if compactQueries != 1 || detailQueries != 1 {
			t.Fatalf("vision routing queries = compact %d, detail %d; statements=%#v", compactQueries, detailQueries, statements)
		}
	}

	cases := []struct {
		name       string
		prompt     string
		wantID     string
		wantBody   string
		wantReason string
	}{
		{name: "simple tier selects API key", prompt: "rename x", wantID: apiSimple.ID, wantBody: apiSimple.ExtraBodyJSON, wantReason: "vision_agent_selected"},
		{name: "moderate tier selects OAuth", prompt: "please implement this feature while preserving existing behavior and validating the image routing path across both task execution and direct chat streaming", wantID: oauthModerate.ID, wantBody: oauthModerate.ExtraBodyJSON, wantReason: "vision_agent_selected"},
		{name: "complex tier selects API key", prompt: "Design a comprehensive architecture strategy across files with a migration", wantID: apiComplex.ID, wantBody: apiComplex.ExtraBodyJSON, wantReason: "vision_agent_selected"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			counter.Reset()
			counter.SetEnabled(true)
			decision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, tc.prompt, []models.Attachment{{MediaType: "image/png"}}, textOnly, "Test", "")
			counter.SetEnabled(false)
			if decision.Reason != tc.wantReason || !decision.Changed {
				t.Fatalf("decision = %#v, want changed vision selection", decision)
			}
			if decision.Agent.ID != tc.wantID || decision.Agent.ExtraBodyJSON != tc.wantBody {
				t.Fatalf("selected agent = %#v, want fully hydrated %s", decision.Agent, tc.wantID)
			}
			assertRoutingQueries(t)
		})
	}

	if err := llmConfigRepo.Delete(ctx, apiComplex.ID); err != nil {
		t.Fatalf("delete API complex model: %v", err)
	}
	if err := llmConfigRepo.Delete(ctx, oauthModerate.ID); err != nil {
		t.Fatalf("delete OAuth moderate model: %v", err)
	}
	if err := llmConfigRepo.Delete(ctx, apiSimple.ID); err != nil {
		t.Fatalf("delete API simple model: %v", err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "environment-vision-key")
	counter.Reset()
	counter.SetEnabled(true)
	decision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, "describe this screenshot", []models.Attachment{{MediaType: "image/png"}}, textOnly, "Test", "")
	counter.SetEnabled(false)
	if !decision.Changed || decision.Reason != "vision_env_fallback" || decision.Agent.APIKey != "environment-vision-key" {
		t.Fatalf("environment fallback decision = %#v", decision)
	}
	if len(counter.Statements()) != 1 {
		t.Fatalf("environment fallback should scan compact rows once without a detail lookup: %#v", counter.Statements())
	}
	if strings.Contains(strings.ToLower(strings.Join(strings.Fields(counter.Statements()[0]), " ")), "oauth_refresh_token") {
		t.Fatalf("environment fallback used a full model projection: %s", counter.Statements()[0])
	}
}

func TestLLMService_ImageAttachments_VisionRoutingHydratesStoredConfigForStreaming(t *testing.T) {
	ctx := context.Background()
	db, counter := testutil.NewStatementCountingTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	textOnly := &models.LLMConfig{
		Name:       "Text-only local model",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "text-only-key",
		Model:      "local-text-model",
	}
	vision := &models.LLMConfig{
		Name:                 "Stored Anthropic Vision",
		Provider:             models.ProviderAnthropic,
		AuthMethod:           models.AuthMethodAPIKey,
		APIKey:               "stored-api-key",
		OAuthAccessToken:     "stored-oauth-access",
		OAuthRefreshToken:    "stored-oauth-refresh",
		OAuthClientID:        "stored-client-id",
		OAuthClientSecret:    "stored-client-secret",
		Model:                "claude-sonnet-4-5-20250929",
		BaseURL:              "https://anthropic.example/v1",
		Transport:            "messages",
		ExtraHeadersJSON:     `{"x-provider-secret":"header"}`,
		ExtraBodyJSON:        `{"provider_setting":"body"}`,
		CustomAuthConfigJSON: `{"signing_secret":"custom-config"}`,
		CustomAuthStateJSON:  `{"access":"custom-state"}`,
		MixtureConfigJSON:    `{"enabled":true,"aggregator":"stored"}`,
		MaxTokens:            4096,
	}
	for _, cfg := range []*models.LLMConfig{textOnly, vision} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	svc := NewLLMService(llmConfigRepo, repository.NewExecutionRepo(db), repository.NewTaskRepo(db, nil), nil, nil, nil)
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderAnthropic: capture}
	attachments := []models.Attachment{{FileName: "screenshot.png", MediaType: "image/png"}}

	counter.Reset()
	counter.SetEnabled(true)
	_, err := svc.CallAgentDirectStreamingDetailed(ctx, "What is in this screenshot?", attachments, *textOnly, "streaming-exec", nil, "", "", nil)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("CallAgentDirectStreamingDetailed: %v", err)
	}

	requests := capture.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want one request", len(requests))
	}
	got := requests[0].Agent
	if got.ID != vision.ID || got.Name != vision.Name || got.Provider != vision.Provider || got.Model != vision.Model || got.AuthMethod != vision.AuthMethod {
		t.Fatalf("provider received wrong selected model: %#v", got)
	}
	if got.APIKey != vision.APIKey || got.OAuthAccessToken != vision.OAuthAccessToken || got.OAuthRefreshToken != vision.OAuthRefreshToken ||
		got.OAuthClientSecret != vision.OAuthClientSecret || got.BaseURL != vision.BaseURL || got.Transport != vision.Transport ||
		got.ExtraHeadersJSON != vision.ExtraHeadersJSON || got.ExtraBodyJSON != vision.ExtraBodyJSON ||
		got.CustomAuthConfigJSON != vision.CustomAuthConfigJSON || got.CustomAuthStateJSON != vision.CustomAuthStateJSON ||
		got.MixtureConfigJSON != vision.MixtureConfigJSON {
		t.Fatalf("provider received compact or incomplete model config: %#v", got)
	}

	var compactQueries, detailQueries int
	for _, raw := range counter.Statements() {
		stmt := strings.ToLower(strings.Join(strings.Fields(raw), " "))
		if strings.Contains(stmt, "order by is_default desc, name asc") {
			compactQueries++
			projection := strings.Split(stmt, " from agent_configs ")[0]
			for _, forbidden := range []string{"oauth_refresh_token", "oauth_client_secret", "base_url", "transport", "extra_headers_json", "extra_body_json", "custom_auth_config_json", "custom_auth_state_json", "mixture_config_json", "max_tokens", "temperature", "created_at", "updated_at"} {
				if strings.Contains(projection, forbidden) {
					t.Fatalf("streaming vision query selected forbidden column %q: %s", forbidden, raw)
				}
			}
		}
		if strings.Contains(stmt, "from agent_configs where id = ?") {
			detailQueries++
		}
	}
	if compactQueries != 1 || detailQueries != 1 {
		t.Fatalf("vision routing queries = compact %d, detail %d; statements=%#v", compactQueries, detailQueries, counter.Statements())
	}
}

func TestLLMService_ExecuteTaskWithAgent_ImageAttachmentsPassHydratedVisionConfig(t *testing.T) {
	ctx := context.Background()
	db, counter := testutil.NewStatementCountingTestDB(t)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	attachmentRepo := repository.NewAttachmentRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	textOnly := &models.LLMConfig{
		Name:       "Task text-only model",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "task-text-key",
		Model:      "local-text-model",
	}
	vision := &models.LLMConfig{
		Name:                 "Task stored vision model",
		Provider:             models.ProviderAnthropic,
		AuthMethod:           models.AuthMethodOAuth,
		OAuthAccessToken:     "task-oauth-access",
		OAuthRefreshToken:    "task-oauth-refresh",
		OAuthClientSecret:    "task-client-secret",
		Model:                "claude-opus-5-20250929",
		BaseURL:              "https://anthropic.example/v1",
		ExtraBodyJSON:        `{"task_setting":"body"}`,
		CustomAuthConfigJSON: `{"task_secret":"config"}`,
		CustomAuthStateJSON:  `{"task_state":"state"}`,
		MixtureConfigJSON:    `{"task_mixture":true}`,
		MaxTokens:            4096,
	}
	for _, cfg := range []*models.LLMConfig{textOnly, vision} {
		if err := llmConfigRepo.Create(ctx, cfg); err != nil {
			t.Fatalf("create %s: %v", cfg.Name, err)
		}
	}

	imagePath := filepath.Join(t.TempDir(), "task.png")
	if err := os.WriteFile(imagePath, []byte("fake png"), 0644); err != nil {
		t.Fatalf("create task image: %v", err)
	}
	task := &models.Task{
		ProjectID: "default",
		Title:     "Execute screenshot task",
		Prompt:    "Describe this screenshot",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if err := attachmentRepo.Create(ctx, &models.Attachment{TaskID: task.ID, FileName: "task.png", FilePath: imagePath, MediaType: "image/png", FileSize: 9}); err != nil {
		t.Fatalf("create task attachment: %v", err)
	}

	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, nil, nil, attachmentRepo)
	capture := &captureProviderAdapter{}
	svc.providerAdapters = map[models.LLMProvider]ProviderAdapter{models.ProviderAnthropic: capture}
	counter.Reset()
	counter.SetEnabled(true)
	_, err := svc.ExecuteTaskWithAgent(ctx, *task, *textOnly)
	counter.SetEnabled(false)
	if err != nil {
		t.Fatalf("ExecuteTaskWithAgent: %v", err)
	}

	requests := capture.Requests()
	if len(requests) != 1 {
		t.Fatalf("provider requests = %d, want one request", len(requests))
	}
	got := requests[0].Agent
	if got.ID != vision.ID || got.AuthMethod != vision.AuthMethod || got.OAuthAccessToken != vision.OAuthAccessToken ||
		got.OAuthRefreshToken != vision.OAuthRefreshToken || got.OAuthClientSecret != vision.OAuthClientSecret ||
		got.BaseURL != vision.BaseURL || got.ExtraBodyJSON != vision.ExtraBodyJSON ||
		got.CustomAuthConfigJSON != vision.CustomAuthConfigJSON || got.CustomAuthStateJSON != vision.CustomAuthStateJSON ||
		got.MixtureConfigJSON != vision.MixtureConfigJSON {
		t.Fatalf("task provider received compact or incomplete model config: %#v", got)
	}
	compactQueries := 0
	for _, raw := range counter.Statements() {
		stmt := strings.ToLower(strings.Join(strings.Fields(raw), " "))
		if strings.Contains(stmt, "order by is_default desc, name asc") {
			compactQueries++
		}
	}
	if compactQueries != 1 {
		t.Fatalf("task vision routing should use one compact selection query, statements=%#v", counter.Statements())
	}
}

func TestLLMService_ImageAttachments_VisionRoutingHandlesNoStoredModels(t *testing.T) {
	ctx := context.Background()
	db, counter := testutil.NewStatementCountingTestDB(t)
	repo := repository.NewLLMConfigRepo(db)
	if _, err := db.Exec(`DELETE FROM agent_configs`); err != nil {
		t.Fatalf("clear model configs: %v", err)
	}

	svc := NewLLMService(repo, nil, nil, nil, nil, nil)
	textOnly := models.LLMConfig{
		Name:       "Unstored text-only model",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "text-only-key",
		Model:      "text-only-model",
	}
	attachments := []models.Attachment{{MediaType: "image/png"}}

	t.Setenv("ANTHROPIC_API_KEY", "")
	counter.Reset()
	counter.SetEnabled(true)
	withoutFallback := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, "describe this screenshot", attachments, textOnly, "Test", "")
	counter.SetEnabled(false)
	if withoutFallback.Changed || withoutFallback.Reason != "no_vision_fallback_available" {
		t.Fatalf("no stored models without environment fallback = %#v, want unchanged agent and no-vision result", withoutFallback)
	}
	assertCompactVisionSelectionStatement(t, counter.Statements())

	t.Setenv("ANTHROPIC_API_KEY", "environment-vision-key")
	counter.Reset()
	counter.SetEnabled(true)
	withFallback := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, "describe this screenshot", attachments, textOnly, "Test", "")
	counter.SetEnabled(false)
	if !withFallback.Changed || withFallback.Reason != "vision_env_fallback" || withFallback.Agent.APIKey != "environment-vision-key" {
		t.Fatalf("no stored models with environment fallback = %#v, want environment vision agent", withFallback)
	}
	assertCompactVisionSelectionStatement(t, counter.Statements())
}

func assertCompactVisionSelectionStatement(t *testing.T, statements []string) {
	t.Helper()
	if len(statements) != 1 {
		t.Fatalf("vision routing statements = %#v, want one compact selection query", statements)
	}
	stmt := strings.ToLower(strings.Join(strings.Fields(statements[0]), " "))
	projection := strings.Split(stmt, " from agent_configs ")[0]
	wantProjection := "select id, name, provider, model, auth_method, is_default, case when coalesce(api_key, '') != '' then 1 else 0 end, case when coalesce(oauth_access_token, '') != '' then 1 else 0 end"
	if projection != wantProjection {
		t.Fatalf("vision selection projection = %q, want %q", projection, wantProjection)
	}
}

func TestLLMService_ImageAttachments_VisionRouting_Integration(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	// Create repos
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Create a test project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	// Create a text-only agent (no vision) as default
	textOnlyAgent := &models.LLMConfig{
		Name:       "Text Only Default",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-compatible-key",
		Model:      "text-only-compatible",
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, textOnlyAgent); err != nil {
		t.Fatalf("Failed to create text-only agent: %v", err)
	}

	// Create a vision-capable API agent
	apiAgent := &models.LLMConfig{
		Name:       "Claude API Vision",
		Provider:   models.ProviderAnthropic,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-key",
		Model:      "claude-sonnet-4-5-20250929",
		MaxTokens:  4096,
	}
	if err := llmConfigRepo.Create(ctx, apiAgent); err != nil {
		t.Fatalf("Failed to create API agent: %v", err)
	}

	// Create task with image attachment (mimics user report)
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Analyze screenshot from user report",
		Prompt:    "What do you see in this screenshot?",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
		// No explicit agent assigned - should use default text-only model.
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create a temporary PNG file
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "Screen Shot 2026-03-21 at 9.14.18 PM.png")
	if err := os.WriteFile(imgPath, []byte("fake PNG"), 0644); err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}

	// Attach the PNG
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "Screen Shot 2026-03-21 at 9.14.18 PM.png",
		FilePath:  imgPath,
		MediaType: "image/png",
		FileSize:  8,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	// Create LLM service
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)

	// Mock LLM caller
	mockCaller := testutil.NewMockLLMCaller()
	svc.SetLLMCaller(mockCaller)

	// Load the task
	loadedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load task: %v", err)
	}

	// Load attachments
	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load attachments: %v", err)
	}

	// Execute vision routing
	visionDecision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "ExecuteTaskWithAgent", task.ID)

	// CRITICAL: The agent should be switched to a vision-capable agent
	if !visionDecision.Changed {
		t.Errorf("BUG REPRODUCED: Vision routing did NOT switch from text-only to vision-capable agent. Reason: %s, Detail: %s", visionDecision.Reason, visionDecision.Detail)
	}

	if visionDecision.Agent.Provider != models.ProviderAnthropic || visionDecision.Agent.AuthMethod != models.AuthMethodAPIKey {
		t.Errorf("BUG REPRODUCED: Expected switch to API agent, but got provider=%s auth=%s", visionDecision.Agent.Provider, visionDecision.Agent.AuthMethod)
	}

	// Verify the selected agent supports vision
	if visionDecision.Agent.IsAnthropicCLI() {
		t.Error("BUG REPRODUCED: Selected agent is still a non-vision legacy CLI config")
	}

	// Success: Agent was properly switched to vision-capable agent
	t.Logf("SUCCESS: Vision routing correctly switched from text-only agent to vision-capable agent: %s (provider=%s, auth=%s)",
		visionDecision.Agent.Name, visionDecision.Agent.Provider, visionDecision.Agent.AuthMethod)
}

func TestLLMService_ImageAttachments_NoVisionAgent_ClearError(t *testing.T) {
	ctx := context.Background()
	db := testutil.NewTestDB(t)

	// Create repos
	projectRepo := repository.NewProjectRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	broadcaster := events.NewBroadcaster()
	taskRepo := repository.NewTaskRepo(db, broadcaster)
	execRepo := repository.NewExecutionRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)

	// Create a test project
	project := &models.Project{Name: "Test Project"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("Failed to create project: %v", err)
	}

	// Create ONLY a text-only agent (no vision agents available)
	textOnlyAgent := &models.LLMConfig{
		Name:       "Text Only Compatible",
		Provider:   models.ProviderOpenAICompatible,
		AuthMethod: models.AuthMethodAPIKey,
		APIKey:     "test-compatible-key",
		Model:      "text-only-compatible",
		MaxTokens:  4096,
		IsDefault:  true,
	}
	if err := llmConfigRepo.Create(ctx, textOnlyAgent); err != nil {
		t.Fatalf("Failed to create text-only agent: %v", err)
	}

	// Clear environment fallback
	oldAPIKey := os.Getenv("ANTHROPIC_API_KEY")
	os.Setenv("ANTHROPIC_API_KEY", "")
	defer os.Setenv("ANTHROPIC_API_KEY", oldAPIKey)

	// Create task with image attachment
	task := &models.Task{
		ProjectID: project.ID,
		Title:     "Analyze screenshot",
		Prompt:    "What's in this image?",
		Category:  models.CategoryBacklog,
		Status:    models.StatusPending,
	}
	if err := taskRepo.Create(ctx, task); err != nil {
		t.Fatalf("Failed to create task: %v", err)
	}

	// Create a temporary PNG file
	tmpDir := t.TempDir()
	imgPath := filepath.Join(tmpDir, "image.png")
	if err := os.WriteFile(imgPath, []byte("PNG"), 0644); err != nil {
		t.Fatalf("Failed to create temp PNG: %v", err)
	}

	// Attach the PNG
	attachment := &models.Attachment{
		TaskID:    task.ID,
		FileName:  "image.png",
		FilePath:  imgPath,
		MediaType: "image/png",
		FileSize:  3,
	}
	if err := attachmentRepo.Create(ctx, attachment); err != nil {
		t.Fatalf("Failed to create attachment: %v", err)
	}

	// Create LLM service
	svc := NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, nil, attachmentRepo)

	// Load task and attachments
	loadedTask, err := taskRepo.GetByID(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load task: %v", err)
	}

	attachments, err := attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		t.Fatalf("Failed to load attachments: %v", err)
	}

	// Execute vision routing
	visionDecision := svc.ensureRoutingStrategy().resolveVisionRoutingDecision(ctx, loadedTask.Prompt, attachments, *textOnlyAgent, "ExecuteTaskWithAgent", task.ID)

	// Agent should NOT change (no vision agents available)
	if visionDecision.Changed {
		t.Error("Expected no agent change when no vision agents available")
	}

	// Reason should clearly indicate why no vision routing happened
	if visionDecision.Reason != "no_vision_fallback_available" {
		t.Errorf("Expected reason 'no_vision_fallback_available', got: %s", visionDecision.Reason)
	}

	// Detail should mention vision-capable agents
	if !strings.Contains(visionDecision.Detail, "vision-capable") {
		t.Errorf("Expected detail to mention vision-capable agents, got: %s", visionDecision.Detail)
	}

	t.Logf("SUCCESS: When no vision agents available, system provides clear warning: %s", visionDecision.Detail)
}
