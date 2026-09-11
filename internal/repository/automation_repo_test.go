package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
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
	selectorItems, err := repo.ListBreadcrumbSelector(ctx, projectID, "", definition.Automation.ID, 20)
	if err != nil || len(selectorItems) != 1 || selectorItems[0].ID != definition.Automation.ID || selectorItems[0].Name != publication.Name {
		t.Fatalf("ListBreadcrumbSelector = %#v, %v", selectorItems, err)
	}
	selectorItems, err = repo.ListBreadcrumbSelector(ctx, projectID, "night", definition.Automation.ID, 20)
	if err != nil || len(selectorItems) != 1 || selectorItems[0].ID != definition.Automation.ID {
		t.Fatalf("search matching current Automation must return it selected = %#v, %v", selectorItems, err)
	}
	selectorItems, err = repo.ListBreadcrumbSelector(ctx, projectID, "missing", definition.Automation.ID, 20)
	if err != nil || len(selectorItems) != 0 {
		t.Fatalf("nonmatching search retained current Automation = %#v, %v", selectorItems, err)
	}
	saved, err := repo.ListSavedByProject(ctx, projectID)
	if err != nil || len(saved) != 1 || saved[0].PublishedVersionID == nil {
		t.Fatalf("ListSavedByProject = %#v, %v", saved, err)
	}
	cards, err := repo.ListPortfolioCards(ctx, projectID)
	if err != nil || len(cards) != 1 || cards[0].Version.ID != definition.Version.ID || cards[0].GraphNodeCount != len(definition.Nodes) {
		t.Fatalf("ListPortfolioCards = %#v, %v", cards, err)
	}
	pagedCards, err := repo.ListPortfolioCardsPage(ctx, projectID, 20, 0, "")
	if err != nil || len(pagedCards) != 1 || pagedCards[0].GraphNodeCount != len(definition.Nodes) {
		t.Fatalf("ListPortfolioCardsPage = %#v, %v", pagedCards, err)
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

	candidateJSON, err := json.Marshal(models.AutomationDraftCandidate{Nodes: []models.AutomationDraftNode{
		{Key: "trigger", Type: models.AutomationNodeTrigger, Role: "trigger", Config: map[string]any{}},
		{Key: "task", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"category": string(models.CategoryActive)}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO automation_graph_metadata
		(version_id, project_id, automation_id, candidate_json, assumptions_json, warnings_json, validation_json)
		VALUES (?, ?, ?, ?, '[]', '[]', '[]')`, definition.Version.ID, projectID, definition.Automation.ID, string(candidateJSON)); err != nil {
		t.Fatalf("insert candidate metadata: %v", err)
	}
	if err := repo.SetAutomationLifecycle(ctx, projectID, definition.Automation.ID, models.AutomationPaused); err != nil {
		t.Fatalf("pause candidate-backed automation: %v", err)
	}
	activeTail := &models.Task{ProjectID: projectID, Title: "Existing active resume tail", Prompt: "tail", Category: models.CategoryActive, Status: models.StatusPending, Priority: 1}
	if err := NewTaskRepo(db, nil).Create(ctx, activeTail); err != nil {
		t.Fatalf("create active resume tail: %v", err)
	}
	admitted, err = repo.ResumeAutomation(ctx, projectID, definition.Automation.ID)
	if err != nil {
		t.Fatalf("resume candidate-backed automation: %v", err)
	}
	if len(admitted) != 1 || admitted[0].ID != task.ID || admitted[0].DisplayOrder <= activeTail.DisplayOrder {
		t.Fatalf("admitted tasks = %#v, want lifecycle task appended after active tail order %d", admitted, activeTail.DisplayOrder)
	}
	storedTask, err := NewTaskRepo(db, nil).GetByID(ctx, task.ID)
	if err != nil || storedTask == nil || storedTask.Category != models.CategoryActive || storedTask.DisplayOrder != admitted[0].DisplayOrder {
		t.Fatalf("stored admitted task = %#v, err=%v", storedTask, err)
	}

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

func TestAutomationRepoDeleteGitHubSDLCTriggerOwnershipBoundaries(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectID := "automation-delete-github-sdlc"
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES (?, 'GitHub SDLC delete', '', '')`, projectID); err != nil {
		t.Fatal(err)
	}
	taskRepo := NewTaskRepo(db, nil)
	scheduleRepo := NewScheduleRepo(db)
	repo := NewAutomationRepo(db)
	broadcaster := events.NewBroadcaster()
	repo.SetBroadcaster(broadcaster)
	subscriber, err := broadcaster.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	defer broadcaster.Unsubscribe(subscriber)

	triggerNames := []string{"Vision Suggestions", "Bug Finder", "Optimization Finder", "Redundancy Finder", "Dev Inbox"}
	nodes := make([]models.AutomationNodeSpec, 0, len(triggerNames))
	resources := make([]models.AutomationResourceBinding, 0, len(triggerNames)*2)
	triggerTasks := make([]*models.Task, 0, len(triggerNames))
	for i, name := range triggerNames {
		task := createAutomationDeleteTask(t, ctx, taskRepo, projectID, name)
		schedule := createAutomationDeleteSchedule(t, ctx, scheduleRepo, task.ID, time.Duration(i+1)*time.Hour)
		nodeKey := strings.ReplaceAll(strings.ToLower(name), " ", "_")
		nodes = append(nodes, models.AutomationNodeSpec{Key: nodeKey, Name: name, Type: models.AutomationNodeTrigger, Role: "scheduled_task"})
		resources = append(resources,
			models.AutomationResourceBinding{NodeKey: nodeKey, ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"},
			models.AutomationResourceBinding{NodeKey: nodeKey, ResourceType: "task", ResourceID: task.ID, Relation: "owned"})
		triggerTasks = append(triggerTasks, task)
	}
	definition, _, err := repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: projectID, StableKey: "github-sdlc/delete", Name: "GitHub SDLC", AutomationType: "github_sdlc", AdapterKey: "github_sdlc",
		Nodes: nodes, Resources: resources,
	})
	if err != nil {
		t.Fatal(err)
	}

	sharedSchedule := createAutomationDeleteSchedule(t, ctx, scheduleRepo, triggerTasks[1].ID, 12*time.Hour)
	survivorTrigger := createAutomationDeleteTask(t, ctx, taskRepo, projectID, "Surviving Automation trigger")
	survivorSchedule := createAutomationDeleteSchedule(t, ctx, scheduleRepo, survivorTrigger.ID, 13*time.Hour)
	survivor, _, err := repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: projectID, StableKey: "survivor", Name: "Survivor", AutomationType: "custom", AdapterKey: "custom",
		Nodes: []models.AutomationNodeSpec{
			{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "scheduled_task"},
			{Key: "shared", Name: "Shared", Type: models.AutomationNodeAgentTask, Role: "task"},
		},
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "trigger", ResourceType: "schedule", ResourceID: survivorSchedule.ID, Relation: "owned"},
			{NodeKey: "trigger", ResourceType: "task", ResourceID: survivorTrigger.ID, Relation: "owned"},
			{NodeKey: "shared", ResourceType: "task", ResourceID: triggerTasks[2].ID, Relation: "shared"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	implementation := createAutomationDeleteTask(t, ctx, taskRepo, projectID, "Generated implementation")
	outcome := createAutomationDeleteTask(t, ctx, taskRepo, projectID, "Generated outcome")
	unrelated := createAutomationDeleteTask(t, ctx, taskRepo, projectID, "Unrelated domain task")
	for {
		select {
		case <-subscriber:
			continue
		default:
		}
		break
	}

	if err := repo.DeleteAutomation(ctx, projectID, definition.Automation.ID); err != nil {
		t.Fatal(err)
	}
	assertRowMissing(t, db, "automations", definition.Automation.ID)
	for _, index := range []int{0, 3, 4} {
		assertRowMissing(t, db, "tasks", triggerTasks[index].ID)
	}
	for _, taskID := range []string{triggerTasks[1].ID, triggerTasks[2].ID, survivorTrigger.ID, implementation.ID, outcome.ID, unrelated.ID} {
		assertRowPresent(t, db, "tasks", taskID)
	}
	assertRowPresent(t, db, "schedules", sharedSchedule.ID)
	assertRowPresent(t, db, "automations", survivor.Automation.ID)

	var automationInvalidation, taskInvalidations int
	for i := 0; i < 4; i++ {
		event := <-subscriber
		switch event.Type {
		case events.AutomationDefinitionUpdated:
			automationInvalidation++
		case events.TaskBoardUpdated:
			taskInvalidations++
		}
	}
	if automationInvalidation != 1 || taskInvalidations != 3 {
		t.Fatalf("invalidations automation=%d task=%d, want 1 and 3", automationInvalidation, taskInvalidations)
	}
}

func TestAutomationRepoDeleteBulkMatchesSingleAndRollsBackFailures(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectID := "automation-delete-bulk"
	foreignProjectID := "automation-delete-bulk-foreign"
	for _, id := range []string{projectID, foreignProjectID} {
		if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, name, description, repo_path) VALUES (?, ?, '', '')`, id, id); err != nil {
			t.Fatal(err)
		}
	}
	taskRepo := NewTaskRepo(db, nil)
	scheduleRepo := NewScheduleRepo(db)
	repo := NewAutomationRepo(db)
	firstAutomationID, firstTaskID, firstScheduleID := createOwnedTriggerAutomation(t, ctx, repo, taskRepo, scheduleRepo, projectID, "bulk-first")
	secondAutomationID, secondTaskID, secondScheduleID := createOwnedTriggerAutomation(t, ctx, repo, taskRepo, scheduleRepo, projectID, "bulk-second")

	if err := repo.DeleteAutomations(ctx, foreignProjectID, []string{firstAutomationID}); err == nil {
		t.Fatal("foreign-project delete should fail")
	}
	for _, row := range []struct{ table, id string }{{"automations", firstAutomationID}, {"tasks", firstTaskID}, {"schedules", firstScheduleID}} {
		assertRowPresent(t, db, row.table, row.id)
	}

	if _, err := db.ExecContext(ctx, `CREATE TRIGGER fail_automation_delete BEFORE DELETE ON automations WHEN OLD.id = '`+firstAutomationID+`' BEGIN SELECT RAISE(ABORT, 'forced delete failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAutomations(ctx, projectID, []string{firstAutomationID, secondAutomationID}); err == nil {
		t.Fatal("forced transaction failure should be returned")
	}
	for _, row := range []struct{ table, id string }{
		{"automations", firstAutomationID}, {"tasks", firstTaskID}, {"schedules", firstScheduleID},
		{"automations", secondAutomationID}, {"tasks", secondTaskID}, {"schedules", secondScheduleID},
	} {
		assertRowPresent(t, db, row.table, row.id)
	}
	if _, err := db.ExecContext(ctx, `DROP TRIGGER fail_automation_delete`); err != nil {
		t.Fatal(err)
	}

	if err := repo.DeleteAutomations(ctx, projectID, []string{firstAutomationID, secondAutomationID}); err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct{ table, id string }{
		{"automations", firstAutomationID}, {"tasks", firstTaskID}, {"schedules", firstScheduleID},
		{"automations", secondAutomationID}, {"tasks", secondTaskID}, {"schedules", secondScheduleID},
	} {
		assertRowMissing(t, db, row.table, row.id)
	}
}

func createOwnedTriggerAutomation(t *testing.T, ctx context.Context, repo *AutomationRepo, taskRepo *TaskRepo, scheduleRepo *ScheduleRepo, projectID, key string) (string, string, string) {
	t.Helper()
	task := createAutomationDeleteTask(t, ctx, taskRepo, projectID, key)
	schedule := createAutomationDeleteSchedule(t, ctx, scheduleRepo, task.ID, time.Hour)
	definition, _, err := repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: projectID, StableKey: key, Name: key, AutomationType: "custom", AdapterKey: "custom",
		Nodes: []models.AutomationNodeSpec{{Key: "trigger", Name: "Trigger", Type: models.AutomationNodeTrigger, Role: "scheduled_task"}},
		Resources: []models.AutomationResourceBinding{
			{NodeKey: "trigger", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"},
			{NodeKey: "trigger", ResourceType: "task", ResourceID: task.ID, Relation: "owned"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return definition.Automation.ID, task.ID, schedule.ID
}

func createAutomationDeleteTask(t *testing.T, ctx context.Context, repo *TaskRepo, projectID, title string) *models.Task {
	t.Helper()
	task := &models.Task{ProjectID: projectID, Title: title, Prompt: title, Category: models.CategoryScheduled, Status: models.StatusPending, Priority: 1}
	if err := repo.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	return task
}

func createAutomationDeleteSchedule(t *testing.T, ctx context.Context, repo *ScheduleRepo, taskID string, offset time.Duration) *models.Schedule {
	t.Helper()
	runAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC).Add(offset)
	schedule := &models.Schedule{TaskID: taskID, RunAt: runAt, NextRun: &runAt, RepeatType: models.RepeatDaily, RepeatInterval: 1, Enabled: true}
	if err := repo.Create(ctx, schedule); err != nil {
		t.Fatal(err)
	}
	return schedule
}

func assertRowPresent(t *testing.T, db *sql.DB, table, id string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("%s %s should be present", table, id)
	}
}

func assertRowMissing(t *testing.T, db *sql.DB, table, id string) {
	t.Helper()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id = ?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("%s %s should be missing", table, id)
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
