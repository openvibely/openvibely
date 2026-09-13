package repository

import (
	"context"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestSkillAnalyticsEvidenceFiltersExactSupportingRecords(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewSkillAnalyticsRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Skill evidence", RepoPath: "/skill-evidence"}
	otherProject := &models.Project{Name: "Other skill evidence", RepoPath: "/other-skill-evidence"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := NewProjectRepo(db).Create(ctx, otherProject); err != nil {
		t.Fatal(err)
	}
	agent := &models.Agent{Name: "Evidence Agent", SystemPrompt: "work", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	otherAgent := &models.Agent{Name: "Other Evidence Agent", SystemPrompt: "work", Model: "inherit", Enabled: true, SelectableAsPrimary: true}
	if err := NewAgentRepo(db).Create(ctx, agent); err != nil {
		t.Fatal(err)
	}
	if err := NewAgentRepo(db).Create(ctx, otherAgent); err != nil {
		t.Fatal(err)
	}
	occurred := time.Date(2026, 1, 10, 15, 0, 0, 0, time.UTC)
	for _, event := range []*models.SkillAnalyticsEvent{
		{ProjectID: project.ID, AgentID: agent.ID, SkillHandle: "project:review", SkillScope: models.SkillScopeProject, EventType: models.SkillEventCreated, Source: models.SkillEventSourceManual, Surface: models.SkillSurfaceTaskThread, CreatedAt: occurred},
		{ProjectID: project.ID, AgentID: agent.ID, SkillHandle: "project:review", SkillScope: models.SkillScopeProject, EventType: models.SkillEventSelected, Source: models.SkillEventSourceManual, Surface: models.SkillSurfaceTaskThread, CreatedAt: occurred},
		{ProjectID: project.ID, AgentID: otherAgent.ID, SkillHandle: "project:other", SkillScope: models.SkillScopeProject, EventType: models.SkillEventCreated, Source: models.SkillEventSourceManual, Surface: models.SkillSurfaceTaskThread, CreatedAt: occurred},
		{ProjectID: otherProject.ID, AgentID: agent.ID, SkillHandle: "project:review", SkillScope: models.SkillScopeProject, EventType: models.SkillEventCreated, Source: models.SkillEventSourceManual, Surface: models.SkillSurfaceTaskThread, CreatedAt: occurred},
	} {
		if err := repo.RecordEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	period := occurred.In(time.Local).Format("2006-01-02")
	rows, total, err := repo.GetEvidence(ctx, SkillAnalyticsFilter{ProjectID: project.ID, GroupBy: "day", EvidencePeriod: period, EvidenceEvent: "created", EvidenceAgentID: agent.ID, EvidenceSkillHandle: "project:review", EvidenceLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ProjectID != project.ID || rows[0].AgentID != agent.ID || rows[0].AgentName != agent.Name || rows[0].SkillHandle != "project:review" || rows[0].EventType != models.SkillEventCreated {
		t.Fatalf("skill evidence did not preserve exact project/period/event/Agent/skill subset: total=%d rows=%+v", total, rows)
	}
	rows, total, err = repo.GetEvidence(ctx, SkillAnalyticsFilter{ProjectID: project.ID, GroupBy: "day", EvidencePeriod: period, EvidenceEvent: "used", EvidenceAgentID: agent.ID, EvidenceSkillHandle: "project:review", EvidenceLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].EventType != models.SkillEventSelected {
		t.Fatalf("used Agent-skill evidence must include only selected, loaded, or viewed events: total=%d rows=%+v", total, rows)
	}
}

func TestUsageEvidenceFiltersExactSupportingRecords(t *testing.T) {
	db := testutil.NewTestDB(t)
	repo := NewUsageRepo(db)
	ctx := context.Background()
	project := &models.Project{Name: "Usage evidence", RepoPath: "/usage-evidence"}
	otherProject := &models.Project{Name: "Other usage evidence", RepoPath: "/other-usage-evidence"}
	if err := NewProjectRepo(db).Create(ctx, project); err != nil {
		t.Fatal(err)
	}
	if err := NewProjectRepo(db).Create(ctx, otherProject); err != nil {
		t.Fatal(err)
	}
	occurred := time.Date(2026, 1, 10, 15, 0, 0, 0, time.UTC)
	for _, event := range []*models.LLMUsageEvent{
		{Provider: "openai", ProjectID: project.ID, Model: "gpt-a", Operation: "task", Status: "completed", InputTokens: 10, OutputTokens: 5, OccurredAt: occurred},
		{Provider: "openai", ProjectID: project.ID, Model: "gpt-b", Operation: "task", Status: "completed", InputTokens: 20, OutputTokens: 5, OccurredAt: occurred},
		{Provider: "openai", ProjectID: otherProject.ID, Model: "gpt-a", Operation: "task", Status: "completed", InputTokens: 30, OutputTokens: 5, OccurredAt: occurred},
	} {
		if err := repo.RecordUsageEvent(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	period := usageLocalPeriod(occurred, "day")
	rows, total, err := repo.GetEvidence(ctx, UsageFilter{ProjectID: project.ID, GroupBy: "day", EvidencePeriod: period, EvidenceProvider: "openai", EvidenceModel: "gpt-a", EvidenceLimit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(rows) != 1 || rows[0].ProjectID != project.ID || rows[0].Provider != "openai" || rows[0].Model != "gpt-a" || rows[0].TotalTokens != 15 {
		t.Fatalf("usage evidence did not preserve exact project/period/provider/model subset: total=%d rows=%+v", total, rows)
	}
}
