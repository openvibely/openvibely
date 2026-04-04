package service

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/util"
)

func TestTrendIntelligenceService_GetDashboardData_Empty(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	data, err := svc.GetDashboardData(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetDashboardData: %v", err)
	}
	if data == nil {
		t.Fatal("expected non-nil dashboard data")
	}
	if data.HasXCredentials {
		t.Error("expected HasXCredentials=false")
	}
	if data.Stats.TotalEntries != 0 {
		t.Errorf("expected 0 entries, got %d", data.Stats.TotalEntries)
	}
}

func TestTrendIntelligenceService_GetDashboardData_WithData(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Add credentials
	creds := &models.XCredentials{
		ProjectID:   project.ID,
		BearerToken: "test-token",
	}
	if err := trendRepo.UpsertXCredentials(ctx, creds); err != nil {
		t.Fatalf("upsert credentials: %v", err)
	}

	// Add a source
	src := &models.TrendSource{
		ProjectID:  project.ID,
		SourceType: models.TrendSourceHashtag,
		Value:      "#AItools",
		Enabled:    true,
	}
	if err := trendRepo.CreateSource(ctx, src); err != nil {
		t.Fatalf("create source: %v", err)
	}

	// Add an entry
	entry := &models.TrendEntry{
		ProjectID:       project.ID,
		SourceType:      "keyword",
		Content:         "AI productivity tools are amazing",
		Author:          "user1",
		EngagementScore: 10,
		Sentiment:       models.SentimentPositive,
		RawData:         "{}",
	}
	if err := trendRepo.CreateEntry(ctx, entry); err != nil {
		t.Fatalf("create entry: %v", err)
	}

	// Add a pattern
	pattern := &models.TrendPattern{
		ProjectID:   project.ID,
		PatternType: models.PatternPainPoint,
		Title:       "Slow CI/CD pipelines",
		Description: "Users complain about slow build times",
		Evidence:    "[]",
		Confidence:  0.7,
		SignalCount: 3,
		Status:      models.PatternStatusActive,
	}
	if err := trendRepo.CreatePattern(ctx, pattern); err != nil {
		t.Fatalf("create pattern: %v", err)
	}

	data, err := svc.GetDashboardData(ctx, project.ID)
	if err != nil {
		t.Fatalf("GetDashboardData: %v", err)
	}
	if !data.HasXCredentials {
		t.Error("expected HasXCredentials=true")
	}
	if data.Stats.TotalEntries != 1 {
		t.Errorf("expected 1 entry, got %d", data.Stats.TotalEntries)
	}
	if data.Stats.ActivePatterns != 1 {
		t.Errorf("expected 1 active pattern, got %d", data.Stats.ActivePatterns)
	}
	if data.Stats.MonitoredSources != 1 {
		t.Errorf("expected 1 source, got %d", data.Stats.MonitoredSources)
	}
	if len(data.Sources) != 1 {
		t.Errorf("expected 1 source in list, got %d", len(data.Sources))
	}
	if len(data.ActivePatterns) != 1 {
		t.Errorf("expected 1 active pattern in list, got %d", len(data.ActivePatterns))
	}
}

func TestTrendIntelligenceService_GetTrendContext_Empty(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	context := svc.GetTrendContext(ctx, project.ID)
	if context != "" {
		t.Errorf("expected empty context for project with no data, got %q", context)
	}
}

func TestTrendIntelligenceService_GetTrendContext_WithPatterns(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Add a pattern
	pattern := &models.TrendPattern{
		ProjectID:   project.ID,
		PatternType: models.PatternFeatureRequest,
		Title:       "Multi-agent workflows",
		Description: "Users are requesting multi-agent collaboration features",
		Evidence:    "[]",
		Confidence:  0.9,
		SignalCount: 10,
		Status:      models.PatternStatusActive,
	}
	if err := trendRepo.CreatePattern(ctx, pattern); err != nil {
		t.Fatalf("create pattern: %v", err)
	}

	trendCtx := svc.GetTrendContext(ctx, project.ID)
	if trendCtx == "" {
		t.Fatal("expected non-empty trend context")
	}
	if !contains(trendCtx, "Multi-agent workflows") {
		t.Error("expected trend context to contain pattern title")
	}
	if !contains(trendCtx, "External Trend Intelligence") {
		t.Error("expected trend context to contain section header")
	}
}

func TestTrendIntelligenceService_SourceManagement(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Add source
	src := &models.TrendSource{
		ProjectID:  project.ID,
		SourceType: models.TrendSourceKeyword,
		Value:      "AI developer tools",
		Enabled:    true,
	}
	if err := svc.AddSource(ctx, src); err != nil {
		t.Fatalf("add source: %v", err)
	}

	// List
	sources, err := svc.ListSources(ctx, project.ID)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}

	// Toggle
	if err := svc.ToggleSource(ctx, src.ID, false); err != nil {
		t.Fatalf("toggle source: %v", err)
	}

	// Delete
	if err := svc.DeleteSource(ctx, src.ID); err != nil {
		t.Fatalf("delete source: %v", err)
	}

	sources, _ = svc.ListSources(ctx, project.ID)
	if len(sources) != 0 {
		t.Fatalf("expected 0 sources after delete, got %d", len(sources))
	}
}

func TestTrendIntelligenceService_RecordFeatureOutcome(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create pattern
	pattern := &models.TrendPattern{
		ProjectID:   project.ID,
		PatternType: models.PatternFeatureRequest,
		Title:       "Task templates",
		Description: "Users want reusable task templates",
		Evidence:    "[]",
		Confidence:  0.8,
		SignalCount: 5,
		Status:      models.PatternStatusActive,
	}
	if err := trendRepo.CreatePattern(ctx, pattern); err != nil {
		t.Fatalf("create pattern: %v", err)
	}

	// Record feature outcome
	if err := svc.RecordFeatureOutcome(ctx, pattern.ID, "Task Templates Feature"); err != nil {
		t.Fatalf("record feature outcome: %v", err)
	}

	// Verify pattern is now implemented
	implemented, _ := trendRepo.CountImplementedPatterns(ctx, project.ID)
	if implemented != 1 {
		t.Fatalf("expected 1 implemented pattern, got %d", implemented)
	}

	active, _ := trendRepo.CountActivePatterns(ctx, project.ID)
	if active != 0 {
		t.Fatalf("expected 0 active patterns, got %d", active)
	}
}

func TestTrendIntelligenceService_DismissPattern(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	pattern := &models.TrendPattern{
		ProjectID:   project.ID,
		PatternType: models.PatternMarketShift,
		Title:       "Shift to local LLMs",
		Description: "Market moving toward local/on-device models",
		Evidence:    "[]",
		Confidence:  0.5,
		SignalCount: 2,
		Status:      models.PatternStatusActive,
	}
	if err := trendRepo.CreatePattern(ctx, pattern); err != nil {
		t.Fatalf("create pattern: %v", err)
	}

	if err := svc.DismissPattern(ctx, pattern.ID); err != nil {
		t.Fatalf("dismiss pattern: %v", err)
	}

	active, _ := trendRepo.CountActivePatterns(ctx, project.ID)
	if active != 0 {
		t.Fatalf("expected 0 active patterns, got %d", active)
	}
}

func TestTrendIntelligenceService_CollectFromX_NoCreds(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	_, err := svc.CollectFromX(ctx, project.ID)
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
}

func TestTrendIntelligenceService_GetCostAnalysisContext(t *testing.T) {
	svc := NewTrendIntelligenceService(nil, nil, nil)
	ctx := svc.GetCostAnalysisContext()
	if ctx == "" {
		t.Fatal("expected non-empty cost analysis context")
	}
	if !contains(ctx, "API Cost Impact") {
		t.Error("expected context to mention API costs")
	}
	if !contains(ctx, "value_to_cost_ratio") {
		t.Error("expected context to mention value_to_cost_ratio")
	}
}

func TestTrendIntelligenceService_ParseTrendPatterns(t *testing.T) {
	svc := NewTrendIntelligenceService(nil, nil, nil)

	// Test valid JSON array
	response := `[
		{
			"pattern_type": "feature_request",
			"title": "Multi-agent support",
			"description": "Users want multi-agent workflows",
			"evidence": ["tweet 1", "tweet 2"],
			"confidence": 0.85,
			"signal_count": 5
		}
	]`

	patterns, err := svc.parseTrendPatternsResponse(response)
	if err != nil {
		t.Fatalf("parse patterns: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].Title != "Multi-agent support" {
		t.Errorf("expected title 'Multi-agent support', got %q", patterns[0].Title)
	}
	if patterns[0].Confidence != 0.85 {
		t.Errorf("expected confidence 0.85, got %f", patterns[0].Confidence)
	}

	// Test with markdown fences
	fencedResponse := "```json\n" + response + "\n```"
	patterns, err = svc.parseTrendPatternsResponse(fencedResponse)
	if err != nil {
		t.Fatalf("parse fenced patterns: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern from fenced, got %d", len(patterns))
	}
}

func TestTrendIntelligenceService_ParseCompetitorUpdates(t *testing.T) {
	svc := NewTrendIntelligenceService(nil, nil, nil)

	response := `[
		{
			"competitor_name": "CursorAI",
			"update_type": "feature_launch",
			"title": "Background agents",
			"description": "Cursor launched background agent support",
			"impact_assessment": "Could increase their market share",
			"relevance_score": 0.9
		}
	]`

	updates, err := svc.parseCompetitorUpdatesResponse(response)
	if err != nil {
		t.Fatalf("parse updates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].CompetitorName != "CursorAI" {
		t.Errorf("expected competitor 'CursorAI', got %q", updates[0].CompetitorName)
	}
}

func TestBuildXSearchQuery(t *testing.T) {
	tests := []struct {
		name     string
		source   *models.TrendSource
		expected string
	}{
		{
			name:     "hashtag without #",
			source:   &models.TrendSource{SourceType: models.TrendSourceHashtag, Value: "AItools"},
			expected: "#AItools -is:retweet lang:en",
		},
		{
			name:     "hashtag with #",
			source:   &models.TrendSource{SourceType: models.TrendSourceHashtag, Value: "#AItools"},
			expected: "#AItools -is:retweet lang:en",
		},
		{
			name:     "account without @",
			source:   &models.TrendSource{SourceType: models.TrendSourceAccount, Value: "cursor_ai"},
			expected: "from:cursor_ai -is:retweet",
		},
		{
			name:     "account with @",
			source:   &models.TrendSource{SourceType: models.TrendSourceAccount, Value: "@cursor_ai"},
			expected: "from:cursor_ai -is:retweet",
		},
		{
			name:     "keyword",
			source:   &models.TrendSource{SourceType: models.TrendSourceKeyword, Value: "AI developer tools"},
			expected: "AI developer tools -is:retweet lang:en",
		},
		{
			name:     "competitor",
			source:   &models.TrendSource{SourceType: models.TrendSourceCompetitor, Value: "Cursor"},
			expected: "Cursor -is:retweet lang:en",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildXSearchQuery(tt.source)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestFindJSONArray(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain array",
			input:    `[{"a": 1}]`,
			expected: `[{"a": 1}]`,
		},
		{
			name:     "fenced array",
			input:    "```json\n[{\"a\": 1}]\n```",
			expected: `[{"a": 1}]`,
		},
		{
			name:     "array with surrounding text",
			input:    "Here are the results:\n[{\"a\": 1}]\nDone.",
			expected: `[{"a": 1}]`,
		},
		{
			name:     "no array",
			input:    "just some text",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := util.ExtractJSONArray(tt.input)
			if result != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestTrendIntelligenceService_CredentialManagement(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()

	projectRepo := repository.NewProjectRepo(db)
	trendRepo := repository.NewTrendRepo(db)
	llmConfigRepo := repository.NewLLMConfigRepo(db)

	svc := NewTrendIntelligenceService(trendRepo, projectRepo, llmConfigRepo)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// No credentials initially
	creds, err := svc.GetXCredentials(ctx, project.ID)
	if err != nil {
		t.Fatalf("get credentials: %v", err)
	}
	if creds != nil {
		t.Fatal("expected nil credentials")
	}

	// Save credentials
	newCreds := &models.XCredentials{
		ProjectID:   project.ID,
		BearerToken: "test-token",
	}
	if err := svc.SaveXCredentials(ctx, newCreds); err != nil {
		t.Fatalf("save credentials: %v", err)
	}

	// Verify
	creds, err = svc.GetXCredentials(ctx, project.ID)
	if err != nil {
		t.Fatalf("get credentials: %v", err)
	}
	if creds == nil || creds.BearerToken != "test-token" {
		t.Fatal("expected saved credentials")
	}
}

// contains is a helper for string containment checks.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
