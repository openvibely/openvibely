package repository

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestAutomationRepoPublishRegisteredAndQuerySurfaces(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectID := "automation-repo-project"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Automation repo', '', '')`, projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	task := &models.Task{ProjectID: projectID, Title: "Run registered automation", Prompt: "go", Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 1}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	due := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	schedule := models.Schedule{TaskID: task.ID, RunAt: due, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: false, NextRun: &due}
	if err := NewScheduleRepo(db).Create(ctx, &schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	repo := NewAutomationRepo(db)
	if repo.DB() != db {
		t.Fatal("DB should expose the backing database")
	}
	publication := models.AutomationRegisteredPublication{
		ProjectID:      projectID,
		StableKey:      "registered/nightly",
		Name:           "Nightly automation",
		Description:    "Runs nightly",
		AutomationType: "scheduled",
		AdapterKey:     "custom",
		CreatedVia:     "test",
		Nodes: []models.AutomationNodeSpec{
			{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger", PositionX: 0, PositionY: 0},
			{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task", PositionX: 1, PositionY: 0},
		},
		Edges: []models.AutomationEdgeSpec{{Key: "trigger-task", SourceNodeKey: "trigger", TargetNodeKey: "task", Label: "Run", DisplayOrder: 1}},
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"},
			{NodeKey: "task", ResourceType: "task", ResourceID: task.ID, Relation: "owned"},
		},
	}
	definition, retained, err := repo.PublishRegistered(ctx, publication)
	if err != nil {
		t.Fatalf("PublishRegistered: %v", err)
	}
	if retained || definition.Automation.ID == "" || definition.Automation.LifecycleState != models.AutomationActive || len(definition.Nodes) != 2 || len(definition.Edges) != 1 || len(definition.Resources) != 2 {
		t.Fatalf("unexpected definition retained=%v definition=%#v", retained, definition)
	}

	listed, err := repo.ListByProject(ctx, projectID, 0)
	if err != nil || len(listed) != 1 || listed[0].ID != definition.Automation.ID {
		t.Fatalf("ListByProject = %#v, %v", listed, err)
	}
	selectorItems, err := repo.ListBreadcrumbSelector(ctx, projectID, "night", definition.Automation.ID, 20)
	if err != nil || len(selectorItems) != 1 || selectorItems[0].ID != definition.Automation.ID || selectorItems[0].Name != publication.Name {
		t.Fatalf("ListBreadcrumbSelector = %#v, %v", selectorItems, err)
	}
	selectorItems, err = repo.ListBreadcrumbSelector(ctx, projectID, "no name matches this", definition.Automation.ID, 20)
	if err != nil || len(selectorItems) != 1 || selectorItems[0].ID != definition.Automation.ID {
		t.Fatalf("ListBreadcrumbSelector must retain current Automation while filtering = %#v, %v", selectorItems, err)
	}
	saved, err := repo.ListSavedByProject(ctx, projectID)
	if err != nil || len(saved) != 1 || saved[0].PublishedVersionID == nil {
		t.Fatalf("ListSavedByProject = %#v, %v", saved, err)
	}
	cards, err := repo.ListPortfolioCards(ctx, projectID)
	if err != nil || len(cards) != 1 || cards[0].Version.ID != definition.Version.ID {
		t.Fatalf("ListPortfolioCards = %#v, %v", cards, err)
	}
	byKey, err := repo.GetByStableKey(ctx, projectID, publication.StableKey)
	if err != nil || byKey == nil || byKey.ID != definition.Automation.ID {
		t.Fatalf("GetByStableKey = %#v, %v", byKey, err)
	}
	loaded, err := repo.GetDefinition(ctx, projectID, definition.Automation.ID)
	if err != nil || loaded == nil || loaded.Version.ID != definition.Version.ID {
		t.Fatalf("GetDefinition = %#v, %v", loaded, err)
	}
	missing, err := repo.GetDefinition(ctx, projectID, "missing")
	if err != nil || missing != nil {
		t.Fatalf("missing GetDefinition = %#v, %v", missing, err)
	}
	resources, err := repo.ListResourceSummaries(ctx, projectID, definition.Automation.ID, definition.Version.ID, -1)
	if err != nil || len(resources) != 2 {
		t.Fatalf("ListResourceSummaries = %#v, %v", resources, err)
	}

	retainedDefinition, retained, err := repo.PublishRegistered(ctx, publication)
	if err != nil || !retained || retainedDefinition.Version.ID != definition.Version.ID {
		t.Fatalf("retained PublishRegistered definition=%#v retained=%v err=%v", retainedDefinition, retained, err)
	}
	publication.AdapterKey = "github_sdlc"
	if _, _, err := repo.PublishRegistered(ctx, publication); err == nil || !strings.Contains(err.Error(), "adapter cannot change") {
		t.Fatalf("expected adapter change error, got %v", err)
	}
}

func TestAutomationRepoExistsUsesProjectScopedIdentityLookup(t *testing.T) {
	db, counter := testutil.NewStatementCountingTestDB(t)
	ctx := context.Background()
	projectRepo := NewProjectRepo(db)
	project := models.Project{Name: "Automation existence project"}
	otherProject := models.Project{Name: "Other existence project"}
	if err := projectRepo.Create(ctx, &project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	if err := projectRepo.Create(ctx, &otherProject); err != nil {
		t.Fatalf("create other project: %v", err)
	}
	automationID := NewID()
	if _, err := db.ExecContext(ctx, `INSERT INTO automations (id, project_id, stable_key, name) VALUES (?, ?, ?, ?)`, automationID, project.ID, "exists/automation", "Exists automation"); err != nil {
		t.Fatalf("insert automation: %v", err)
	}

	repo := NewAutomationRepo(db)
	counter.Reset()
	counter.SetEnabled(true)
	exists, err := repo.Exists(ctx, project.ID, automationID)
	if err != nil {
		t.Fatalf("Exists existing automation: %v", err)
	}
	if !exists {
		t.Fatal("Exists existing automation = false, want true")
	}
	statements := counter.Statements()
	if len(statements) != 1 || statements[0] != "SELECT EXISTS(SELECT 1 FROM automations WHERE project_id = ? AND id = ?)" {
		t.Fatalf("Exists statements = %#v, want one identity-only query", statements)
	}

	counter.Reset()
	exists, err = repo.Exists(ctx, otherProject.ID, automationID)
	if err != nil {
		t.Fatalf("Exists project-mismatched automation: %v", err)
	}
	if exists {
		t.Fatal("Exists project-mismatched automation = true, want false")
	}
	counter.Reset()
	exists, err = repo.Exists(ctx, project.ID, "missing")
	if err != nil {
		t.Fatalf("Exists missing automation: %v", err)
	}
	if exists {
		t.Fatal("Exists missing automation = true, want false")
	}
}

func TestAutomationRepoPublishRegisteredValidationErrors(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectID := "automation-repo-validation"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Automation validation', '', '')`, projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	repo := NewAutomationRepo(db)
	base := models.AutomationRegisteredPublication{
		ProjectID:      projectID,
		StableKey:      "registered/invalid",
		Name:           "Invalid automation",
		AutomationType: "scheduled",
		AdapterKey:     "custom",
		Nodes: []models.AutomationNodeSpec{
			{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"},
			{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task"},
		},
	}
	unknownEdge := base
	unknownEdge.Edges = []models.AutomationEdgeSpec{{Key: "bad", SourceNodeKey: "trigger", TargetNodeKey: "missing"}}
	if _, _, err := repo.PublishRegistered(ctx, unknownEdge); err == nil || !strings.Contains(err.Error(), "unknown node") {
		t.Fatalf("expected unknown node error, got %v", err)
	}
	emptyResource := base
	emptyResource.Resources = []models.AutomationResourceBinding{{NodeKey: "trigger", ResourceType: "task"}}
	if _, _, err := repo.PublishRegistered(ctx, emptyResource); err == nil || !strings.Contains(err.Error(), "resource ID is required") {
		t.Fatalf("expected empty resource error, got %v", err)
	}
	noTrigger := base
	if _, _, err := repo.PublishRegistered(ctx, noTrigger); err == nil || !strings.Contains(err.Error(), "requires at least one trigger schedule") {
		t.Fatalf("expected missing trigger schedule error, got %v", err)
	}
}

func TestAutomationRepoLifecyclePauseResumeArchiveAndDelete(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectID := "automation-repo-lifecycle"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'Automation lifecycle', '', '')`, projectID); err != nil {
		t.Fatalf("insert project: %v", err)
	}
	task := &models.Task{ProjectID: projectID, Title: "Lifecycle task", Prompt: "go", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := NewTaskRepo(db, nil).Create(ctx, task); err != nil {
		t.Fatalf("create task: %v", err)
	}
	due := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	schedule := models.Schedule{TaskID: task.ID, RunAt: due, RepeatType: models.RepeatOnce, RepeatInterval: 1, Enabled: false, NextRun: &due}
	if err := NewScheduleRepo(db).Create(ctx, &schedule); err != nil {
		t.Fatalf("create schedule: %v", err)
	}
	repo := NewAutomationRepo(db)
	definition, _, err := repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID:      projectID,
		StableKey:      "registered/lifecycle",
		Name:           "Lifecycle automation",
		AutomationType: "scheduled",
		AdapterKey:     "custom",
		Nodes: []models.AutomationNodeSpec{
			{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "trigger"},
			{Key: "task", Name: "Task", Type: models.AutomationNodeAgentTask, Role: "task"},
		},
		Edges: []models.AutomationEdgeSpec{{Key: "trigger-task", SourceNodeKey: "trigger", TargetNodeKey: "task"}},
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"},
			{NodeKey: "task", ResourceType: "task", ResourceID: task.ID, Relation: "owned"},
		},
	})
	if err != nil {
		t.Fatalf("PublishRegistered: %v", err)
	}

	if err := repo.SetAutomationLifecycle(ctx, projectID, definition.Automation.ID, models.AutomationPaused); err != nil {
		t.Fatalf("pause automation: %v", err)
	}
	assertAutomationLifecycle(t, db, projectID, definition.Automation.ID, models.AutomationPaused)
	assertScheduleEnabled(t, db, schedule.ID, false)
	assertTriggerOwnerState(t, db, schedule.ID, "paused")
	assertTaskCategory(t, db, task.ID, models.CategoryBacklog)

	admitted, err := repo.ResumeAutomation(ctx, projectID, definition.Automation.ID)
	if err != nil {
		t.Fatalf("resume automation: %v", err)
	}
	if len(admitted) != 0 {
		t.Fatalf("registered publication without candidate metadata should not admit tasks, got %#v", admitted)
	}
	assertAutomationLifecycle(t, db, projectID, definition.Automation.ID, models.AutomationActive)
	assertScheduleEnabled(t, db, schedule.ID, true)
	assertTriggerOwnerState(t, db, schedule.ID, "active")
	assertTaskCategory(t, db, task.ID, models.CategoryBacklog)

	if err := repo.SetAutomationLifecycle(ctx, projectID, definition.Automation.ID, models.AutomationArchived); err != nil {
		t.Fatalf("archive automation: %v", err)
	}
	assertAutomationLifecycle(t, db, projectID, definition.Automation.ID, models.AutomationArchived)
	assertScheduleEnabled(t, db, schedule.ID, false)
	assertTriggerOwnerState(t, db, schedule.ID, "archived")
	if err := repo.SetAutomationLifecycle(ctx, projectID, definition.Automation.ID, models.AutomationActive); err == nil || !strings.Contains(err.Error(), "archived automation") {
		t.Fatalf("expected archived resume error, got %v", err)
	}
	if err := repo.SetAutomationLifecycle(ctx, projectID, definition.Automation.ID, models.AutomationLifecycleState("draft")); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported lifecycle error, got %v", err)
	}
	if err := repo.DeleteAutomation(ctx, projectID, definition.Automation.ID); err != nil {
		t.Fatalf("DeleteAutomation: %v", err)
	}
	if loaded, err := repo.GetDefinition(ctx, projectID, definition.Automation.ID); err != nil || loaded != nil {
		t.Fatalf("deleted automation should be gone, got %#v err=%v", loaded, err)
	}
	if err := repo.DeleteAutomation(ctx, projectID, definition.Automation.ID); err == nil || !strings.Contains(err.Error(), "automation not found") {
		t.Fatalf("expected delete missing error, got %v", err)
	}
}

func assertAutomationLifecycle(t *testing.T, db *sql.DB, projectID, automationID string, want models.AutomationLifecycleState) {
	t.Helper()
	var got models.AutomationLifecycleState
	if err := db.QueryRow(`SELECT lifecycle_state FROM automations WHERE project_id = ? AND id = ?`, projectID, automationID).Scan(&got); err != nil {
		t.Fatalf("query lifecycle: %v", err)
	}
	if got != want {
		t.Fatalf("lifecycle = %q, want %q", got, want)
	}
}

func assertScheduleEnabled(t *testing.T, db *sql.DB, scheduleID string, want bool) {
	t.Helper()
	var got bool
	if err := db.QueryRow(`SELECT enabled FROM schedules WHERE id = ?`, scheduleID).Scan(&got); err != nil {
		t.Fatalf("query schedule enabled: %v", err)
	}
	if got != want {
		t.Fatalf("schedule enabled = %v, want %v", got, want)
	}
}

func assertTriggerOwnerState(t *testing.T, db *sql.DB, scheduleID, want string) {
	t.Helper()
	var got string
	if err := db.QueryRow(`SELECT ownership_state FROM automation_trigger_owners WHERE schedule_id = ?`, scheduleID).Scan(&got); err != nil {
		t.Fatalf("query trigger owner: %v", err)
	}
	if got != want {
		t.Fatalf("trigger owner state = %q, want %q", got, want)
	}
}

func assertTaskCategory(t *testing.T, db *sql.DB, taskID string, want models.TaskCategory) {
	t.Helper()
	var got models.TaskCategory
	if err := db.QueryRow(`SELECT category FROM tasks WHERE id = ?`, taskID).Scan(&got); err != nil {
		t.Fatalf("query task category: %v", err)
	}
	if got != want {
		t.Fatalf("task category = %q, want %q", got, want)
	}
}
