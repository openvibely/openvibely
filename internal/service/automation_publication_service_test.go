package service

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

type automationSaveHarness struct {
	db             *sql.DB
	project        models.Project
	automationRepo *repository.AutomationRepo
	taskRepo       *repository.TaskRepo
	scheduleRepo   *repository.ScheduleRepo
	drafts         *AutomationDraftService
	compiler       *AutomationCompiler
	lifecycle      *AutomationLifecycleService
}

type recordingAutomationTaskService struct {
	*TaskService
	submitted []models.Task
}

func (s *recordingAutomationTaskService) SubmitSavedAutomationTask(task models.Task) {
	s.submitted = append(s.submitted, task)
}

func newAutomationSaveHarness(t *testing.T, name string) automationSaveHarness {
	t.Helper()
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), name)
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	planner := NewAutomationSaveValidator(registry, drafts)
	taskSvc := NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil)
	compiler := NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, planner)
	return automationSaveHarness{db: db, project: project, automationRepo: automationRepo, taskRepo: taskRepo,
		scheduleRepo: scheduleRepo, drafts: drafts, compiler: compiler,
		lifecycle: NewAutomationLifecycleService(automationRepo, scheduleRepo, taskSvc)}
}

func TestAutomationSaveCreatesCurrentGraphTaskAndScheduleAtomically(t *testing.T) {
	h := newAutomationSaveHarness(t, "Atomic custom Save")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Daily review", "Review one request.")

	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Equal(t, models.AutomationActive, saved.Definition.Automation.LifecycleState)
	require.Len(t, saved.Definition.Nodes, 2)
	require.Len(t, saved.Definition.Edges, 1)
	require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM automation_versions WHERE automation_id = ?`, saved.Definition.Automation.ID))
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, saved.Definition.Automation.ID))
	taskID := automationResourceID(t, saved.Definition, "schedule", "task")
	scheduleID := automationResourceID(t, saved.Definition, "schedule", "schedule")
	task, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, "Review one request.\n\nConnected Task handoff:\nDo not create or schedule the connected downstream Task yourself. OpenVibely activates it automatically after this task completes successfully.", task.Prompt)
	schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.Equal(t, taskID, schedule.TaskID)
	require.True(t, schedule.Enabled)
	require.False(t, tableExists(t, h.db, "automation_publication_attempts"))
	require.False(t, tableExists(t, h.db, "automation_publication_steps"))
}

func TestAutomationReplacementUsesOneCurrentGraphAndDeletesRemovedSchedule(t *testing.T) {
	h := newAutomationSaveHarness(t, "Atomic replacement")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Daily review", "Review one request.")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	oldGraphID := first.Definition.Version.ID
	oldScheduleID := automationResourceID(t, first.Definition, "schedule", "schedule")

	replacement := models.AutomationDraftCandidate{SchemaVersion: 1, Name: candidate.Name, Description: "Task only",
		AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{{Key: "followup", Name: "Follow up", Type: models.AutomationNodeAgentTask, Role: "task",
			Config:   map[string]any{"prompt": "Use the replacement instructions.", "category": "backlog", "priority": 2},
			Position: &models.AutomationDraftPoint{X: 0, Y: 0}}}, Edges: []models.AutomationDraftEdge{}}
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: replacement})
	require.NoError(t, err)
	require.NotEqual(t, oldGraphID, saved.Definition.Version.ID)
	require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM automation_versions WHERE automation_id = ?`, saved.Definition.Automation.ID))
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM automation_versions WHERE id = ?`, oldGraphID))
	removed, err := h.scheduleRepo.GetByID(ctx, oldScheduleID)
	require.NoError(t, err)
	require.Nil(t, removed)
}

func TestAutomationSaveRollsBackAllRowsWhenScheduleCreationFails(t *testing.T) {
	h := newAutomationSaveHarness(t, "Atomic automation save")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	_, err = h.db.ExecContext(ctx, `CREATE TRIGGER fail_atomic_automation_schedule
		BEFORE INSERT ON schedules BEGIN SELECT RAISE(ABORT, 'injected schedule failure'); END`)
	require.NoError(t, err)

	_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.ErrorContains(t, err, "injected schedule failure")
	for _, table := range []string{"automations", "automation_versions", "automation_nodes", "automation_edges", "automation_definition_resources", "tasks", "schedules"} {
		require.Zero(t, countRows(t, h.db, "SELECT COUNT(*) FROM "+table), table+" must remain empty after the failed atomic Save")
	}
}

func TestAutomationReplacementRollsBackToCurrentGraphWhenScheduleUpdateFails(t *testing.T) {
	h := newAutomationSaveHarness(t, "Atomic automation replacement")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	currentGraphID := first.Definition.Version.ID
	taskID := automationResourceID(t, first.Definition, "vision_driver", "task")
	originalTask, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)

	candidate.Nodes[0].Config["prompt"] = "replacement prompt that must roll back"
	_, err = h.db.ExecContext(ctx, `CREATE TRIGGER fail_atomic_automation_schedule_update
		BEFORE UPDATE ON schedules BEGIN SELECT RAISE(ABORT, 'injected schedule update failure'); END`)
	require.NoError(t, err)
	_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.ErrorContains(t, err, "injected schedule update failure")
	current, err := h.automationRepo.GetDefinition(ctx, h.project.ID, first.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, currentGraphID, current.Version.ID)
	storedTask, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, originalTask.Prompt, storedTask.Prompt)
	require.Equal(t, 1, countRows(t, h.db, `SELECT COUNT(*) FROM automation_versions WHERE automation_id = ?`, first.Definition.Automation.ID))
	require.False(t, tableExists(t, h.db, "automation_publication_attempts"))
}

func TestAutomationSavePreservesPausedAndArchivedLifecycle(t *testing.T) {
	h := newAutomationSaveHarness(t, "Atomic lifecycle Save")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, first.Definition.Automation.ID))
	candidate.Nodes[0].Config["prompt"] = "Paused replacement prompt"
	paused, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Equal(t, models.AutomationPaused, paused.Definition.Automation.LifecycleState)
	schedule, err := h.scheduleRepo.GetByID(ctx, automationResourceID(t, paused.Definition, "vision_driver", "schedule"))
	require.NoError(t, err)
	require.False(t, schedule.Enabled)

	require.NoError(t, h.lifecycle.Archive(ctx, h.project.ID, first.Definition.Automation.ID))
	candidate.Nodes[0].Config["prompt"] = "Archived replacement prompt"
	archived, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Equal(t, models.AutomationArchived, archived.Definition.Automation.LifecycleState)
	schedule, err = h.scheduleRepo.GetByID(ctx, automationResourceID(t, archived.Definition, "vision_driver", "schedule"))
	require.NoError(t, err)
	require.False(t, schedule.Enabled)
}

func TestAutomationPauseAndArchiveDemotePendingActiveRoots(t *testing.T) {
	for _, state := range []models.AutomationLifecycleState{models.AutomationPaused, models.AutomationArchived} {
		t.Run(string(state), func(t *testing.T) {
			h := newAutomationSaveHarness(t, "Lifecycle root admission "+string(state))
			ctx := context.Background()
			candidate := customTaskOnlyCandidate("Lifecycle root", "Wait for lifecycle changes.", models.CategoryActive)
			saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)
			taskID := automationResourceID(t, saved.Definition, "root", "task")

			if state == models.AutomationPaused {
				err = h.lifecycle.Pause(ctx, h.project.ID, saved.Definition.Automation.ID)
			} else {
				err = h.lifecycle.Archive(ctx, h.project.ID, saved.Definition.Automation.ID)
			}
			require.NoError(t, err)
			task, err := h.taskRepo.GetByID(ctx, taskID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, task.Category)
			require.Equal(t, models.StatusPending, task.Status)
		})
	}
}

func TestAutomationSavePreservesScheduleTimingWhenCadenceIsUnchanged(t *testing.T) {
	h := newAutomationSaveHarness(t, "Preserve schedule timing")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Scheduled review", "Review one request.")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	scheduleID := automationResourceID(t, first.Definition, "schedule", "schedule")
	preservedNextRun := time.Date(2042, time.March, 4, 5, 6, 7, 0, time.UTC)
	_, err = h.db.ExecContext(ctx, `UPDATE schedules SET next_run = ? WHERE id = ?`, preservedNextRun, scheduleID)
	require.NoError(t, err)
	before, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)

	candidate.Nodes[0].Config["prompt"] = "Only the task prompt changed."
	_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	after, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.Equal(t, before.RunAt, after.RunAt)
	require.NotNil(t, after.NextRun)
	require.Equal(t, preservedNextRun, *after.NextRun)
}

func TestAutomationRenameUpdatesBoundTaskTitles(t *testing.T) {
	h := newAutomationSaveHarness(t, "Rename task titles")
	ctx := context.Background()
	candidate := customTaskOnlyCandidate("Original automation", "Do the work.", models.CategoryBacklog)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	taskID := automationResourceID(t, first.Definition, "root", "task")

	candidate.Name = "Renamed automation"
	_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	task, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Contains(t, task.Title, "Renamed automation")
	require.NotContains(t, task.Title, "Original automation")
}

func TestAutomationSaveSubmitsExistingRootsThatBecomePendingActive(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, *sql.DB, string)
	}{
		{name: "backlog to active", setup: func(t *testing.T, db *sql.DB, taskID string) {
			_, err := db.Exec(`UPDATE tasks SET category = 'backlog' WHERE id = ?`, taskID)
			require.NoError(t, err)
		}},
		{name: "completed reset to pending", setup: func(t *testing.T, db *sql.DB, taskID string) {
			_, err := db.Exec(`UPDATE tasks SET status = 'completed' WHERE id = ?`, taskID)
			require.NoError(t, err)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAutomationSaveHarness(t, "Submit existing root "+test.name)
			ctx := context.Background()
			recorder := &recordingAutomationTaskService{TaskService: NewTaskService(h.taskRepo, repository.NewAttachmentRepo(h.db), nil)}
			h.compiler.taskSvc = recorder
			candidate := customTaskOnlyCandidate("Runnable root", "Run this root.", models.CategoryActive)
			first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)
			taskID := automationResourceID(t, first.Definition, "root", "task")
			recorder.submitted = nil
			test.setup(t, h.db, taskID)

			_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
				Source: "manual", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)
			require.Len(t, recorder.submitted, 1)
			require.Equal(t, taskID, recorder.submitted[0].ID)
			require.Equal(t, models.CategoryActive, recorder.submitted[0].Category)
			require.Equal(t, models.StatusPending, recorder.submitted[0].Status)
		})
	}
}

func TestAutomationDeleteRemovesOwnedScheduleAndPreservesTask(t *testing.T) {
	h := newAutomationSaveHarness(t, "Atomic delete")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	taskID := automationResourceID(t, saved.Definition, "vision_driver", "task")
	scheduleID := automationResourceID(t, saved.Definition, "vision_driver", "schedule")
	require.NoError(t, h.lifecycle.Delete(ctx, h.project.ID, saved.Definition.Automation.ID))
	task, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, task)
	schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.Nil(t, schedule)
}

func TestAutomationSavePreviewRejectsUnavailableGitHubIntegrationWithoutPersistence(t *testing.T) {
	h := newAutomationSaveHarness(t, "Atomic GitHub validation")
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	plan, _, err := h.compiler.PreviewSave(context.Background(), h.project.ID, candidate)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Validation)
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM automations`))
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM tasks`))
	require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM schedules`))
}

func customTaskOnlyCandidate(name, prompt string, category models.TaskCategory) models.AutomationDraftCandidate {
	return models.AutomationDraftCandidate{SchemaVersion: 1, Name: name, Description: "Atomic custom root",
		AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{{Key: "root", Name: "Root", Type: models.AutomationNodeAgentTask, Role: "task",
			Config: map[string]any{"prompt": prompt, "category": string(category), "priority": 2}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}}},
		Edges: []models.AutomationDraftEdge{}}
}

func customScheduledTaskCandidate(name, prompt string) models.AutomationDraftCandidate {
	return models.AutomationDraftCandidate{SchemaVersion: 1, Name: name, Description: "Atomic custom graph",
		AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{
			{Key: "schedule", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule",
				Config: map[string]any{"prompt": prompt, "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
			{Key: "followup", Name: "Follow up", Type: models.AutomationNodeAgentTask, Role: "task",
				Config: map[string]any{"prompt": "Follow up after review.", "category": "backlog", "priority": 2}, Position: &models.AutomationDraftPoint{X: 240, Y: 0}},
		},
		Edges: []models.AutomationDraftEdge{{Key: "schedule_followup", From: "schedule", To: "followup", FromPort: "right", ToPort: "left", Condition: map[string]any{}}}}
}

func automationResourceID(t *testing.T, definition *models.AutomationDefinition, nodeKey, resourceType string) string {
	t.Helper()
	for _, resource := range definition.Resources {
		if resource.NodeKey == nodeKey && resource.ResourceType == resourceType {
			return resource.ResourceID
		}
	}
	require.FailNow(t, "Automation resource not found", nodeKey+"/"+resourceType)
	return ""
}

func tableExists(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	return countRows(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table) == 1
}

func tableCountWhere(t *testing.T, db *sql.DB, table, column, value string) int {
	t.Helper()
	return countRows(t, db, "SELECT COUNT(*) FROM "+table+" WHERE "+column+" = ?", value)
}

func countRows(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRow(query, args...).Scan(&count))
	return count
}
