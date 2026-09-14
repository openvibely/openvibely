package repository

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestSkillAnalyticsRepo_UsageOverTime(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSkillAnalyticsRepo(db)
	ctx := context.Background()
	projectID := defaultProjectID(t, db)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	recordSkillAnalyticsEvents(t, repo,
		skillEvent(projectID, "turn-1", "", "global", "provider_adapter", "selected", "skill_curator", now),
		skillEvent(projectID, "turn-1", "", "global", "provider_adapter", "loaded", "skill_curator", now.Add(time.Minute)),
		skillEvent(projectID, "turn-2", "", "project", "frontend", "viewed", "manual", now.AddDate(0, 0, 1)),
		skillEvent(projectID, "turn-2", "", "project", "frontend", "created", "manual", now.AddDate(0, 0, 1).Add(time.Minute)),
		skillEvent(projectID, "turn-2", "", "project", "frontend", "edited", "manual", now.AddDate(0, 0, 1).Add(2*time.Minute)),
	)

	usage, err := repo.GetUsageOverTime(ctx, SkillAnalyticsFilter{ProjectID: projectID, GroupBy: "day"})
	if err != nil {
		t.Fatalf("GetUsageOverTime: %v", err)
	}
	if len(usage) != 2 {
		t.Fatalf("usage periods = %+v, want 2", usage)
	}
	if usage[0].Period != "2026-06-10" || usage[0].SelectedCount != 1 || usage[0].LoadedCount != 1 || usage[0].ActivityCount != 2 {
		t.Fatalf("first period = %+v", usage[0])
	}
	if usage[1].Period != "2026-06-11" || usage[1].ViewedCount != 1 || usage[1].CreatedCount != 1 || usage[1].EditedCount != 1 || usage[1].ActivityCount != 3 {
		t.Fatalf("second period = %+v", usage[1])
	}
}

func TestSkillAnalyticsRepo_TopSkillsAndFollowThrough(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSkillAnalyticsRepo(db)
	ctx := context.Background()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	projectID := defaultProjectID(t, db)

	recordSkillAnalyticsEvents(t, repo,
		skillEvent(projectID, "turn-1", "", "global", "provider_adapter", "selected", "skill_curator", now),
		skillEvent(projectID, "turn-1", "", "global", "provider_adapter", "loaded", "skill_curator", now.Add(time.Minute)),
		skillEvent(projectID, "turn-2", "", "global", "provider_adapter", "selected", "skill_curator", now.Add(2*time.Minute)),
		skillEvent(projectID, "turn-3", "", "project", "frontend", "selected", "skill_curator", now.Add(3*time.Minute)),
		skillEvent(projectID, "turn-3", "", "project", "frontend", "viewed", "manual", now.Add(4*time.Minute)),
	)

	top, err := repo.GetTopSkills(ctx, SkillAnalyticsFilter{ProjectID: projectID, Limit: 10})
	if err != nil {
		t.Fatalf("GetTopSkills: %v", err)
	}
	if len(top) < 2 {
		t.Fatalf("expected at least two skills, got %+v", top)
	}
	provider := findTopSkill(top, "provider_adapter")
	if provider == nil {
		t.Fatalf("provider_adapter missing from top skills: %+v", top)
	}
	if provider.SelectedCount != 2 || provider.LoadedCount != 1 || provider.ViewedCount != 0 || provider.ActivityCount != 3 {
		t.Fatalf("provider counts = %+v", provider)
	}
	if provider.FollowThroughRate == nil || *provider.FollowThroughRate != 0.5 {
		t.Fatalf("provider follow-through = %v, want 0.5", provider.FollowThroughRate)
	}

	follow, err := repo.GetSelectionFollowThrough(ctx, SkillAnalyticsFilter{ProjectID: projectID, Limit: 10})
	if err != nil {
		t.Fatalf("GetSelectionFollowThrough: %v", err)
	}
	providerFollow := findFollowSkill(follow, "provider_adapter")
	if providerFollow == nil {
		t.Fatalf("provider_adapter missing from follow-through: %+v", follow)
	}
	if providerFollow.SelectedCount != 2 || providerFollow.LoadedOrViewed != 1 || providerFollow.IgnoredCount != 1 {
		t.Fatalf("provider follow-through counts = %+v", providerFollow)
	}
	frontendFollow := findFollowSkill(follow, "frontend")
	if frontendFollow == nil || frontendFollow.SelectedCount != 1 || frontendFollow.LoadedOrViewed != 1 || frontendFollow.IgnoredCount != 0 {
		t.Fatalf("frontend follow-through counts = %+v", frontendFollow)
	}
}

func TestSkillAnalyticsRepo_AgentUsageHeatmap(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSkillAnalyticsRepo(db)
	ctx := context.Background()
	projectID := defaultProjectID(t, db)
	if _, err := db.Exec(`INSERT INTO agents (id, name, key, scope) VALUES ('agent-a', 'Default Coding Agent', 'default_coding_agent', 'global')`); err != nil {
		t.Fatalf("insert agent: %v", err)
	}
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	recordSkillAnalyticsEvents(t, repo,
		skillEvent(projectID, "turn-1", "agent-a", "global", "provider_adapter", "selected", "skill_curator", now),
		skillEvent(projectID, "turn-1", "agent-a", "global", "provider_adapter", "loaded", "skill_curator", now),
		skillEvent(projectID, "turn-1", "agent-a", "global", "provider_adapter", "viewed", "manual", now),
		skillEvent(projectID, "turn-1", "agent-a", "global", "provider_adapter", "edited", "manual", now),
		skillEvent(projectID, "turn-2", "agent-a", "project", "frontend", "selected", "skill_curator", now),
		skillEvent(projectID, "turn-3", "agent-a", "project", "provider_adapter", "selected", "skill_curator", now),
	)

	heatmap, err := repo.GetAgentUsage(ctx, SkillAnalyticsFilter{ProjectID: projectID, Limit: 5})
	if err != nil {
		t.Fatalf("GetAgentUsage: %v", err)
	}
	if len(heatmap.Agents) != 1 || heatmap.Agents[0].AgentName != "Default Coding Agent" {
		t.Fatalf("agents = %+v", heatmap.Agents)
	}
	if len(heatmap.Skills) != 2 {
		t.Fatalf("legacy heatmap skills = %+v, want unique handles", heatmap.Skills)
	}
	if len(heatmap.SkillPairs) != 3 {
		t.Fatalf("scoped heatmap skills = %+v, want global/project provider pairs plus frontend", heatmap.SkillPairs)
	}
	globalCell := findAgentUsageCell(heatmap.Cells, "agent-a", "provider_adapter", models.SkillScopeGlobal)
	if globalCell == nil {
		t.Fatalf("global provider cell missing: %+v", heatmap.Cells)
	}
	if globalCell.ActivityCount != 3 || globalCell.SelectedCount != 1 || globalCell.LoadedCount != 1 || globalCell.ViewedCount != 1 || globalCell.EditedCount != 1 {
		t.Fatalf("global provider cell = %+v", globalCell)
	}
	projectCell := findAgentUsageCell(heatmap.Cells, "agent-a", "provider_adapter", models.SkillScopeProject)
	if projectCell == nil || projectCell.ActivityCount != 1 || projectCell.SelectedCount != 1 {
		t.Fatalf("project provider cell must remain separate: %+v", heatmap.Cells)
	}
}

func TestSkillAnalyticsRepo_UnderusedSkillsIncludesEnabledSkillsWithNoEvents(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSkillAnalyticsRepo(db)
	ctx := context.Background()
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	projectID := defaultProjectID(t, db)
	recordSkillAnalyticsEvents(t, repo,
		skillEvent(projectID, "turn-1", "", "global", "active_skill", "selected", "skill_curator", now),
		skillEvent(projectID, "turn-1", "", "global", "active_skill", "loaded", "skill_curator", now),
	)
	enabled := []EnabledSkillInfo{
		{Handle: "never_used", Scope: models.SkillScopeGlobal, Enabled: true},
		{Handle: "always_guidance", Scope: models.SkillScopeProject, Enabled: true, AlwaysUse: true},
		{Handle: "active_skill", Scope: models.SkillScopeGlobal, Enabled: true},
	}

	underused, err := repo.GetUnderusedSkills(ctx, SkillAnalyticsFilter{ProjectID: projectID}, enabled)
	if err != nil {
		t.Fatalf("GetUnderusedSkills: %v", err)
	}
	if len(underused) < 3 {
		t.Fatalf("expected enabled skills plus activity, got %+v", underused)
	}
	if underused[0].SkillHandle != "always_guidance" && underused[0].SkillHandle != "never_used" {
		t.Fatalf("first underused skill should have zero activity, got %+v", underused[0])
	}
	never := findUnderusedSkill(underused, "never_used")
	if never == nil || !never.Enabled || never.ActivityCount != 0 || never.LastActivity != nil {
		t.Fatalf("never_used row = %+v", never)
	}
	always := findUnderusedSkill(underused, "always_guidance")
	if always == nil || !always.AlwaysUse || always.ActivityCount != 0 {
		t.Fatalf("always_guidance row = %+v", always)
	}
}

func TestSkillAnalyticsRepo_UnderusedSkillsCountsLifecycleHookSelectionsByProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSkillAnalyticsRepo(db)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	projectA := &models.Project{Name: "Lifecycle Repo Analytics A"}
	if err := projectRepo.Create(ctx, projectA); err != nil {
		t.Fatalf("create project A: %v", err)
	}
	projectB := &models.Project{Name: "Lifecycle Repo Analytics B"}
	if err := projectRepo.Create(ctx, projectB); err != nil {
		t.Fatalf("create project B: %v", err)
	}
	agentRepo := NewAgentRepo(db)
	agentA := &models.Agent{Name: "Goal Agent A", Key: "goal_agent_a", Model: "inherit", Enabled: true}
	if err := agentRepo.Create(ctx, agentA); err != nil {
		t.Fatalf("create agent A: %v", err)
	}
	agentB := &models.Agent{Name: "Goal Agent B", Key: "goal_agent_b", Model: "inherit", Enabled: true}
	if err := agentRepo.Create(ctx, agentB); err != nil {
		t.Fatalf("create agent B: %v", err)
	}
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	eventA := skillEvent(projectA.ID, "lifecycle-exec-a", agentA.ID, models.SkillScopeAgentOwned, "evaluate_task_goal", models.SkillEventSelected, models.SkillEventSourceLifecycleHook, now)
	eventA.Surface = models.SkillSurfaceLifecycleHook
	eventB := skillEvent(projectB.ID, "lifecycle-exec-b", agentB.ID, models.SkillScopeAgentOwned, "evaluate_task_goal", models.SkillEventSelected, models.SkillEventSourceLifecycleHook, now.Add(time.Minute))
	eventB.Surface = models.SkillSurfaceLifecycleHook
	recordSkillAnalyticsEvents(t, repo, eventA, eventB)
	enabled := []EnabledSkillInfo{{Handle: "evaluate_task_goal", Scope: models.SkillScopeAgentOwned, Enabled: true}}

	underusedA, err := repo.GetUnderusedSkills(ctx, SkillAnalyticsFilter{ProjectID: projectA.ID}, enabled)
	if err != nil {
		t.Fatalf("GetUnderusedSkills project A: %v", err)
	}
	metricA := findUnderusedSkill(underusedA, "evaluate_task_goal")
	if metricA == nil || metricA.ActivityCount != 1 || metricA.SelectedCount != 1 || metricA.LastActivity == nil || !metricA.LastActivity.Equal(now) {
		t.Fatalf("project A metric = %+v", metricA)
	}

	underusedB, err := repo.GetUnderusedSkills(ctx, SkillAnalyticsFilter{ProjectID: projectB.ID}, enabled)
	if err != nil {
		t.Fatalf("GetUnderusedSkills project B: %v", err)
	}
	metricB := findUnderusedSkill(underusedB, "evaluate_task_goal")
	if metricB == nil || metricB.ActivityCount != 1 || metricB.SelectedCount != 1 || metricB.LastActivity == nil || !metricB.LastActivity.Equal(now.Add(time.Minute)) {
		t.Fatalf("project B metric = %+v", metricB)
	}
}

func defaultProjectID(t *testing.T, db interface {
	QueryRow(query string, args ...any) *sql.Row
}) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM projects ORDER BY is_default DESC, name ASC LIMIT 1`).Scan(&id); err != nil {
		t.Fatalf("default project id: %v", err)
	}
	return id
}

func recordSkillAnalyticsEvents(t *testing.T, repo *SkillAnalyticsRepo, events ...models.SkillAnalyticsEvent) {
	t.Helper()
	for i := range events {
		if err := repo.RecordEvent(context.Background(), &events[i]); err != nil {
			t.Fatalf("RecordEvent(%+v): %v", events[i], err)
		}
	}
}

func skillEvent(projectID, threadID, agentID, scope, handle, eventType, source string, at time.Time) models.SkillAnalyticsEvent {
	return models.SkillAnalyticsEvent{
		CreatedAt:   at,
		ProjectID:   projectID,
		ThreadID:    threadID,
		AgentID:     agentID,
		SkillScope:  scope,
		SkillHandle: handle,
		EventType:   eventType,
		Source:      source,
		Surface:     models.SkillSurfaceTaskThread,
	}
}

func findTopSkill(rows []models.SkillAnalyticsSkillMetric, handle string) *models.SkillAnalyticsSkillMetric {
	for i := range rows {
		if rows[i].SkillHandle == handle {
			return &rows[i]
		}
	}
	return nil
}

func findFollowSkill(rows []models.SkillFollowThroughMetric, handle string) *models.SkillFollowThroughMetric {
	for i := range rows {
		if rows[i].SkillHandle == handle {
			return &rows[i]
		}
	}
	return nil
}

func findAgentUsageCell(rows []models.SkillAgentUsageCell, agentID, handle, scope string) *models.SkillAgentUsageCell {
	for i := range rows {
		if rows[i].AgentID == agentID && rows[i].SkillHandle == handle && rows[i].SkillScope == scope {
			return &rows[i]
		}
	}
	return nil
}

func findUnderusedSkill(rows []models.UnderusedSkillMetric, handle string) *models.UnderusedSkillMetric {
	for i := range rows {
		if rows[i].SkillHandle == handle {
			return &rows[i]
		}
	}
	return nil
}
