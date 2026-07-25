package service

import (
	"context"
	"database/sql"
	"encoding/json"
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

func TestAutomationSaveRejectsSemanticRepairsAndPreservesSelectedTaskCategories(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*models.AutomationDraftCandidate)
	}{
		{name: "invalid Task category", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[1].Config["category"] = "scheduled"
		}},
		{name: "invalid Schedule category", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[0].Config["category"] = "active"
		}},
		{name: "explicit custom Schedule target", mutate: func(candidate *models.AutomationDraftCandidate) {
			candidate.Nodes[0].Config["target_node_key"] = "different_task"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAutomationSaveHarness(t, "Strict semantic Save "+test.name)
			candidate := customScheduledTaskCandidate("Strict semantic Save", "Review one request.")
			test.mutate(&candidate)

			_, err := h.compiler.Save(context.Background(), AutomationSaveRequest{
				ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
			})
			require.ErrorContains(t, err, "automation graph validation failed")
			require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM automations`))
			require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM tasks`))
			require.Zero(t, countRows(t, h.db, `SELECT COUNT(*) FROM schedules`))
		})
	}

	for _, test := range []struct {
		name      string
		candidate models.AutomationDraftCandidate
		nodeKey   string
		want      models.TaskCategory
	}{
		{name: "Schedule child remains active", candidate: func() models.AutomationDraftCandidate {
			candidate := customScheduledTaskCandidate("Active scheduled follow-up", "Review one request.")
			candidate.Nodes[1].Config["category"] = string(models.CategoryActive)
			return candidate
		}(), nodeKey: "followup", want: models.CategoryActive},
		{name: "Task child remains backlog", candidate: models.AutomationDraftCandidate{
			SchemaVersion: 1, Name: "Backlog task follow-up", Description: "Preserve the selected category",
			AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
			Nodes: []models.AutomationDraftNode{
				{Key: "parent", Name: "Parent", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Run first.", "category": "active", "priority": 2}},
				{Key: "child", Name: "Child", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Wait in backlog.", "category": "backlog", "priority": 2}},
			},
			Edges: []models.AutomationDraftEdge{{Key: "parent_child", From: "parent", To: "child", FromPort: "right", ToPort: "left", Condition: map[string]any{}}},
		}, nodeKey: "child", want: models.CategoryBacklog},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAutomationSaveHarness(t, test.name)
			saved, err := h.compiler.Save(context.Background(), AutomationSaveRequest{
				ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: test.candidate,
			})
			require.NoError(t, err)
			task, err := h.taskRepo.GetByID(context.Background(), automationResourceID(t, saved.Definition, test.nodeKey, "task"))
			require.NoError(t, err)
			require.Equal(t, test.want, task.Category)

			var candidateJSON string
			require.NoError(t, h.db.QueryRow(`SELECT candidate_json FROM automation_graph_metadata WHERE automation_id = ?`, saved.Definition.Automation.ID).Scan(&candidateJSON))
			var stored models.AutomationDraftCandidate
			require.NoError(t, json.Unmarshal([]byte(candidateJSON), &stored))
			require.Equal(t, string(test.want), automationDraftNodeByKey(t, stored, test.nodeKey).Config["category"])
		})
	}
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

func TestAutomationInactiveLifecycleDefersCompiledChildHandoff(t *testing.T) {
	for _, state := range []models.AutomationLifecycleState{models.AutomationPaused, models.AutomationArchived} {
		t.Run(string(state), func(t *testing.T) {
			h := newAutomationSaveHarness(t, "Deferred child handoff "+string(state))
			ctx := context.Background()
			candidate := customScheduledTaskCandidate("Deferred handoff", "Produce parent output.")
			candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{Key: "downstream", Name: "Downstream", Type: models.AutomationNodeAgentTask, Role: "task",
				Config: map[string]any{"prompt": "Continue after the producer.", "category": string(models.CategoryActive), "priority": 2}, Position: &models.AutomationDraftPoint{X: 480, Y: 0}})
			candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{Key: "followup_downstream", From: "followup", To: "downstream", FromPort: "right", ToPort: "left", Condition: map[string]any{}})
			saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)
			parentID := automationResourceID(t, saved.Definition, "followup", "task")
			childID := automationResourceID(t, saved.Definition, "downstream", "task")
			scheduleID := automationResourceID(t, saved.Definition, "schedule", "schedule")
			parent, err := h.taskRepo.GetByID(ctx, parentID)
			require.NoError(t, err)
			child, err := h.taskRepo.GetByID(ctx, childID)
			require.NoError(t, err)
			schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
			require.NoError(t, err)
			due := schedule.NextRun.UTC()
			invocation, _, err := h.automationRepo.ClaimScheduledOccurrence(ctx, *schedule, due, schedule.ComputeNextRun(due))
			require.NoError(t, err)
			require.NoError(t, h.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning))
			execution := models.Execution{TaskID: parent.ID, Status: models.ExecRunning, PromptSent: parent.Prompt}
			require.NoError(t, repository.NewExecutionRepo(h.db).Create(ctx, &execution))
			sourceNode := automationNodeByKey(t, saved.Definition, "followup")
			binding := models.AutomationBinding{AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID,
				InvocationID: invocation.ID, NodeID: sourceNode.ID}
			causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{binding}})
			causalCtx = withAutomationExecution(causalCtx, parent.ID, execution.ID)

			if state == models.AutomationPaused {
				require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, saved.Definition.Automation.ID))
			} else {
				require.NoError(t, h.lifecycle.Archive(ctx, h.project.ID, saved.Definition.Automation.ID))
			}
			config, err := parent.ParseChainConfig()
			require.NoError(t, err)
			taskSvc := NewTaskService(h.taskRepo, repository.NewAttachmentRepo(h.db), nil)
			llmSvc := &LLMService{taskRepo: h.taskRepo, automationRepo: h.automationRepo, taskSvc: taskSvc}
			require.NoError(t, llmSvc.activateCompiledAutomationChild(causalCtx, *parent, "parent result\n[STATUS: SUCCESS]", config))

			deferred, err := h.taskRepo.GetByID(ctx, child.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, deferred.Category)
			require.Equal(t, models.StatusPending, deferred.Status)
			require.Contains(t, deferred.Prompt, "parent result")
			var enteredTransitions int
			require.NoError(t, h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_transitions
				WHERE project_id = ? AND automation_id = ? AND version_id = ? AND from_node_id = ? AND to_node_id = ? AND state = 'entered'`,
				h.project.ID, saved.Definition.Automation.ID, saved.Definition.Version.ID, sourceNode.ID, automationNodeByKey(t, saved.Definition, "downstream").ID).Scan(&enteredTransitions))
			require.Equal(t, 1, enteredTransitions, "inactive handoff must retain its causal transition for Resume")
			followupNode := automationNodeByKey(t, saved.Definition, "downstream")
			var resumableChildren int
			require.NoError(t, h.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM automation_definition_resources resource
				JOIN automation_nodes n ON n.id = resource.node_id AND n.version_id = resource.version_id
				JOIN tasks t ON t.id = resource.resource_id AND t.project_id = resource.project_id
				WHERE resource.project_id = ? AND resource.automation_id = ? AND resource.version_id = ?
					AND resource.resource_type = 'task' AND n.node_key = ? AND t.category = 'backlog'
					AND t.status = 'pending' AND t.parent_task_id = ? AND EXISTS (
						SELECT 1 FROM automation_transitions transition
						WHERE transition.project_id = resource.project_id AND transition.automation_id = resource.automation_id
							AND transition.version_id = resource.version_id AND transition.from_node_id = ?
							AND transition.to_node_id = n.id AND transition.state = 'entered')`,
				h.project.ID, saved.Definition.Automation.ID, saved.Definition.Version.ID, followupNode.NodeKey, parent.ID, sourceNode.ID).Scan(&resumableChildren))
			require.Equal(t, 1, resumableChildren, "deferred child must remain eligible through the exact current graph handoff")
			var savedCandidateJSON string
			require.NoError(t, h.db.QueryRowContext(ctx, `SELECT candidate_json FROM automation_graph_metadata
				WHERE project_id = ? AND automation_id = ? AND version_id = ?`, h.project.ID, saved.Definition.Automation.ID, saved.Definition.Version.ID).Scan(&savedCandidateJSON))
			var savedCandidate models.AutomationDraftCandidate
			require.NoError(t, json.Unmarshal([]byte(savedCandidateJSON), &savedCandidate))
			var savedFollowup models.AutomationDraftNode
			for _, node := range savedCandidate.Nodes {
				if node.Key == "downstream" {
					savedFollowup = node
				}
			}
			require.Equal(t, models.AutomationNodeAgentTask, savedFollowup.Type)
			require.Equal(t, "task", savedFollowup.Role)
			require.Equal(t, string(models.CategoryActive), savedFollowup.Config["category"])

			recorder := &recordingAutomationTaskService{TaskService: taskSvc}
			if state == models.AutomationPaused {
				resume := NewAutomationLifecycleService(h.automationRepo, h.scheduleRepo, recorder)
				require.NoError(t, resume.Resume(ctx, h.project.ID, saved.Definition.Automation.ID))
				require.Len(t, recorder.submitted, 1, "Resume must admit the deferred completed-parent handoff exactly once")
				require.Equal(t, child.ID, recorder.submitted[0].ID)
			} else {
				require.ErrorContains(t, h.lifecycle.Resume(ctx, h.project.ID, saved.Definition.Automation.ID), "archived automation cannot be resumed")
				require.Empty(t, recorder.submitted)
			}
		})
	}
}

func TestAutomationPauseOrArchiveAfterChildActivationPreventsSubmission(t *testing.T) {
	for _, state := range []models.AutomationLifecycleState{models.AutomationPaused, models.AutomationArchived} {
		t.Run(string(state), func(t *testing.T) {
			h := newAutomationSaveHarness(t, "Post-activation handoff "+string(state))
			ctx := context.Background()
			candidate := customScheduledTaskCandidate("Post-activation handoff", "Produce parent output.")
			candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{Key: "downstream", Name: "Downstream", Type: models.AutomationNodeAgentTask, Role: "task",
				Config: map[string]any{"prompt": "Continue after the producer.", "category": string(models.CategoryActive), "priority": 2}, Position: &models.AutomationDraftPoint{X: 480, Y: 0}})
			candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{Key: "followup_downstream", From: "followup", To: "downstream", FromPort: "right", ToPort: "left", Condition: map[string]any{}})
			saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)
			parent, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, "followup", "task"))
			require.NoError(t, err)
			childID := automationResourceID(t, saved.Definition, "downstream", "task")
			schedule, err := h.scheduleRepo.GetByID(ctx, automationResourceID(t, saved.Definition, "schedule", "schedule"))
			require.NoError(t, err)
			due := schedule.NextRun.UTC()
			invocation, _, err := h.automationRepo.ClaimScheduledOccurrence(ctx, *schedule, due, schedule.ComputeNextRun(due))
			require.NoError(t, err)
			require.NoError(t, h.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning))
			execution := models.Execution{TaskID: parent.ID, Status: models.ExecRunning, PromptSent: parent.Prompt}
			require.NoError(t, repository.NewExecutionRepo(h.db).Create(ctx, &execution))
			sourceNode := automationNodeByKey(t, saved.Definition, "followup")
			binding := models.AutomationBinding{AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, InvocationID: invocation.ID, NodeID: sourceNode.ID}
			causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{binding}})
			causalCtx = withAutomationExecution(causalCtx, parent.ID, execution.ID)
			config, err := parent.ParseChainConfig()
			require.NoError(t, err)

			worker := NewWorkerService(nil, 1, repository.NewProjectRepo(h.db))
			worker.SetTaskRepo(h.taskRepo)
			taskSvc := NewTaskService(h.taskRepo, repository.NewAttachmentRepo(h.db), worker)
			llmSvc := &LLMService{taskRepo: h.taskRepo, automationRepo: h.automationRepo, taskSvc: taskSvc}
			activated := make(chan struct{})
			release := make(chan struct{})
			llmSvc.automationHandoffBeforeFinalAdmission = func() {
				close(activated)
				<-release
			}
			done := make(chan error, 1)
			go func() {
				done <- llmSvc.activateCompiledAutomationChild(causalCtx, *parent, "parent result\n[STATUS: SUCCESS]", config)
			}()
			<-activated
			if state == models.AutomationPaused {
				require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, saved.Definition.Automation.ID))
			} else {
				require.NoError(t, h.lifecycle.Archive(ctx, h.project.ID, saved.Definition.Automation.ID))
			}
			close(release)
			require.NoError(t, <-done)
			select {
			case submitted := <-worker.Submitted():
				t.Fatalf("inactive Automation submitted downstream task %s", submitted.ID)
			default:
			}
			child, err := h.taskRepo.GetByID(ctx, childID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, child.Category)
			require.Equal(t, models.StatusPending, child.Status)

			recorder := &recordingAutomationTaskService{TaskService: taskSvc}
			resume := NewAutomationLifecycleService(h.automationRepo, h.scheduleRepo, recorder)
			if state == models.AutomationPaused {
				require.NoError(t, resume.Resume(ctx, h.project.ID, saved.Definition.Automation.ID))
				require.Len(t, recorder.submitted, 1)
				require.Equal(t, childID, recorder.submitted[0].ID)
			} else {
				require.ErrorContains(t, resume.Resume(ctx, h.project.ID, saved.Definition.Automation.ID), "archived automation cannot be resumed")
				require.Empty(t, recorder.submitted)
			}
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
