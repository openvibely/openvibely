package repository

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestTrendRepo_XCredentials(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	trendRepo := NewTrendRepo(db)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Should return nil when no credentials exist
	creds, err := trendRepo.GetXCredentials(ctx, project.ID)
	if err != nil {
		t.Fatalf("get X credentials: %v", err)
	}
	if creds != nil {
		t.Fatal("expected nil credentials")
	}

	// Create credentials
	newCreds := &models.XCredentials{
		ProjectID:   project.ID,
		BearerToken: "test-bearer-token",
		APIKey:      "test-api-key",
		APISecret:   "test-api-secret",
	}
	if err := trendRepo.UpsertXCredentials(ctx, newCreds); err != nil {
		t.Fatalf("upsert X credentials: %v", err)
	}
	if newCreds.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// Verify retrieval
	creds, err = trendRepo.GetXCredentials(ctx, project.ID)
	if err != nil {
		t.Fatalf("get X credentials: %v", err)
	}
	if creds == nil {
		t.Fatal("expected credentials")
	}
	if creds.BearerToken != "test-bearer-token" {
		t.Errorf("expected bearer token 'test-bearer-token', got %q", creds.BearerToken)
	}
	if creds.APIKey != "test-api-key" {
		t.Errorf("expected api key 'test-api-key', got %q", creds.APIKey)
	}

	// Update credentials (upsert)
	newCreds.BearerToken = "updated-bearer"
	if err := trendRepo.UpsertXCredentials(ctx, newCreds); err != nil {
		t.Fatalf("upsert X credentials (update): %v", err)
	}

	creds, err = trendRepo.GetXCredentials(ctx, project.ID)
	if err != nil {
		t.Fatalf("get X credentials after update: %v", err)
	}
	if creds.BearerToken != "updated-bearer" {
		t.Errorf("expected bearer token 'updated-bearer', got %q", creds.BearerToken)
	}
}

func TestTrendRepo_Sources(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	trendRepo := NewTrendRepo(db)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Empty initially
	sources, err := trendRepo.ListSources(ctx, project.ID)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if len(sources) != 0 {
		t.Fatalf("expected 0 sources, got %d", len(sources))
	}

	// Create sources
	src1 := &models.TrendSource{
		ProjectID:  project.ID,
		SourceType: models.TrendSourceHashtag,
		Value:      "#AItools",
		Enabled:    true,
	}
	if err := trendRepo.CreateSource(ctx, src1); err != nil {
		t.Fatalf("create source: %v", err)
	}
	if src1.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	src2 := &models.TrendSource{
		ProjectID:  project.ID,
		SourceType: models.TrendSourceCompetitor,
		Value:      "CursorAI",
		Enabled:    true,
	}
	if err := trendRepo.CreateSource(ctx, src2); err != nil {
		t.Fatalf("create source 2: %v", err)
	}

	// List all
	sources, err = trendRepo.ListSources(ctx, project.ID)
	if err != nil {
		t.Fatalf("list sources: %v", err)
	}
	if len(sources) != 2 {
		t.Fatalf("expected 2 sources, got %d", len(sources))
	}

	// List enabled
	enabled, err := trendRepo.ListEnabledSources(ctx, project.ID)
	if err != nil {
		t.Fatalf("list enabled sources: %v", err)
	}
	if len(enabled) != 2 {
		t.Fatalf("expected 2 enabled sources, got %d", len(enabled))
	}

	// Toggle off
	if err := trendRepo.ToggleSource(ctx, src1.ID, false); err != nil {
		t.Fatalf("toggle source: %v", err)
	}

	enabled, err = trendRepo.ListEnabledSources(ctx, project.ID)
	if err != nil {
		t.Fatalf("list enabled sources after toggle: %v", err)
	}
	if len(enabled) != 1 {
		t.Fatalf("expected 1 enabled source, got %d", len(enabled))
	}

	// Count
	count, err := trendRepo.CountSources(ctx, project.ID)
	if err != nil {
		t.Fatalf("count sources: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 sources, got %d", count)
	}

	// Delete
	if err := trendRepo.DeleteSource(ctx, src1.ID); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	sources, err = trendRepo.ListSources(ctx, project.ID)
	if err != nil {
		t.Fatalf("list sources after delete: %v", err)
	}
	if len(sources) != 1 {
		t.Fatalf("expected 1 source, got %d", len(sources))
	}
}

func TestTrendRepo_Entries(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	trendRepo := NewTrendRepo(db)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create entry
	entry := &models.TrendEntry{
		ProjectID:       project.ID,
		SourceType:      "keyword",
		Content:         "Multi-agent collaboration is the future of AI tools",
		Author:          "techuser1",
		URL:             "https://x.com/i/status/123",
		EngagementScore: 42,
		Sentiment:       models.SentimentPositive,
		RawData:         "{}",
	}
	if err := trendRepo.CreateEntry(ctx, entry); err != nil {
		t.Fatalf("create entry: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// List recent
	entries, err := trendRepo.ListRecentEntries(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Content != entry.Content {
		t.Errorf("expected content %q, got %q", entry.Content, entries[0].Content)
	}
	if entries[0].EngagementScore != 42 {
		t.Errorf("expected engagement score 42, got %d", entries[0].EngagementScore)
	}

	// Count
	count, err := trendRepo.CountEntries(ctx, project.ID)
	if err != nil {
		t.Fatalf("count entries: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 entry, got %d", count)
	}
}

func TestTrendRepo_Patterns(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	trendRepo := NewTrendRepo(db)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create pattern
	pattern := &models.TrendPattern{
		ProjectID:   project.ID,
		PatternType: models.PatternFeatureRequest,
		Title:       "Multi-agent collaboration",
		Description: "Users want multi-agent workflows for complex tasks",
		Evidence:    `["tweet1", "tweet2"]`,
		Confidence:  0.85,
		SignalCount: 5,
		Status:      models.PatternStatusActive,
	}
	if err := trendRepo.CreatePattern(ctx, pattern); err != nil {
		t.Fatalf("create pattern: %v", err)
	}
	if pattern.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// List active
	patterns, err := trendRepo.ListActivePatterns(ctx, project.ID)
	if err != nil {
		t.Fatalf("list active patterns: %v", err)
	}
	if len(patterns) != 1 {
		t.Fatalf("expected 1 pattern, got %d", len(patterns))
	}
	if patterns[0].Title != "Multi-agent collaboration" {
		t.Errorf("expected title 'Multi-agent collaboration', got %q", patterns[0].Title)
	}

	// Existing pattern check
	existing, err := trendRepo.ExistingPattern(ctx, project.ID, "Multi-agent collaboration", models.PatternFeatureRequest)
	if err != nil {
		t.Fatalf("existing pattern: %v", err)
	}
	if existing == nil {
		t.Fatal("expected existing pattern")
	}

	// Bump
	if err := trendRepo.BumpPattern(ctx, pattern.ID, 3); err != nil {
		t.Fatalf("bump pattern: %v", err)
	}
	patterns, _ = trendRepo.ListActivePatterns(ctx, project.ID)
	if patterns[0].SignalCount != 8 {
		t.Errorf("expected signal count 8, got %d", patterns[0].SignalCount)
	}

	// Update status to implemented
	if err := trendRepo.UpdatePatternStatus(ctx, pattern.ID, models.PatternStatusImplemented, "Workflow Feature"); err != nil {
		t.Fatalf("update pattern status: %v", err)
	}

	// Active list should be empty now
	activePatterns, _ := trendRepo.ListActivePatterns(ctx, project.ID)
	if len(activePatterns) != 0 {
		t.Fatalf("expected 0 active patterns, got %d", len(activePatterns))
	}

	// Count implemented
	implCount, _ := trendRepo.CountImplementedPatterns(ctx, project.ID)
	if implCount != 1 {
		t.Fatalf("expected 1 implemented pattern, got %d", implCount)
	}

	// Existing pattern check for implemented (should return nil since only active)
	existing, _ = trendRepo.ExistingPattern(ctx, project.ID, "Multi-agent collaboration", models.PatternFeatureRequest)
	if existing != nil {
		t.Error("expected nil for non-active pattern")
	}
}

func TestTrendRepo_CompetitorUpdates(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	trendRepo := NewTrendRepo(db)

	project := &models.Project{Name: "test", Description: "test", RepoPath: "/tmp"}
	if err := projectRepo.Create(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}

	// Create competitor update
	update := &models.CompetitorUpdate{
		ProjectID:        project.ID,
		CompetitorName:   "CursorAI",
		UpdateType:       models.CompUpdateFeatureLaunch,
		Title:            "Cursor launched multi-file editing",
		Description:      "New feature allows editing multiple files at once",
		ImpactAssessment: "Could reduce our competitive advantage in code editing",
		RelevanceScore:   0.9,
	}
	if err := trendRepo.CreateCompetitorUpdate(ctx, update); err != nil {
		t.Fatalf("create competitor update: %v", err)
	}
	if update.ID == "" {
		t.Fatal("expected non-empty ID")
	}

	// List recent
	updates, err := trendRepo.ListRecentCompetitorUpdates(ctx, project.ID, 10)
	if err != nil {
		t.Fatalf("list competitor updates: %v", err)
	}
	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d", len(updates))
	}
	if updates[0].CompetitorName != "CursorAI" {
		t.Errorf("expected competitor 'CursorAI', got %q", updates[0].CompetitorName)
	}

	// Count
	count, err := trendRepo.CountCompetitorUpdates(ctx, project.ID)
	if err != nil {
		t.Fatalf("count competitor updates: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 update, got %d", count)
	}
}
