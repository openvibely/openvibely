package service

import (
	"context"

	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type automationSaveHarness struct {
	stores         *teststores.Env
	project        models.Project
	automationRepo persistence.AutomationStore
	taskRepo       persistence.TaskStore
	scheduleRepo   persistence.ScheduleStore
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

func newAutomationSaveStoreHarness(t *testing.T, name string) automationSaveHarness {
	t.Helper()
	return buildAutomationSaveHarness(t, teststores.New(t), name)
}
func buildAutomationSaveHarness(t *testing.T, stores *teststores.Env, name string) automationSaveHarness {
	t.Helper()
	project := automationTestProject(t, stores.Projects, name)
	automationRepo, taskRepo, scheduleRepo := stores.Automations, stores.Tasks, stores.Schedules
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	planner := NewAutomationSaveValidator(registry, drafts)
	taskSvc := NewTaskService(taskRepo, stores.Attachments, nil)
	compiler := NewAutomationCompiler(automationRepo, taskSvc, taskRepo, scheduleRepo, planner)
	return automationSaveHarness{stores: stores, project: project, automationRepo: automationRepo, taskRepo: taskRepo,
		scheduleRepo: scheduleRepo, drafts: drafts, compiler: compiler,
		lifecycle: NewAutomationLifecycleService(automationRepo, scheduleRepo, taskSvc)}
}

func TestAutomationSaveDefaultModelSentinelUsesRuntimeDefaultLookup(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Default model execution")
	ctx := context.Background()

	candidate := customTaskOnlyCandidate("Default model execution", "Run with the runtime default model.", models.CategoryActive)
	candidate.Nodes[0].Config["model_config_id"] = automationDefaultModelConfigID
	recorder := &recordingAutomationTaskService{TaskService: NewTaskService(h.taskRepo, h.stores.Attachments, nil)}
	compiler := NewAutomationCompiler(h.automationRepo, recorder, h.taskRepo, h.scheduleRepo, NewAutomationSaveValidator(NewAutomationAdapterRegistry(), h.drafts))

	saved, err := compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	taskID := automationResourceID(t, saved.Definition, "root", "task")
	stored, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Nil(t, stored.AgentID)
	require.Len(t, recorder.submitted, 1)
	require.Nil(t, recorder.submitted[0].AgentID)

	claim, claimed, err := h.taskRepo.ClaimTaskForDispatch(ctx, taskID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Nil(t, claim.Task.AgentID)
}

func TestAutomationSaveStoresSelectedModelForTaskExecution(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Selected model execution")
	ctx := context.Background()
	llmConfigRepo := h.stores.LLMConfigs
	selectedModel := models.LLMConfig{Name: "Selected automation model", Provider: models.ProviderTest, Model: "selected-model"}
	require.NoError(t, llmConfigRepo.Create(ctx, &selectedModel))
	capabilities := NewAutomationCapabilitySnapshotBuilder(h.stores.Projects, nil, nil, nil)
	capabilities.SetLLMConfigRepository(llmConfigRepo)
	h.drafts.SetCapabilitySnapshotBuilder(capabilities)

	candidate := customTaskOnlyCandidate("Selected model execution", "Run with the selected model.", models.CategoryActive)
	candidate.Nodes[0].Config["model_config_id"] = selectedModel.ID
	recorder := &recordingAutomationTaskService{TaskService: NewTaskService(h.taskRepo, h.stores.Attachments, nil)}
	compiler := NewAutomationCompiler(h.automationRepo, recorder, h.taskRepo, h.scheduleRepo, NewAutomationSaveValidator(NewAutomationAdapterRegistry(), h.drafts))

	saved, err := compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	taskID := automationResourceID(t, saved.Definition, "root", "task")
	stored, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.NotNil(t, stored.AgentID)
	require.Equal(t, selectedModel.ID, *stored.AgentID)
	require.Len(t, recorder.submitted, 1)
	require.NotNil(t, recorder.submitted[0].AgentID)
	require.Equal(t, selectedModel.ID, *recorder.submitted[0].AgentID)

	claim, claimed, err := h.taskRepo.ClaimTaskForDispatch(ctx, taskID)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NotNil(t, claim.Task.AgentID)
	require.Equal(t, selectedModel.ID, *claim.Task.AgentID)
}

func seedExistingVisionDriverAutomation(t *testing.T, h automationSaveHarness) (*models.AutomationDefinition, models.AutomationDraftCandidate) {
	t.Helper()
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	driver := automationDraftNodeByKey(t, candidate, "vision_driver")
	prompt, category, priority := automationNodeTaskConfiguration(candidate, driver)
	task := &models.Task{ProjectID: h.project.ID, Title: "Existing Vision Driver", Prompt: prompt, Category: category,
		Priority: priority, Status: models.StatusPending, CreatedVia: "test-existing-vision-driver"}
	require.NoError(t, h.taskRepo.Create(ctx, task))
	schedule := &models.Schedule{TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatDaily,
		RepeatInterval: 1, Enabled: true}
	require.NoError(t, h.scheduleRepo.Create(ctx, schedule))

	nodes := make([]models.AutomationNodeSpec, 0, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		config, marshalErr := json.Marshal(node.Config)
		require.NoError(t, marshalErr)
		position := models.AutomationDraftPoint{}
		if node.Position != nil {
			position = *node.Position
		}
		nodes = append(nodes, models.AutomationNodeSpec{Key: node.Key, Name: node.Name, Type: node.Type, Role: node.Role,
			ConfigJSON: string(config), PositionX: position.X, PositionY: position.Y})
	}
	edges := make([]models.AutomationEdgeSpec, 0, len(candidate.Edges))
	for i, edge := range candidate.Edges {
		condition, marshalErr := json.Marshal(edge.Condition)
		require.NoError(t, marshalErr)
		edges = append(edges, models.AutomationEdgeSpec{Key: edge.Key, SourceNodeKey: edge.From, TargetNodeKey: edge.To,
			Label: edge.Label, ConditionJSON: string(condition), DisplayOrder: i})
	}
	definition, reused, err := h.automationRepo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: h.project.ID, StableKey: "vision-driver/existing", Name: candidate.Name, Description: candidate.Description,
		AutomationType: candidate.AutomationType, AdapterKey: candidate.AdapterKey, CreatedVia: "test",
		Nodes: nodes, Edges: edges, Resources: []models.AutomationResourceBinding{
			{NodeKey: "vision_driver", ResourceType: "task", ResourceID: task.ID, Relation: "owned"},
			{NodeKey: "vision_driver", ResourceType: "schedule", ResourceID: schedule.ID, Relation: "owned"},
		},
	})
	require.NoError(t, err)
	require.False(t, reused)
	return definition, candidate
}

func TestGitHubSDLCReducedGraphRequiresSetupOnlyForRetainedGitHubRuntimeNodes(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Reduced GitHub graph capability requirements")
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)

	for _, test := range []struct {
		key    string
		prompt string
	}{
		{key: "vision_suggestions", prompt: "Open an issue for each actionable finding."},
	} {
		customized, err := h.drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
		require.NoError(t, err)
		for _, node := range append([]models.AutomationDraftNode(nil), customized.Nodes...) {
			if node.Key != test.key {
				customized = automationCandidateWithoutNode(customized, node.Key)
			}
		}
		customized.Nodes[0].Config["prompt"] = test.prompt
		customizedResult, err := h.compiler.Save(context.Background(), AutomationSaveRequest{
			ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: customized,
		})
		require.NoError(t, err, test.key)
		require.Len(t, customizedResult.Definition.Nodes, 1)
		require.Equal(t, test.key, customizedResult.Definition.Nodes[0].NodeKey)
	}

	_, err = h.compiler.Save(context.Background(), AutomationSaveRequest{
		ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate,
	})
	require.ErrorContains(t, err, "Configure the selected GitHub authentication mode")

	terminalOnly := candidate
	for _, node := range append([]models.AutomationDraftNode(nil), candidate.Nodes...) {
		if node.Key != "completed" {
			terminalOnly = automationCandidateWithoutNode(terminalOnly, node.Key)
		}
	}
	result, err := h.compiler.Save(context.Background(), AutomationSaveRequest{
		ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: terminalOnly,
	})
	require.NoError(t, err)
	require.Len(t, result.Definition.Nodes, 1)
	require.Equal(t, "completed", result.Definition.Nodes[0].NodeKey)
}

func TestMaintainedSDLCSaveRemovesOptionalVisionProducerAndOwnedResources(t *testing.T) {
	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
		t.Run(adapterKey, func(t *testing.T) {
			h := newAutomationSaveStoreHarness(t, "Optional producer "+adapterKey)
			ctx := context.Background()
			if adapterKey == AutomationAdapterGitHubSDLC {
				projectRepo := h.stores.Projects
				h.project.RepoURL = "https://github.com/example/automation.git"
				require.NoError(t, projectRepo.Update(ctx, &h.project))
				settingsRepo := h.stores.Settings
				require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
				require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "test-token"))
				githubAuthRepo := h.stores.GitHubAuth
				require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
				h.compiler.validator.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)
			}
			candidate, err := h.drafts.TemplateCandidate(adapterKey)
			require.NoError(t, err)
			fullCandidate := candidate

			initial, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err, "the unmodified maintained template must still save")
			visionTaskID := automationResourceID(t, initial.Definition, "vision_suggestions", "task")
			visionScheduleID := automationResourceID(t, initial.Definition, "vision_suggestions", "schedule")

			candidate = automationCandidateWithoutNode(candidate, "vision_suggestions")
			replacement, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: initial.Definition.Automation.ID,
				Source: "manual", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)
			for _, node := range replacement.Definition.Nodes {
				require.NotEqual(t, "vision_suggestions", node.NodeKey)
			}
			for _, resource := range replacement.Definition.Resources {
				require.NotEqual(t, "vision_suggestions", resource.NodeKey)
			}
			retainedTask, err := h.taskRepo.GetByID(ctx, visionTaskID)
			require.NoError(t, err)
			require.NotNil(t, retainedTask, "replacement preserves the backing domain Task")
			removedSchedule, err := h.scheduleRepo.GetByID(ctx, visionScheduleID)
			require.NoError(t, err)
			require.Nil(t, removedSchedule)
			bugNode := automationNodeByKey(t, replacement.Definition, "bug_finder")
			actionKey := "notification"
			if adapterKey == AutomationAdapterGitHubSDLC {
				actionKey = "issue"
			}
			actionNode := automationNodeByKey(t, replacement.Definition, actionKey)
			persisted, err := h.automationRepo.GetDefinitionVersion(ctx, h.project.ID, replacement.Definition.Automation.ID, replacement.Definition.Version.ID)
			require.NoError(t, err)
			require.NotNil(t, persisted)
			edgeCount := 0
			for _, edge := range persisted.Edges {
				if edge.SourceNodeID == bugNode.ID && edge.TargetNodeID == actionNode.ID {
					edgeCount++
				}
			}
			require.Equal(t, 1, edgeCount, "deleting Vision Suggestions must preserve Bug Finder's shared action connection")

			restored, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: initial.Definition.Automation.ID,
				Source: "manual", CreatedVia: "web", Candidate: fullCandidate})
			require.NoError(t, err, "restoring a maintained node must reuse its preserved backing Task")
			require.Equal(t, visionTaskID, automationResourceID(t, restored.Definition, "vision_suggestions", "task"))
			require.NotEqual(t, visionScheduleID, automationResourceID(t, restored.Definition, "vision_suggestions", "schedule"))
			originCount, err := h.stores.Fixtures.CountTasksByCreationOrigin(ctx, "", persistence.AutomationCompilerTaskCreatedVia(initial.Definition.Automation.ID, "vision_suggestions"))
			require.NoError(t, err)
			require.Equal(t, 1, originCount)

			bugOnly := fullCandidate
			for _, node := range append([]models.AutomationDraftNode(nil), bugOnly.Nodes...) {
				if node.Key != "bug_finder" {
					bugOnly = automationCandidateWithoutNode(bugOnly, node.Key)
				}
			}
			reduced, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: initial.Definition.Automation.ID,
				Source: "manual", CreatedVia: "web", Candidate: bugOnly})
			require.NoError(t, err, "a template may be reduced to any valid remaining graph")
			require.Len(t, reduced.Definition.Nodes, 1)
			require.Equal(t, "bug_finder", reduced.Definition.Nodes[0].NodeKey)
			for _, resource := range reduced.Definition.Resources {
				require.Equal(t, "bug_finder", resource.NodeKey)
			}

			require.NoError(t, h.stores.Fixtures.SetTaskCreationOrigin(ctx, visionTaskID, persistence.AutomationCompilerTaskCreatedVia(persistence.NewID(), "vision_suggestions")))
			_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: initial.Definition.Automation.ID,
				Source: "manual", CreatedVia: "web", Candidate: fullCandidate})
			require.ErrorContains(t, err, "task with this name already exists", "restore must not adopt a task whose durable origin belongs to another Automation")
			current, err := h.automationRepo.GetDefinition(ctx, h.project.ID, initial.Definition.Automation.ID)
			require.NoError(t, err)
			require.Len(t, current.Nodes, 1, "failed restore must leave the reduced graph current")
			require.Equal(t, "bug_finder", current.Nodes[0].NodeKey)
		})
	}
}

func TestAutomationSaveRejectsNewVisionDriverAndAllowsExistingEdit(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Vision Driver creation boundary")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)

	for _, automationID := range []string{"", persistence.NewID()} {
		_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: automationID,
			Source: "template", CreatedVia: "web", Candidate: candidate})
		require.ErrorContains(t, err, "Vision Driver cannot be created")
	}
	require.Zero(t, automationStateForTest(t, h.stores).Automations)
	require.Zero(t, automationStateForTest(t, h.stores).Tasks)
	require.Zero(t, automationStateForTest(t, h.stores).Schedules)

	existing, editable := seedExistingVisionDriverAutomation(t, h)
	editable.Description = "Edited existing Vision Driver"
	editable.Nodes[0].Config["prompt"] = "Use the edited existing prompt."
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: existing.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: editable})
	require.NoError(t, err)
	require.Equal(t, existing.Automation.ID, saved.Definition.Automation.ID)
	require.Equal(t, "Edited existing Vision Driver", saved.Definition.Automation.Description)
	require.Equal(t, 1, automationStateForTest(t, h.stores).Automations)
}

func TestAutomationSaveRejectsInvalidMaintainedGitHubActionSettingsAtomically(t *testing.T) {
	for _, test := range []struct {
		name        string
		role        string
		field       string
		value       any
		removeField bool
		wantCode    string
	}{
		{name: "implementation prompt missing", role: "implementation", field: "prompt", removeField: true, wantCode: "missing_prompt"},
		{name: "implementation category missing", role: "implementation", field: "category", removeField: true, wantCode: "category"},
		{name: "implementation category backlog", role: "implementation", field: "category", value: string(models.CategoryBacklog), wantCode: "category"},
		{name: "implementation category scheduled", role: "implementation", field: "category", value: string(models.CategoryScheduled), wantCode: "category"},
		{name: "implementation priority missing", role: "implementation", field: "priority", removeField: true, wantCode: "priority"},
		{name: "schedule clear context wrong type", role: "github_inbox", field: "clear_context_on_start", value: "false", wantCode: "clear_context_on_start"},
		{name: "issue instructions wrong type", role: "create_github_issue", field: "instructions", value: true, wantCode: "action_instructions"},
		{name: "issue instructions exceed limit", role: "create_github_issue", field: "instructions", value: strings.Repeat("x", 2001), wantCode: "action_instructions"},
		{name: "labels wrong type", role: "create_github_issue", field: "labels", value: "bug", wantCode: "github_issue_labels"},
		{name: "labels exceed limit", role: "create_github_issue", field: "labels", value: []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11"}, wantCode: "github_issue_labels"},
		{name: "label exceeds limit", role: "create_github_issue", field: "labels", value: []string{strings.Repeat("x", 101)}, wantCode: "github_issue_labels"},
		{name: "label uses forbidden prefix", role: "create_github_issue", field: "labels", value: []string{"OpenVibely:internal"}, wantCode: "github_issue_labels"},
		{name: "pull request instructions wrong type", role: "open_pull_request", field: "instructions", value: false, wantCode: "action_instructions"},
		{name: "pull request base wrong type", role: "open_pull_request", field: "base", value: 42, wantCode: "pull_request_base"},
		{name: "pull request base exceeds limit", role: "open_pull_request", field: "base", value: strings.Repeat("x", 201), wantCode: "pull_request_base"},
		{name: "pull request draft wrong type", role: "open_pull_request", field: "draft", value: "false", wantCode: "pull_request_draft"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAutomationSaveStoreHarness(t, "Invalid maintained GitHub settings "+test.name)
			candidate, err := h.drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
			require.NoError(t, err)
			for i := range candidate.Nodes {
				if candidate.Nodes[i].Role == test.role {
					if test.removeField {
						delete(candidate.Nodes[i].Config, test.field)
					} else {
						candidate.Nodes[i].Config[test.field] = test.value
					}
				}
			}

			require.Contains(t, issueCodes(h.drafts.ValidateCandidate(candidate)), test.wantCode)
			_, err = h.compiler.Save(context.Background(), AutomationSaveRequest{
				ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate,
			})
			require.ErrorContains(t, err, "automation graph validation failed")
			require.Zero(t, automationStateForTest(t, h.stores).Automations)
			require.Zero(t, automationStateForTest(t, h.stores).Tasks)
			require.Zero(t, automationStateForTest(t, h.stores).Schedules)
		})
	}
}

func TestAutomationSaveRejectsSemanticRepairsAndPreservesSelectedTaskCategories(t *testing.T) {
	for _, test := range []struct {
		name      string
		candidate func(*automationSaveHarness) models.AutomationDraftCandidate
		mutate    func(*models.AutomationDraftCandidate)
	}{
		{name: "invalid automation type", candidate: func(_ *automationSaveHarness) models.AutomationDraftCandidate {
			candidate := customScheduledTaskCandidate("Strict semantic Save", "Review one request.")
			candidate.AutomationType = "github_sdlc"
			return candidate
		}},
		{name: "empty maintained node name", candidate: func(h *automationSaveHarness) models.AutomationDraftCandidate {
			candidate, err := h.drafts.TemplateCandidate(AutomationAdapterVisionDriver)
			require.NoError(t, err)
			candidate.Nodes[0].Name = ""
			return candidate
		}},
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
			h := newAutomationSaveStoreHarness(t, "Strict semantic Save "+test.name)
			candidate := customScheduledTaskCandidate("Strict semantic Save", "Review one request.")
			if test.candidate != nil {
				candidate = test.candidate(&h)
			}
			if test.mutate != nil {
				test.mutate(&candidate)
			}

			_, err := h.compiler.Save(context.Background(), AutomationSaveRequest{
				ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
			})
			require.ErrorContains(t, err, "automation graph validation failed")
			require.Zero(t, automationStateForTest(t, h.stores).Automations)
			require.Zero(t, automationStateForTest(t, h.stores).Tasks)
			require.Zero(t, automationStateForTest(t, h.stores).Schedules)
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
			h := newAutomationSaveStoreHarness(t, test.name)
			saved, err := h.compiler.Save(context.Background(), AutomationSaveRequest{
				ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: test.candidate,
			})
			require.NoError(t, err)
			task, err := h.taskRepo.GetByID(context.Background(), automationResourceID(t, saved.Definition, test.nodeKey, "task"))
			require.NoError(t, err)
			require.Equal(t, test.want, task.Category)

			metadata, err := h.automationRepo.GetAutomationGraphMetadata(t.Context(), h.project.ID, saved.Definition.Automation.ID, saved.Definition.Version.ID)
			require.NoError(t, err)
			require.NotNil(t, metadata)
			candidateJSON := metadata.CandidateJSON
			var stored models.AutomationDraftCandidate
			require.NoError(t, json.Unmarshal([]byte(candidateJSON), &stored))
			require.Equal(t, string(test.want), automationDraftNodeByKey(t, stored, test.nodeKey).Config["category"])
		})
	}
}

func TestAutomationQueuedRootClaimsReplacementDispatchStateAtomically(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Atomic queued root dispatch")
	ctx := context.Background()
	agentRepo := h.stores.Agents
	staleAgent := models.Agent{Name: "Stale dispatch agent", Key: "stale_dispatch_agent", Scope: models.AgentScopeProject,
		ProjectID: h.project.ID, Enabled: true, SelectableAsPrimary: true, SystemPrompt: "stale agent must not execute"}
	replacementAgent := models.Agent{Name: "Replacement dispatch agent", Key: "replacement_dispatch_agent", Scope: models.AgentScopeProject,
		ProjectID: h.project.ID, Enabled: true, SelectableAsPrimary: true, SystemPrompt: "replacement agent executes"}
	require.NoError(t, agentRepo.Create(ctx, &staleAgent))
	require.NoError(t, agentRepo.Create(ctx, &replacementAgent))
	h.drafts.SetCapabilitySnapshotBuilder(NewAutomationCapabilitySnapshotBuilder(
		h.stores.Projects, agentRepo, h.taskRepo, h.stores.Settings))
	h.compiler.SetAgentRepository(agentRepo)
	h.compiler.validator.SetAgentRepository(agentRepo)
	initial := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Queued root", Description: "Initial graph",
		AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{{Key: "root", Name: "Root task", Type: models.AutomationNodeAgentTask, Role: "task",
			Config: map[string]any{"prompt": "stale prompt must not execute", "category": "active", "priority": 2, "agent_ref": staleAgent.Key}}}}
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: initial})
	require.NoError(t, err)
	taskID := automationResourceID(t, first.Definition, "root", "task")
	queuedTask, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)

	llmConfigRepo := h.stores.LLMConfigs
	agent := &models.LLMConfig{Name: "Atomic dispatch agent", Provider: models.ProviderTest, Model: "test-model",
		MaxTokens: 4096, AuthMethod: models.AuthMethodAPIKey, IsDefault: true}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))
	execRepo := h.stores.Executions
	llmSvc := NewLLMService(llmConfigRepo, execRepo, h.taskRepo, h.stores.Projects, h.scheduleRepo, h.stores.Attachments)
	llmSvc.SetAgentRepo(agentRepo)
	caller := testutil.NewMockLLMCaller()
	called := make(chan models.AutomationContext, 1)
	caller.OnCall = func(callCtx context.Context, _ testutil.MockLLMCall) {
		automationContext, _ := AutomationContextFromContext(callCtx)
		called <- automationContext
	}
	llmSvc.SetLLMCaller(caller)
	worker := NewWorkerService(llmSvc, 1, h.stores.Projects)
	worker.SetTaskRepo(h.taskRepo)
	worker.SetLLMConfigRepo(llmConfigRepo)
	worker.SetAutomationRepo(h.automationRepo)
	beforeClaim := make(chan struct{})
	releaseClaim := make(chan struct{})
	worker.beforeOrdinaryTaskClaim = func(models.Task) {
		close(beforeClaim)
		<-releaseClaim
	}
	worker.Start(ctx)
	defer worker.Stop()

	worker.Submit(*queuedTask)
	select {
	case <-beforeClaim:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not reach the post-reload claim barrier")
	}

	replacement := initial
	replacement.Description = "Replacement graph"
	replacement.Nodes = append(append([]models.AutomationDraftNode(nil), initial.Nodes...),
		models.AutomationDraftNode{Key: "done", Name: "Done", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}})
	replacement.Nodes[0].Config = map[string]any{"prompt": "replacement prompt executes", "category": "active", "priority": 2,
		"agent_ref": replacementAgent.Key}
	replacement.Edges = []models.AutomationDraftEdge{{Key: "root_done", From: "root", To: "done", FromPort: "right", ToPort: "left", Condition: map[string]any{}}}
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: replacement})
	require.NoError(t, err)
	close(releaseClaim)

	select {
	case dispatchContext := <-called:
		require.Len(t, dispatchContext.Bindings, 1)
		require.Equal(t, saved.Definition.Version.ID, dispatchContext.Bindings[0].VersionID)
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not execute the claimed queued root")
	}
	require.Contains(t, caller.LastCall().Prompt, "replacement prompt executes")
	require.NotContains(t, caller.LastCall().Prompt, "stale prompt must not execute")
	require.NotNil(t, caller.LastAgentRequest().AgentDefinition)
	require.Equal(t, replacementAgent.ID, caller.LastAgentRequest().AgentDefinition.ID)
	require.NotEqual(t, staleAgent.ID, caller.LastAgentRequest().AgentDefinition.ID)
}

func customNativeMailboxCandidate(name string) models.AutomationDraftCandidate {
	return models.AutomationDraftCandidate{SchemaVersion: 1, Name: name, AutomationType: "custom", AdapterKey: AutomationAdapterCustom, Nodes: []models.AutomationDraftNode{
		{Key: "custom_producer", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Review one focused area and prepare an actionable notification.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "custom_notification", Name: "Create notification", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "suggestion", "instructions": "Create one reviewable suggestion."}},
		{Key: "custom_approval", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
		{Key: "custom_approved_inbox", Name: "Approved inbox", Type: models.AutomationNodeTrigger, Role: "native_inbox", Config: map[string]any{"prompt": NativeSDLCNotificationInboxPrompt, "category": "scheduled", "priority": 2, "run_at": "09:15", "repeat_type": "hours", "repeat_interval": 1, "enabled": true}},
		{Key: "custom_implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "implementation", Config: map[string]any{}},
		{Key: "custom_completed", Name: "Completed", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		{Key: "custom_rejected", Name: "Rejected", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}, Edges: []models.AutomationDraftEdge{
		{Key: "custom_producer_notification", From: "custom_producer", To: "custom_notification", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "custom_notification_approval", From: "custom_notification", To: "custom_approval", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "custom_approval_inbox", From: "custom_approval", To: "custom_approved_inbox", FromPort: "right", ToPort: "left", Condition: map[string]any{"state": "approved"}},
		{Key: "custom_approval_rejected", From: "custom_approval", To: "custom_rejected", FromPort: "right", ToPort: "left", Condition: map[string]any{"state": "rejected"}},
		{Key: "custom_inbox_implementation", From: "custom_approved_inbox", To: "custom_implementation", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "custom_implementation_completed", From: "custom_implementation", To: "custom_completed", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}}
}

func TestAutomationSavePreviewExcludesCustomNativeImplementationPlaceholderTask(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Custom Native mailbox preview")
	plan, _, err := h.compiler.PreviewSave(context.Background(), h.project.ID, customNativeMailboxCandidate("Custom Native mailbox preview"))
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	require.ElementsMatch(t, []models.AutomationSaveEffect{
		{Operation: "create", ResourceType: "task", Name: "Daily review"},
		{Operation: "create", ResourceType: "schedule", Name: "Daily review"},
		{Operation: "create", ResourceType: "task", Name: "Approved inbox"},
		{Operation: "create", ResourceType: "schedule", Name: "Approved inbox"},
	}, plan.Effects)
	for _, effect := range plan.Effects {
		require.False(t, effect.Name == "Implementation" && effect.ResourceType == "task", "Native implementation is projection-only")
	}
}

func TestAutomationCompiledCustomNativeInboxScopeIsLimitedToItsApprovalBranch(t *testing.T) {
	candidate := customNativeMailboxCandidate("Two Native mailbox branches")
	candidate.Nodes = append(candidate.Nodes,
		models.AutomationDraftNode{Key: "other_producer", Name: "Weekly security review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Find security hardening proposals only."}},
		models.AutomationDraftNode{Key: "other_notification", Name: "Create security notification", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{}},
		models.AutomationDraftNode{Key: "other_approval", Name: "Security approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{}},
		models.AutomationDraftNode{Key: "other_inbox", Name: "Security approved inbox", Type: models.AutomationNodeTrigger, Role: "native_inbox", Config: map[string]any{"prompt": "Process approved security proposals."}},
	)
	candidate.Edges = append(candidate.Edges,
		models.AutomationDraftEdge{Key: "other_producer_notification", From: "other_producer", To: "other_notification"},
		models.AutomationDraftEdge{Key: "other_notification_approval", From: "other_notification", To: "other_approval"},
		models.AutomationDraftEdge{Key: "other_approval_inbox", From: "other_approval", To: "other_inbox"},
	)

	prompt := automationCompiledTaskPrompt(candidate, candidate.Nodes[3])
	require.Contains(t, prompt, `Producer: "Daily review"`)
	require.Contains(t, prompt, `Purpose: "Review one focused area and prepare an actionable notification."`)
	require.NotContains(t, prompt, "Weekly security review")
	require.NotContains(t, prompt, "Find security hardening proposals only.")
}

func TestAutomationSaveHardensCustomNativeInboxPersistedProjectPrompt(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Custom Native inbox prompt")
	candidate := customNativeMailboxCandidate("Custom Native inbox prompt")
	candidate.Nodes[3].Config["prompt"] = "Process approved notifications safely."

	saved, err := h.compiler.Save(context.Background(), AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	inboxTask, err := h.taskRepo.GetByID(context.Background(), automationResourceID(t, saved.Definition, "custom_approved_inbox", "task"))
	require.NoError(t, err)
	require.Contains(t, inboxTask.Prompt, "Only process approved notifications owned by this same Automation in the current project")
	require.Contains(t, inboxTask.Prompt, `Producer: "Daily review"`)
	require.Contains(t, inboxTask.Prompt, `Purpose: "Review one focused area and prepare an actionable notification."`)
	require.Contains(t, inboxTask.Prompt, "source context rather than a graph-branch eligibility limit")
	require.Contains(t, inboxTask.Prompt, "durable project + Automation + notification ownership for this current Native inbox execution")
	require.NotContains(t, inboxTask.Prompt, "exact trusted Automation, graph-version, and inbox-branch provenance")
	require.Contains(t, inboxTask.Prompt, "Call list_alerts without project_id, using decision_state=approved, processing_state=unclaimed, implementation_task_linked=false")
	require.Contains(t, inboxTask.Prompt, "Before calling claim_alert, collect every eligible result from all pages")
	require.Contains(t, inboxTask.Prompt, "Do not claim, link, or process any notification while paginating")
	require.Contains(t, inboxTask.Prompt, "Only after the complete paginated snapshot is collected")
	require.Contains(t, inboxTask.Prompt, "Do not pass read, type, or source")
	require.Contains(t, inboxTask.Prompt, "failed notifications require a separate recovery query")
	require.Contains(t, inboxTask.Prompt, "Never search for or reuse a project ID")
	require.Contains(t, inboxTask.Prompt, "call get_alert for each collected notification and inspect the full body and metadata before claiming it")
	require.Contains(t, inboxTask.Prompt, "Call claim_alert for each notification you can process")
	require.Contains(t, inboxTask.Prompt, "Only continue when the claim succeeds")
	require.Contains(t, inboxTask.Prompt, "call create_alert_implementation_task with a focused Backlog Task title and prompt")
	require.Contains(t, inboxTask.Prompt, "prompt must include the notification ID, reviewed context, and acceptance criteria")
	require.Contains(t, inboxTask.Prompt, "The created task is the implementation task")
	require.Contains(t, inboxTask.Prompt, "directly instruct it to implement the reviewed change")
	require.Contains(t, inboxTask.Prompt, "must not create or look for another implementation task")
	require.Contains(t, inboxTask.Prompt, "must not run notification intake or call get_alert")
	require.Contains(t, inboxTask.Prompt, "call execute_tasks with the exact returned implementation_task_id")
	require.Contains(t, inboxTask.Prompt, "Only call complete_alert_processing after execute_tasks succeeds")
	require.Contains(t, inboxTask.Prompt, "If creation, linkage, or Task execution fails, call fail_alert_processing")
	require.Contains(t, inboxTask.Prompt, "Call release_alert_claim only when no task was linked")
	require.NotContains(t, inboxTask.Prompt, "implementation approval")
}

func TestAutomationSaveCreatesCustomNativeMailboxWithoutImplementationPlaceholderTask(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Custom Native mailbox")
	saved, err := h.compiler.Save(context.Background(), AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: customNativeMailboxCandidate("Custom Native mailbox")})
	require.NoError(t, err)
	require.NotEmpty(t, automationResourceID(t, saved.Definition, "custom_producer", "task"))
	require.NotEmpty(t, automationResourceID(t, saved.Definition, "custom_approved_inbox", "task"))
	for _, resource := range saved.Definition.Resources {
		require.False(t, resource.NodeKey == "custom_implementation" && resource.ResourceType == "task", "Native implementation is projection-only")
	}
	persisted, err := h.automationRepo.GetDefinition(context.Background(), h.project.ID, saved.Definition.Automation.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	var tasks, schedules int
	for _, resource := range persisted.Resources {
		switch resource.ResourceType {
		case "task":
			tasks++
		case "schedule":
			schedules++
		}
	}
	require.Equal(t, 2, tasks)
	require.Equal(t, 2, schedules)
}

func TestAutomationSaveCreatesCurrentGraphTaskAndScheduleAtomically(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Atomic custom Save")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Daily review", "Review one request.")

	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Equal(t, models.AutomationActive, saved.Definition.Automation.LifecycleState)
	require.Len(t, saved.Definition.Nodes, 2)
	require.Len(t, saved.Definition.Edges, 1)
	versions, err := h.stores.Fixtures.AutomationVersions(ctx, saved.Definition.Automation.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, saved.Definition.Version.ID, versions[0].ID)
	require.NotEqual(t, "draft", string(versions[0].State))
	taskID := automationResourceID(t, saved.Definition, "schedule", "task")
	scheduleID := automationResourceID(t, saved.Definition, "schedule", "schedule")
	task, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, "Review one request.\n\nConnected Task handoff:\nDo not create or schedule the connected downstream Task yourself. OpenVibely activates it automatically after this task completes successfully.", task.Prompt)
	schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.Equal(t, taskID, schedule.TaskID)
	require.True(t, schedule.Enabled)
	require.True(t, schedule.ClearContextOnStart)
}

func TestAutomationSaveRecreatesDeletedRetainedNativeNodeResources(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Recover deleted retained Native node resources")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)

	optimizationTaskID := automationResourceID(t, first.Definition, "optimization_finder", "task")
	optimizationScheduleID := automationResourceID(t, first.Definition, "optimization_finder", "schedule")
	visionTaskID := automationResourceID(t, first.Definition, "vision_suggestions", "task")
	visionScheduleID := automationResourceID(t, first.Definition, "vision_suggestions", "schedule")
	originalNodeCount := len(first.Definition.Nodes)
	originalEdgeCount := len(first.Definition.Edges)
	originalOptimizationNode := automationDraftNodeByKey(t, candidate, "optimization_finder")

	require.NoError(t, h.scheduleRepo.Delete(ctx, optimizationScheduleID))
	require.NoError(t, h.taskRepo.Delete(ctx, optimizationTaskID))
	deletedTask, err := h.taskRepo.GetByID(ctx, optimizationTaskID)
	require.NoError(t, err)
	require.Nil(t, deletedTask)
	deletedSchedule, err := h.scheduleRepo.GetByID(ctx, optimizationScheduleID)
	require.NoError(t, err)
	require.Nil(t, deletedSchedule)

	reopened, err := h.drafts.CurrentCandidate(ctx, h.project.ID, first.Definition.Automation.ID)
	require.NoError(t, err)
	plan, _, err := h.compiler.PreviewSave(ctx, h.project.ID, reopened.Candidate)
	require.NoError(t, err)
	require.Empty(t, plan.Validation)
	optimizedReopenedNode := automationDraftNodeByKey(t, reopened.Candidate, "optimization_finder")
	require.Equal(t, originalOptimizationNode.Name, optimizedReopenedNode.Name)
	require.Equal(t, originalOptimizationNode.Config["prompt"], optimizedReopenedNode.Config["prompt"])

	recovered, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: reopened.Candidate})
	require.NoError(t, err)
	require.Len(t, recovered.Definition.Nodes, originalNodeCount)
	require.Len(t, recovered.Definition.Edges, originalEdgeCount)
	recoveredOptimizationTaskID := automationResourceID(t, recovered.Definition, "optimization_finder", "task")
	recoveredOptimizationScheduleID := automationResourceID(t, recovered.Definition, "optimization_finder", "schedule")
	require.NotEqual(t, optimizationTaskID, recoveredOptimizationTaskID)
	require.NotEqual(t, optimizationScheduleID, recoveredOptimizationScheduleID)
	require.Equal(t, visionTaskID, automationResourceID(t, recovered.Definition, "vision_suggestions", "task"))
	require.Equal(t, visionScheduleID, automationResourceID(t, recovered.Definition, "vision_suggestions", "schedule"))
	require.Equal(t, originalOptimizationNode.Config["prompt"], automationDraftNodeByKey(t, reopened.Candidate, "optimization_finder").Config["prompt"])
	persisted, err := h.automationRepo.GetDefinitionVersion(ctx, h.project.ID, recovered.Definition.Automation.ID, recovered.Definition.Version.ID)
	require.NoError(t, err)
	require.NotNil(t, persisted)
	require.Len(t, persisted.Nodes, originalNodeCount)
	taskBindings, scheduleBindings := 0, 0
	for _, resource := range persisted.Resources {
		if resource.NodeID == automationNodeByKey(t, persisted, "optimization_finder").ID {
			switch resource.ResourceType {
			case "task":
				taskBindings++
			case "schedule":
				scheduleBindings++
			}
		}
	}
	require.Equal(t, 1, taskBindings)
	require.Equal(t, 1, scheduleBindings)

	repeated, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: reopened.Candidate})
	require.NoError(t, err)
	require.Equal(t, recoveredOptimizationTaskID, automationResourceID(t, repeated.Definition, "optimization_finder", "task"))
	require.Equal(t, recoveredOptimizationScheduleID, automationResourceID(t, repeated.Definition, "optimization_finder", "schedule"))
	originCount, err := h.stores.Fixtures.CountTasksByCreationOrigin(ctx, h.project.ID, persistence.AutomationCompilerTaskCreatedVia(first.Definition.Automation.ID, "optimization_finder"))
	require.NoError(t, err)
	require.Equal(t, 1, originCount)
	ownerCount, err := h.stores.Fixtures.CountAutomationTriggerOwners(ctx, h.project.ID, first.Definition.Automation.ID, automationNodeByKey(t, repeated.Definition, "optimization_finder").ID)
	require.NoError(t, err)
	require.Equal(t, 1, ownerCount)
}

func TestAutomationSaveRecreatesDeletedRetainedNativeResourcesPreservesPausedLifecycle(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Recover deleted retained paused Native resources")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, first.Definition.Automation.ID))
	oldTaskID := automationResourceID(t, first.Definition, "optimization_finder", "task")
	oldScheduleID := automationResourceID(t, first.Definition, "optimization_finder", "schedule")
	require.NoError(t, h.taskRepo.Delete(ctx, oldTaskID))
	deletedSchedule, err := h.scheduleRepo.GetByID(ctx, oldScheduleID)
	require.NoError(t, err)
	require.Nil(t, deletedSchedule)

	reopened, err := h.drafts.CurrentCandidate(ctx, h.project.ID, first.Definition.Automation.ID)
	require.NoError(t, err)
	recovered, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: reopened.Candidate})
	require.NoError(t, err)
	require.Equal(t, models.AutomationPaused, recovered.Definition.Automation.LifecycleState)

	newTaskID := automationResourceID(t, recovered.Definition, "optimization_finder", "task")
	newScheduleID := automationResourceID(t, recovered.Definition, "optimization_finder", "schedule")
	require.NotEqual(t, oldTaskID, newTaskID)
	require.NotEqual(t, oldScheduleID, newScheduleID)
	newTask, err := h.taskRepo.GetByID(ctx, newTaskID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryScheduled, newTask.Category)
	newSchedule, err := h.scheduleRepo.GetByID(ctx, newScheduleID)
	require.NoError(t, err)
	require.False(t, newSchedule.Enabled)
	owner, err := h.automationRepo.GetTriggerOwner(ctx, newScheduleID)
	require.NoError(t, err)
	require.NotNil(t, owner)
	require.Equal(t, "paused", owner.OwnershipState)
}

func TestAutomationLiveCandidateDerivesMissingMetadataFromLiveGraph(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Live candidate graph fallback")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Live candidate graph fallback", "Review the current request.")
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NoError(t, h.stores.Fixtures.RemoveAutomationCandidate(ctx, h.project.ID, saved.Definition.Automation.ID, saved.Definition.Version.ID))

	graph, err := NewAutomationGraphService(h.automationRepo).GetLive(ctx, h.project.ID, saved.Definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	live, err := h.drafts.LoadLiveCandidate(ctx, h.project.ID, graph)
	require.NoError(t, err)

	expected, err := h.drafts.candidateFromDefinition(ctx, h.project.ID, saved.Definition.Automation.ID, saved.Definition)
	require.NoError(t, err)
	expected, err = h.drafts.hydratePersistedScheduleContext(ctx, h.project.ID, expected, saved.Definition)
	require.NoError(t, err)
	expected, err = h.drafts.normalizeReopenedCandidate(expected)
	require.NoError(t, err)
	require.Equal(t, expected, live.Candidate)
	require.Equal(t, "right", live.Candidate.Edges[0].FromPort)
	require.Equal(t, "left", live.Candidate.Edges[0].ToPort)
}

func TestAutomationLiveCandidateMatchesCurrentCandidate(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Live candidate serialization")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Live candidate serialization", "Review the current request.")
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)

	graph, err := NewAutomationGraphService(h.automationRepo).GetLive(ctx, h.project.ID, saved.Definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	live, err := h.drafts.LoadLiveCandidate(ctx, h.project.ID, graph)
	require.NoError(t, err)
	current, err := h.drafts.CurrentCandidate(ctx, h.project.ID, saved.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, current.Candidate, live.Candidate)

	expectedYAML, err := EncodeAutomationDraftYAML(current.Candidate)
	require.NoError(t, err)
	actualYAML, err := EncodeAutomationDraftYAML(live.Candidate)
	require.NoError(t, err)
	require.Equal(t, expectedYAML, actualYAML)
}

func TestAutomationLiveCandidatePreservesLegacyScheduleContext(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Live legacy schedule context")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Live legacy schedule context", "Review the current request.")
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	scheduleID := automationResourceID(t, saved.Definition, "schedule", "schedule")
	schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.NotNil(t, schedule)
	schedule.ClearContextOnStart = false
	require.NoError(t, h.scheduleRepo.Update(ctx, schedule))

	graph, err := NewAutomationGraphService(h.automationRepo).GetLive(ctx, h.project.ID, saved.Definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	live, err := h.drafts.LoadLiveCandidate(ctx, h.project.ID, graph)
	require.NoError(t, err)
	liveSchedule := automationDraftNodeByKey(t, live.Candidate, "schedule")
	require.Equal(t, false, liveSchedule.Config["clear_context_on_start"])

	require.NoError(t, h.scheduleRepo.Delete(ctx, scheduleID))
	graph, err = NewAutomationGraphService(h.automationRepo).GetLive(ctx, h.project.ID, saved.Definition.Automation.ID, time.Now().UTC())
	require.NoError(t, err)
	live, err = h.drafts.LoadLiveCandidate(ctx, h.project.ID, graph)
	require.NoError(t, err)
	_, hasPersistedContext := automationDraftNodeByKey(t, live.Candidate, "schedule").Config["clear_context_on_start"]
	require.False(t, hasPersistedContext, "a deleted schedule has no stored context value to hydrate")
}

func TestAutomationCurrentCandidateRecoversMissingScheduleContextAfterDeletion(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Recover missing schedule context")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Recover missing schedule context", "Review one request.")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	oldScheduleID := automationResourceID(t, first.Definition, "schedule", "schedule")
	require.NoError(t, h.scheduleRepo.Delete(ctx, oldScheduleID))

	reopened, err := h.drafts.CurrentCandidate(ctx, h.project.ID, first.Definition.Automation.ID)
	require.NoError(t, err)
	_, hasPersistedContext := automationDraftNodeByKey(t, reopened.Candidate, "schedule").Config["clear_context_on_start"]
	require.False(t, hasPersistedContext, "a deleted schedule has no stored context value to hydrate")
	recovered, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: reopened.Candidate})
	require.NoError(t, err)
	newScheduleID := automationResourceID(t, recovered.Definition, "schedule", "schedule")
	require.NotEqual(t, oldScheduleID, newScheduleID)
	newSchedule, err := h.scheduleRepo.GetByID(ctx, newScheduleID)
	require.NoError(t, err)
	require.True(t, newSchedule.ClearContextOnStart)
}

func TestAutomationSavePreservesLegacyScheduleClearContextValue(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Legacy schedule context")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Legacy schedule context", "Review one request.")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{
		ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
	})
	require.NoError(t, err)

	scheduleID := automationResourceID(t, first.Definition, "schedule", "schedule")
	schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.NotNil(t, schedule)
	schedule.ClearContextOnStart = false
	require.NoError(t, h.scheduleRepo.Update(ctx, schedule))

	delete(candidate.Nodes[0].Config, "clear_context_on_start")
	require.NoError(t, h.stores.Fixtures.SetAutomationCandidate(ctx, first.Definition.Automation.ID, candidate))

	reopened, err := h.drafts.CurrentCandidate(ctx, h.project.ID, first.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, false, automationDraftNodeByKey(t, reopened.Candidate, "schedule").Config["clear_context_on_start"])

	delete(automationDraftNodeByKey(t, reopened.Candidate, "schedule").Config, "clear_context_on_start")
	_, err = h.compiler.Save(ctx, AutomationSaveRequest{
		ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: reopened.Candidate,
	})
	require.NoError(t, err)
	stored, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.False(t, stored.ClearContextOnStart)
}

func TestAutomationReplacementUsesOneCurrentGraphAndDeletesRemovedSchedule(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Atomic replacement")
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
	versions, err := h.stores.Fixtures.AutomationVersions(ctx, saved.Definition.Automation.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, saved.Definition.Version.ID, versions[0].ID)
	require.NotEqual(t, oldGraphID, versions[0].ID)

	removed, err := h.scheduleRepo.GetByID(ctx, oldScheduleID)
	require.NoError(t, err)
	require.Nil(t, removed)
}

func TestAutomationSaveRollsBackAllRowsWhenScheduleCreationFails(t *testing.T) {
	t.Run("isolated", func(t *testing.T) { testAutomationSaveRollsBackAllRowsWhenScheduleCreationFails(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationSaveRollsBackAllRowsWhenScheduleCreationFails(t, teststores.WithProductionTopology())
	})
}
func testAutomationSaveRollsBackAllRowsWhenScheduleCreationFails(t *testing.T, opts ...teststores.Option) {
	h := newAutomationSaveStoreHarnessWithOptions(t, opts, "Atomic automation save")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	require.NoError(t, h.stores.Fixtures.FailScheduleCreation(ctx))

	_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.ErrorContains(t, err, "injected schedule failure")
	require.Equal(t, storefixture.AutomationStateCounts{}, automationStateForTest(t, h.stores), "failed Save must leave no graph or runtime resources")
}

func TestAutomationReplacementRollsBackToCurrentGraphWhenScheduleUpdateFails(t *testing.T) {
	t.Run("isolated", func(t *testing.T) { testAutomationReplacementRollsBackToCurrentGraphWhenScheduleUpdateFails(t) })
	t.Run("production", func(t *testing.T) {
		testAutomationReplacementRollsBackToCurrentGraphWhenScheduleUpdateFails(t, teststores.WithProductionTopology())
	})
}
func testAutomationReplacementRollsBackToCurrentGraphWhenScheduleUpdateFails(t *testing.T, opts ...teststores.Option) {
	h := newAutomationSaveStoreHarnessWithOptions(t, opts, "Atomic automation replacement")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	currentGraphID := first.Definition.Version.ID
	taskID := automationResourceID(t, first.Definition, "vision_suggestions", "task")
	originalTask, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)

	candidate.Nodes[0].Config["prompt"] = "replacement prompt that must roll back"
	require.NoError(t, h.stores.Fixtures.FailScheduleUpdate(ctx))
	_, err = h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.ErrorContains(t, err, "injected schedule update failure")
	current, err := h.automationRepo.GetDefinition(ctx, h.project.ID, first.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, currentGraphID, current.Version.ID)
	storedTask, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Equal(t, originalTask.Prompt, storedTask.Prompt)
	versions, err := h.stores.Fixtures.AutomationVersions(ctx, first.Definition.Automation.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
}

func TestAutomationSavePreservesPausedAndArchivedLifecycle(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Atomic lifecycle Save")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, first.Definition.Automation.ID))
	candidate.Nodes[0].Config["prompt"] = "Paused replacement prompt"
	paused, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Equal(t, models.AutomationPaused, paused.Definition.Automation.LifecycleState)
	schedule, err := h.scheduleRepo.GetByID(ctx, automationResourceID(t, paused.Definition, "vision_suggestions", "schedule"))
	require.NoError(t, err)
	require.False(t, schedule.Enabled)

	require.NoError(t, h.lifecycle.Archive(ctx, h.project.ID, first.Definition.Automation.ID))
	candidate.Nodes[0].Config["prompt"] = "Archived replacement prompt"
	archived, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID,
		Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Equal(t, models.AutomationArchived, archived.Definition.Automation.LifecycleState)
	schedule, err = h.scheduleRepo.GetByID(ctx, automationResourceID(t, archived.Definition, "vision_suggestions", "schedule"))
	require.NoError(t, err)
	require.False(t, schedule.Enabled)
}

func TestAutomationPauseAndArchiveDemotePendingActiveRoots(t *testing.T) {
	for _, state := range []models.AutomationLifecycleState{models.AutomationPaused, models.AutomationArchived} {
		t.Run(string(state), func(t *testing.T) {
			h := newAutomationSaveStoreHarness(t, "Lifecycle root admission "+string(state))
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

func seedActivityOnlyAutomationIssueTask(t *testing.T, h automationSaveHarness, definition *models.AutomationDefinition, nodeKey string, category models.TaskCategory, activityKey ...string) models.Task {
	t.Helper()
	ctx := context.Background()
	node := automationNodeByKey(t, definition, nodeKey)
	task := models.Task{ProjectID: h.project.ID, Title: "Issue-specific task " + persistence.NewID(), Category: category,
		Priority: 2, Status: models.StatusPending, Prompt: "Implement the approved issue.",
		CreatedVia: persistence.AutomationCompilerTaskCreatedVia(definition.Automation.ID, node.NodeKey)}
	require.NoError(t, h.taskRepo.Create(ctx, &task))
	key := "implementation-task:" + task.ID
	if len(activityKey) > 0 {
		key = activityKey[0]
	}
	binding := models.AutomationBinding{AutomationID: definition.Automation.ID, VersionID: definition.Version.ID, NodeID: node.ID}
	_, _, err := h.automationRepo.RecordProjectionEvent(ctx, persistence.AutomationProjectionEvent{
		Context: models.AutomationContext{ProjectID: h.project.ID}, Binding: binding,
		WorkItemKey: "issue-work-item:" + persistence.NewID(), ActivityKey: key,
		ActivityType: "create_task", ActivityStatus: models.AutomationActivityCompleted,
		Resources: []models.AutomationActivityResource{{ResourceType: "task", ResourceID: task.ID, Relation: "child"}},
	})
	require.NoError(t, err)
	return task
}

func TestAutomationPauseArchiveAndResumeActivityOnlyIssueTasks(t *testing.T) {
	for _, state := range []models.AutomationLifecycleState{models.AutomationPaused, models.AutomationArchived} {
		t.Run(string(state), func(t *testing.T) {
			h := newAutomationSaveStoreHarness(t, "Activity issue lifecycle "+string(state))
			ctx := context.Background()
			saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web",
				Candidate: customTaskOnlyCandidate("Activity issue lifecycle", "Keep the definition task in Backlog.", models.CategoryBacklog)})
			require.NoError(t, err)
			issueTask := seedActivityOnlyAutomationIssueTask(t, h, saved.Definition, "root", models.CategoryActive)

			if state == models.AutomationPaused {
				require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, saved.Definition.Automation.ID))
			} else {
				require.NoError(t, h.lifecycle.Archive(ctx, h.project.ID, saved.Definition.Automation.ID))
			}
			stored, err := h.taskRepo.GetByID(ctx, issueTask.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, stored.Category)
			require.Equal(t, models.StatusPending, stored.Status)

			recorder := &recordingAutomationTaskService{TaskService: NewTaskService(h.taskRepo, h.stores.Attachments, nil)}
			lifecycle := NewAutomationLifecycleService(h.automationRepo, h.scheduleRepo, recorder)
			if state == models.AutomationPaused {
				require.NoError(t, lifecycle.Resume(ctx, h.project.ID, saved.Definition.Automation.ID))
				require.Len(t, recorder.submitted, 1)
				require.Equal(t, issueTask.ID, recorder.submitted[0].ID)
				require.Equal(t, models.CategoryActive, recorder.submitted[0].Category)
				require.NoError(t, lifecycle.Resume(ctx, h.project.ID, saved.Definition.Automation.ID))
				require.Len(t, recorder.submitted, 1, "activity-only issue Task must be resumed exactly once")
			} else {
				require.ErrorContains(t, lifecycle.Resume(ctx, h.project.ID, saved.Definition.Automation.ID), "archived automation cannot be resumed")
				require.Empty(t, recorder.submitted)
			}
		})
	}
}

func TestAutomationPauseAndResumeGenericActivityOnlyCreatedTask(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Generic activity child lifecycle")
	ctx := context.Background()
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web",
		Candidate: customTaskOnlyCandidate("Generic activity child lifecycle", "Keep the definition task in Backlog.", models.CategoryBacklog)})
	require.NoError(t, err)
	child := seedActivityOnlyAutomationIssueTask(t, h, saved.Definition, "root", models.CategoryActive, "execution:generic:create-task")

	require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, saved.Definition.Automation.ID))
	stored, err := h.taskRepo.GetByID(ctx, child.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, stored.Category)
	recorder := &recordingAutomationTaskService{TaskService: NewTaskService(h.taskRepo, h.stores.Attachments, nil)}
	require.NoError(t, NewAutomationLifecycleService(h.automationRepo, h.scheduleRepo, recorder).Resume(ctx, h.project.ID, saved.Definition.Automation.ID))
	require.Len(t, recorder.submitted, 1)
	require.Equal(t, child.ID, recorder.submitted[0].ID)
}

func TestAutomationDispatchClaimRejectsCurrentActivityTaskAfterLifecycleDeactivation(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Activity issue dispatch gate")
	ctx := context.Background()
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web",
		Candidate: customTaskOnlyCandidate("Activity issue dispatch gate", "Keep the definition task in Backlog.", models.CategoryBacklog)})
	require.NoError(t, err)
	issueTask := seedActivityOnlyAutomationIssueTask(t, h, saved.Definition, "root", models.CategoryActive)

	// Simulate Pause winning the lifecycle/claim serialization point while an
	// already queued Task still carries its pre-Pause Active snapshot. The final
	// dispatch gate must fail closed even without relying on queue pruning.
	require.NoError(t, h.stores.Fixtures.SetAutomationLifecycleSnapshot(ctx, h.project.ID, saved.Definition.Automation.ID, models.AutomationPaused))
	claim, claimed, err := h.taskRepo.ClaimTaskForDispatch(ctx, issueTask.ID)
	require.NoError(t, err)
	require.False(t, claimed)
	require.NotNil(t, claim)
	stored, err := h.taskRepo.GetByID(ctx, issueTask.ID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, stored.Category)
	require.Equal(t, models.StatusPending, stored.Status)
}

func TestAutomationSaveReplacesLegacyRetainedDraftGraph(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Legacy retained draft Save")
	ctx := context.Background()
	candidate := customTaskOnlyCandidate("Legacy retained draft Save", "Run current behavior.", models.CategoryBacklog)
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NoError(t, h.stores.Fixtures.SeedAutomationVersion(ctx, models.AutomationVersion{ID: "legacy-failed-draft", ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID, Version: 2, State: "draft", Source: "manual", AdapterKey: "custom"}))

	retainedTask := models.Task{ProjectID: h.project.ID, Title: "Legacy failed draft schedule task", Category: models.CategoryScheduled,
		Priority: 2, Status: models.StatusPending, Prompt: "Preserve this domain task."}
	require.NoError(t, h.taskRepo.Create(ctx, &retainedTask))
	retainedSchedule := models.Schedule{TaskID: retainedTask.ID, RunAt: time.Now().UTC().Add(time.Hour), RepeatType: models.RepeatDaily,
		RepeatInterval: 1, Enabled: false}
	require.NoError(t, h.scheduleRepo.Create(ctx, &retainedSchedule))
	require.NoError(t, h.stores.Fixtures.SeedRetainedScheduleOwnership(ctx, models.AutomationNode{ID: "legacy-failed-schedule-node", ProjectID: h.project.ID, AutomationID: first.Definition.Automation.ID, VersionID: "legacy-failed-draft", NodeKey: "legacy_schedule", Name: "Legacy schedule", NodeType: "trigger", Role: "schedule"}, retainedSchedule.ID))

	candidate.Description = "Replacement behavior."
	replacement, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID,
		AutomationID: first.Definition.Automation.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NotEqual(t, first.Definition.Version.ID, replacement.Definition.Version.ID)
	versions, err := h.stores.Fixtures.AutomationVersions(ctx, first.Definition.Automation.ID)
	require.NoError(t, err)
	require.Len(t, versions, 1)
	require.Equal(t, replacement.Definition.Version.ID, versions[0].ID)
	require.NotEqual(t, "legacy-failed-draft", versions[0].ID)
	removed, err := h.scheduleRepo.GetByID(ctx, retainedSchedule.ID)
	require.NoError(t, err)
	require.Nil(t, removed, "replacement Save must remove schedules exclusively owned by discarded graphs")
	retained, err := h.taskRepo.GetByID(ctx, retainedTask.ID)
	require.NoError(t, err)
	require.NotNil(t, retained, "replacement Save must preserve the backing domain Task")

	require.Equal(t, 1, replacement.Definition.Version.Version)
}

func TestAutomationSavePreservesScheduleTimingWhenCadenceIsUnchanged(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Preserve schedule timing")
	ctx := context.Background()
	candidate := customScheduledTaskCandidate("Scheduled review", "Review one request.")
	first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	scheduleID := automationResourceID(t, first.Definition, "schedule", "schedule")
	preservedNextRun := time.Date(2042, time.March, 4, 5, 6, 7, 0, time.UTC)
	require.NoError(t, h.stores.Fixtures.SetScheduleNextRun(ctx, scheduleID, &preservedNextRun))
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
	h := newAutomationSaveStoreHarness(t, "Rename task titles")
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
		setup func(*testing.T, *teststores.Env, string)
	}{
		{name: "backlog to active", setup: func(t *testing.T, db *teststores.Env, taskID string) {
			err := db.Fixtures.SetTaskCategory(t.Context(), taskID, models.CategoryBacklog)
			require.NoError(t, err)
		}},
		{name: "completed reset to pending", setup: func(t *testing.T, db *teststores.Env, taskID string) {
			err := db.Fixtures.SetTaskStatus(t.Context(), taskID, models.StatusCompleted)
			require.NoError(t, err)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAutomationSaveStoreHarness(t, "Submit existing root "+test.name)
			ctx := context.Background()
			recorder := &recordingAutomationTaskService{TaskService: NewTaskService(h.taskRepo, h.stores.Attachments, nil)}
			h.compiler.taskSvc = recorder
			candidate := customTaskOnlyCandidate("Runnable root", "Run this root.", models.CategoryActive)
			first, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
			require.NoError(t, err)
			taskID := automationResourceID(t, first.Definition, "root", "task")
			recorder.submitted = nil
			test.setup(t, h.stores, taskID)

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

func TestAutomationCompiledCustomGitHubInboxScopeIsLimitedToItsAssignmentBranch(t *testing.T) {
	candidate := models.AutomationDraftCandidate{AdapterKey: AutomationAdapterCustom, Nodes: []models.AutomationDraftNode{
		{Key: "model_producer", Name: "Daily model release review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Find newly released AI models requiring support."}},
		{Key: "model_issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{}},
		{Key: "model_assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{}},
		{Key: "model_inbox", Type: models.AutomationNodeTrigger, Role: "github_inbox", Config: map[string]any{"prompt": "Process assigned model support issues."}},
		{Key: "model_implementation", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement model support.", "category": "active", "priority": 2}},
		{Key: "model_pr", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open the model support pull request."}},
		{Key: "security_producer", Name: "Weekly security review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Find security hardening work only."}},
		{Key: "security_issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{}},
		{Key: "security_assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{}},
		{Key: "security_inbox", Type: models.AutomationNodeTrigger, Role: "github_inbox", Config: map[string]any{"prompt": "Process assigned security issues."}},
	}, Edges: []models.AutomationDraftEdge{
		{Key: "model_producer_issue", From: "model_producer", To: "model_issue"},
		{Key: "model_issue_assignment", From: "model_issue", To: "model_assignment"},
		{Key: "model_assignment_inbox", From: "model_assignment", To: "model_inbox"},
		{Key: "model_inbox_implementation", From: "model_inbox", To: "model_implementation"},
		{Key: "model_implementation_pr", From: "model_implementation", To: "model_pr"},
		{Key: "security_producer_issue", From: "security_producer", To: "security_issue"},
		{Key: "security_issue_assignment", From: "security_issue", To: "security_assignment"},
		{Key: "security_assignment_inbox", From: "security_assignment", To: "security_inbox"},
	}}

	prompt := automationCompiledTaskPrompt(candidate, candidate.Nodes[3])
	require.Contains(t, prompt, `Producer: "Daily model release review"`)
	require.Contains(t, prompt, `Purpose: "Find newly released AI models requiring support."`)
	require.NotContains(t, prompt, "Weekly security review")
	require.NotContains(t, prompt, "Find security hardening work only.")
}

func TestAutomationCustomScheduledGitHubInboxCreatesRuntimeIssueTasksWithoutRelay(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Scheduled GitHub inbox")
	ctx := context.Background()
	projectRepo := h.stores.Projects
	h.project.RepoURL = "https://github.com/example/automation.git"
	require.NoError(t, projectRepo.Update(ctx, &h.project))
	settingsRepo := h.stores.Settings
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "test-token"))
	githubAuthRepo := h.stores.GitHubAuth
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
	h.compiler.validator.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)

	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Scheduled inbox", AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{
			{Key: "producer", Name: "Find model releases", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Find relevant model releases.", "category": "scheduled", "priority": 2, "run_at": "04:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
			{Key: "issue", Name: "Create issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"instructions": "Open one focused model support issue.", "labels": []any{"model-support"}}},
			{Key: "assignment", Name: "Human assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{"approval_method": "github_assignment"}},
			{Key: "inbox", Name: "Scheduled model support inbox", Type: models.AutomationNodeTrigger, Role: "github_inbox", Config: map[string]any{"prompt": "Process assigned model support issues and create or continue issue-linked implementation tasks.", "category": "scheduled", "priority": 2, "run_at": "04:15", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
			{Key: "implementation", Name: "Issue implementation configuration", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the accepted model support issue.", "category": "active", "priority": 2}},
			{Key: "open_pr", Name: "Open pull request", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open a reviewable pull request.", "base": "main", "draft": false}},
			{Key: "review", Name: "Human review", Type: models.AutomationNodeHumanGate, Role: "pull_request_review", Config: map[string]any{"approval_method": "pull_request_review"}},
			{Key: "complete", Name: "Completed", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		},
		Edges: []models.AutomationDraftEdge{
			{Key: "producer_issue", From: "producer", To: "issue", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "issue_assignment", From: "issue", To: "assignment", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "assignment_inbox", From: "assignment", To: "inbox", FromPort: "right", ToPort: "left", Label: "assigned", Condition: map[string]any{"state": "assigned"}},
			{Key: "inbox_implementation", From: "inbox", To: "implementation", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "implementation_pr", From: "implementation", To: "open_pr", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "pr_review", From: "open_pr", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "review_complete", From: "review", To: "complete", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		}}

	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NotEmpty(t, automationResourceID(t, saved.Definition, "inbox", "task"))
	require.NotEmpty(t, automationResourceID(t, saved.Definition, "inbox", "schedule"))
	for _, resource := range saved.Definition.Resources {
		require.False(t, resource.NodeKey == "implementation" && resource.ResourceType == "task", "issue-specific implementation is created only when the scheduled inbox processes an assigned issue")
	}
	inboxTask, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, "inbox", "task"))
	require.NoError(t, err)
	require.Contains(t, inboxTask.Prompt, "Process open issues assigned to the PAT owner or configured GitHub Authorized Users for this inbox")
	require.Contains(t, inboxTask.Prompt, "whether the issue was created by this Automation or manually in GitHub")
	require.Contains(t, inboxTask.Prompt, `Producer: "Find model releases"`)
	require.Contains(t, inboxTask.Prompt, `Purpose: "Find relevant model releases."`)
	require.Contains(t, inboxTask.Prompt, "Create at most one visible task per actionable assigned issue")
	require.NotContains(t, inboxTask.Prompt, "Connected Task handoff")
}

func TestAutomationResumeAdmitsDeferredScheduleGitHubInboxHandoff(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Deferred scheduled GitHub inbox")
	ctx := context.Background()
	projectRepo := h.stores.Projects
	h.project.RepoURL = "https://github.com/example/automation.git"
	require.NoError(t, projectRepo.Update(ctx, &h.project))
	settingsRepo := h.stores.Settings
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
	require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "test-token"))
	githubAuthRepo := h.stores.GitHubAuth
	require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
	h.compiler.validator.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)

	candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Deferred scheduled inbox", Description: "Resume a scheduled GitHub inbox handoff",
		AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{
			{Key: "producer", Name: "Find improvements", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Find one focused improvement.", "category": "backlog", "priority": 2}},
			{Key: "issue", Name: "Create issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"instructions": "Open one reviewable suggestion issue.", "labels": []any{"suggestion"}}},
			{Key: "assignment", Name: "Human assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{"approval_method": "github_assignment"}},
			{Key: "schedule", Name: "Hourly inbox", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Poll the assigned issue inbox.", "category": "scheduled", "priority": 2, "run_at": "09:15", "repeat_type": "hours", "repeat_interval": 1, "enabled": true}},
			{Key: "followup", Name: "GitHub inbox", Type: models.AutomationNodeAgentTask, Role: "github_inbox", Config: map[string]any{"prompt": "Process newly assigned issues.", "category": "backlog", "priority": 3}},
			{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the accepted issue.", "category": "active", "priority": 3}},
			{Key: "open_pr", Name: "Open pull request", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open a reviewable pull request.", "base": "main", "draft": false}},
			{Key: "review", Name: "Human review", Type: models.AutomationNodeHumanGate, Role: "pull_request_review", Config: map[string]any{"approval_method": "pull_request_review"}},
			{Key: "complete", Name: "Merged", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		},
		Edges: []models.AutomationDraftEdge{
			{Key: "producer_issue", From: "producer", To: "issue", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "issue_assignment", From: "issue", To: "assignment", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "schedule_inbox", From: "schedule", To: "followup", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "assignment_inbox", From: "assignment", To: "followup", FromPort: "right", ToPort: "left", Label: "assigned", Condition: map[string]any{"state": "assigned"}},
			{Key: "inbox_implementation", From: "followup", To: "implementation", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "implementation_pr", From: "implementation", To: "open_pr", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "pr_review", From: "open_pr", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			{Key: "review_complete", From: "review", To: "complete", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		}}
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{
		ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
	})
	require.NoError(t, err)

	parent, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, "schedule", "task"))
	require.NoError(t, err)
	childID := automationResourceID(t, saved.Definition, "followup", "task")
	child, err := h.taskRepo.GetByID(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, child.Category)
	require.NoError(t, h.lifecycle.Pause(ctx, h.project.ID, saved.Definition.Automation.ID))

	execution := models.Execution{TaskID: parent.ID, Status: models.ExecRunning, PromptSent: parent.Prompt}
	require.NoError(t, h.stores.Executions.Create(ctx, &execution))
	sourceNode := automationNodeByKey(t, saved.Definition, "schedule")
	binding := models.AutomationBinding{AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: sourceNode.ID}
	causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{binding}})
	causalCtx = withAutomationExecution(causalCtx, parent.ID, execution.ID)
	config, err := parent.ParseChainConfig()
	require.NoError(t, err)
	require.Equal(t, string(models.CategoryActive), config.ChildCategory, "Schedule handoffs are effectively runnable even when the saved inbox category is Backlog")
	llmSvc := &LLMService{taskRepo: h.taskRepo, automationRepo: h.automationRepo,
		taskSvc: NewTaskService(h.taskRepo, h.stores.Attachments, nil)}
	require.NoError(t, llmSvc.activateCompiledAutomationChild(causalCtx, *parent, "assigned issues discovered\n[STATUS: SUCCESS]", config))

	deferred, err := h.taskRepo.GetByID(ctx, childID)
	require.NoError(t, err)
	require.Equal(t, models.CategoryBacklog, deferred.Category)
	entered := countAutomationHandoffs(t, h, saved.Definition, sourceNode.ID, automationNodeByKey(t, saved.Definition, "followup").ID, models.AutomationTransitionEntered)
	require.Equal(t, 1, entered)

	recorder := &recordingAutomationTaskService{TaskService: NewTaskService(h.taskRepo, h.stores.Attachments, nil)}
	resume := NewAutomationLifecycleService(h.automationRepo, h.scheduleRepo, recorder)
	require.NoError(t, resume.Resume(ctx, h.project.ID, saved.Definition.Automation.ID))
	require.Len(t, recorder.submitted, 1)
	require.Equal(t, childID, recorder.submitted[0].ID)
	require.Equal(t, models.CategoryActive, recorder.submitted[0].Category)

	require.NoError(t, resume.Resume(ctx, h.project.ID, saved.Definition.Automation.ID))
	require.Len(t, recorder.submitted, 1, "the deferred Schedule handoff must be admitted exactly once")
}

func TestAutomationPublishedCustomTaskActivationPreservesDownstreamHandoffPrompts(t *testing.T) {
	activate := func(t *testing.T, h automationSaveHarness, candidate models.AutomationDraftCandidate, parentKey, childKey string) models.Task {
		t.Helper()
		ctx := context.Background()
		saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
		require.NoError(t, err)
		parent, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, parentKey, "task"))
		require.NoError(t, err)
		childID := automationResourceID(t, saved.Definition, childKey, "task")
		tasksBefore, err := h.taskRepo.ListByProject(ctx, h.project.ID, "")
		require.NoError(t, err)
		require.NoError(t, h.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning))
		execution := models.Execution{TaskID: parent.ID, Status: models.ExecRunning, PromptSent: parent.Prompt}
		require.NoError(t, h.stores.Executions.Create(ctx, &execution))
		sourceNode := automationNodeByKey(t, saved.Definition, parentKey)
		binding := models.AutomationBinding{AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: sourceNode.ID}
		causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{binding}})
		causalCtx = withAutomationExecution(causalCtx, parent.ID, execution.ID)
		worker := NewWorkerService(nil, 1, h.stores.Projects)
		worker.SetTaskRepo(h.taskRepo)
		taskSvc := NewTaskService(h.taskRepo, h.stores.Attachments, worker)
		llmSvc := &LLMService{taskRepo: h.taskRepo, automationRepo: h.automationRepo, taskSvc: taskSvc}
		handled, err := llmSvc.activatePublishedCustomAutomationChild(causalCtx, *parent, "upstream result\n[STATUS: SUCCESS]")
		require.NoError(t, err)
		require.True(t, handled)
		activated, err := h.taskRepo.GetByID(ctx, childID)
		require.NoError(t, err)
		tasksAfter, err := h.taskRepo.ListByProject(ctx, h.project.ID, "")
		require.NoError(t, err)
		require.Len(t, tasksAfter, len(tasksBefore))
		require.Contains(t, activated.Prompt, "upstream result")
		select {
		case submitted := <-worker.Submitted():
			require.Equal(t, childID, submitted.ID)
		default:
			t.Fatal("activated child was not submitted")
		}
		return *activated
	}

	t.Run("pull request handoff", func(t *testing.T) {
		h := newAutomationSaveStoreHarness(t, "Custom PR activation")
		ctx := context.Background()
		projectRepo := h.stores.Projects
		h.project.RepoURL = "https://github.com/example/automation.git"
		require.NoError(t, projectRepo.Update(ctx, &h.project))
		settingsRepo := h.stores.Settings
		require.NoError(t, settingsRepo.Set(ctx, GitHubSettingAuthMode, GitHubAuthModePAT))
		require.NoError(t, settingsRepo.Set(ctx, GitHubSettingPAT, "test-token"))
		githubAuthRepo := h.stores.GitHubAuth
		require.NoError(t, githubAuthRepo.UpsertAuthorizedActor(ctx, &models.GitHubAuthorizedActor{GitHubLogin: "automation-bot"}))
		h.compiler.validator.SetCapabilityDependencies(projectRepo, settingsRepo, githubAuthRepo)
		candidate := models.AutomationDraftCandidate{SchemaVersion: 1, Name: "Custom PR activation", AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
			Nodes: []models.AutomationDraftNode{
				{Key: "producer", Name: "Find issues", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Find relevant issues.", "category": "scheduled", "priority": 2, "run_at": "04:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
				{Key: "issue", Name: "Create issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"instructions": "Open one focused issue.", "labels": []any{"bug"}}},
				{Key: "assignment", Name: "Human assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{"approval_method": "github_assignment"}},
				{Key: "schedule", Name: "GitHub inbox schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Poll assigned issues.", "category": "scheduled", "priority": 2, "run_at": "04:15", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
				{Key: "inbox", Name: "GitHub inbox", Type: models.AutomationNodeAgentTask, Role: "github_inbox", Config: map[string]any{"prompt": "Process assigned issues.", "category": "backlog", "priority": 2}},
				{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the accepted issue.", "category": "active", "priority": 2}},
				{Key: "open_pr", Name: "Open pull request", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open a reviewable pull request linked to the source issue.", "base": "release/v2", "draft": true}},
				{Key: "review", Name: "Human review", Type: models.AutomationNodeHumanGate, Role: "pull_request_review", Config: map[string]any{"approval_method": "pull_request_review"}},
				{Key: "complete", Name: "Completed", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
			},
			Edges: []models.AutomationDraftEdge{
				{Key: "producer_issue", From: "producer", To: "issue", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
				{Key: "issue_assignment", From: "issue", To: "assignment", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
				{Key: "schedule_inbox", From: "schedule", To: "inbox", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
				{Key: "assignment_inbox", From: "assignment", To: "inbox", FromPort: "right", ToPort: "left", Label: "assigned", Condition: map[string]any{"state": "assigned"}},
				{Key: "inbox_implementation", From: "inbox", To: "implementation", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
				{Key: "implementation_pr", From: "implementation", To: "open_pr", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
				{Key: "pr_review", From: "open_pr", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
				{Key: "review_complete", From: "review", To: "complete", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			}}
		activated := activate(t, h, candidate, "schedule", "inbox")
		require.Contains(t, activated.Prompt, "Pull request handoff")
		require.Contains(t, activated.Prompt, "github_open_pull_request")
		require.Contains(t, activated.Prompt, `base "release/v2"`)
		require.Contains(t, activated.Prompt, "draft=true")
	})

	t.Run("notification handoff", func(t *testing.T) {
		h := newAutomationSaveStoreHarness(t, "Custom notification activation")
		candidate := customScheduledTaskCandidate("Custom notification activation", "Start the notification flow.")
		candidate.Nodes = append(candidate.Nodes,
			models.AutomationDraftNode{Key: "notify", Name: "Create notification", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Ask a human to review the prepared proposal."}},
			models.AutomationDraftNode{Key: "approval", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
			models.AutomationDraftNode{Key: "complete", Name: "Completed", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		)
		candidate.Edges = append(candidate.Edges,
			models.AutomationDraftEdge{Key: "followup_notify", From: "followup", To: "notify", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			models.AutomationDraftEdge{Key: "notify_approval", From: "notify", To: "approval", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
			models.AutomationDraftEdge{Key: "approval_complete", From: "approval", To: "complete", FromPort: "right", ToPort: "left", Condition: map[string]any{"state": "approved"}},
		)
		activated := activate(t, h, candidate, "schedule", "followup")
		require.Contains(t, activated.Prompt, "Human approval handoff")
		require.Contains(t, activated.Prompt, "create_notification exactly once")
		require.NotContains(t, activated.Prompt, "Pull request handoff")
		require.NotContains(t, activated.Prompt, "github_open_pull_request")
	})

	t.Run("task only", func(t *testing.T) {
		h := newAutomationSaveStoreHarness(t, "Custom task-only activation")
		candidate := customScheduledTaskCandidate("Custom task-only activation", "Start the task-only flow.")
		activated := activate(t, h, candidate, "schedule", "followup")
		require.NotContains(t, activated.Prompt, "Human approval handoff")
		require.NotContains(t, activated.Prompt, "create_notification exactly once")
		require.NotContains(t, activated.Prompt, "Pull request handoff")
		require.NotContains(t, activated.Prompt, "github_open_pull_request")
	})
}

func TestAutomationCustomTaskFanoutContinuesAfterBusyBranch(t *testing.T) {
	for _, test := range []struct {
		name         string
		busyNodeKey  string
		busyStatus   models.TaskStatus
		eligibleNode string
	}{
		{name: "busy first", busyNodeKey: "first", busyStatus: models.StatusQueued, eligibleNode: "second"},
		{name: "busy last", busyNodeKey: "second", busyStatus: models.StatusRunning, eligibleNode: "first"},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newAutomationSaveStoreHarness(t, "Independent fanout "+test.name)
			ctx := context.Background()
			candidate := models.AutomationDraftCandidate{
				SchemaVersion: 1, Name: "Independent fanout", Description: "Activate every eligible child",
				AutomationType: "custom", AdapterKey: AutomationAdapterCustom,
				Nodes: []models.AutomationDraftNode{
					{Key: "parent", Name: "Parent", Type: models.AutomationNodeAgentTask, Role: "task",
						Config: map[string]any{"prompt": "Produce the shared result.", "category": "active", "priority": 2}},
					{Key: "first", Name: "First child", Type: models.AutomationNodeAgentTask, Role: "task",
						Config: map[string]any{"prompt": "Handle the first branch.", "category": "active", "priority": 2}},
					{Key: "second", Name: "Second child", Type: models.AutomationNodeAgentTask, Role: "task",
						Config: map[string]any{"prompt": "Handle the second branch.", "category": "active", "priority": 2}},
				},
				Edges: []models.AutomationDraftEdge{
					{Key: "parent_first", From: "parent", To: "first", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
					{Key: "parent_second", From: "parent", To: "second", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
				},
			}
			saved, err := h.compiler.Save(ctx, AutomationSaveRequest{
				ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
			})
			require.NoError(t, err)
			parent, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, "parent", "task"))
			require.NoError(t, err)
			busy, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, test.busyNodeKey, "task"))
			require.NoError(t, err)
			eligible, err := h.taskRepo.GetByID(ctx, automationResourceID(t, saved.Definition, test.eligibleNode, "task"))
			require.NoError(t, err)
			require.NoError(t, h.taskRepo.UpdateStatus(ctx, parent.ID, models.StatusRunning))
			require.NoError(t, h.taskRepo.UpdateStatus(ctx, busy.ID, test.busyStatus))

			execution := models.Execution{TaskID: parent.ID, Status: models.ExecRunning, PromptSent: parent.Prompt}
			require.NoError(t, h.stores.Executions.Create(ctx, &execution))
			sourceNode := automationNodeByKey(t, saved.Definition, "parent")
			binding := models.AutomationBinding{AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, NodeID: sourceNode.ID}
			causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{binding}})
			causalCtx = withAutomationExecution(causalCtx, parent.ID, execution.ID)

			worker := NewWorkerService(nil, 1, h.stores.Projects)
			worker.SetTaskRepo(h.taskRepo)
			taskSvc := NewTaskService(h.taskRepo, h.stores.Attachments, worker)
			llmSvc := &LLMService{taskRepo: h.taskRepo, automationRepo: h.automationRepo, taskSvc: taskSvc}
			handled, err := llmSvc.activatePublishedCustomAutomationChild(causalCtx, *parent, "shared parent result\n[STATUS: SUCCESS]")
			require.True(t, handled)
			require.ErrorIs(t, err, persistence.ErrAutomationChainChildBusy)

			unchangedBusy, err := h.taskRepo.GetByID(ctx, busy.ID)
			require.NoError(t, err)
			require.Equal(t, test.busyStatus, unchangedBusy.Status)
			activated, err := h.taskRepo.GetByID(ctx, eligible.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryActive, activated.Category)
			require.Equal(t, models.StatusPending, activated.Status)
			require.Contains(t, activated.Prompt, "shared parent result")

			select {
			case submitted := <-worker.Submitted():
				require.Equal(t, eligible.ID, submitted.ID)
			default:
				t.Fatal("eligible fanout child was not submitted")
			}
			blockedTransitions := countAutomationHandoffs(t, h, saved.Definition, sourceNode.ID, automationNodeByKey(t, saved.Definition, test.busyNodeKey).ID, models.AutomationTransitionBlocked)
			require.Equal(t, 1, blockedTransitions)

			handled, err = llmSvc.activatePublishedCustomAutomationChild(causalCtx, *parent, "shared parent result\n[STATUS: SUCCESS]")
			require.True(t, handled)
			require.ErrorIs(t, err, persistence.ErrAutomationChainChildBusy)
			select {
			case duplicate := <-worker.Submitted():
				t.Fatalf("fanout retry submitted child %s more than once", duplicate.ID)
			default:
			}
			require.Equal(t, 1, countAutomationHandoffs(t, h, saved.Definition, sourceNode.ID, automationNodeByKey(t, saved.Definition, test.busyNodeKey).ID, models.AutomationTransitionBlocked))
		})
	}
}

func TestAutomationInactiveLifecycleDefersCompiledChildHandoff(t *testing.T) {
	for _, state := range []models.AutomationLifecycleState{models.AutomationPaused, models.AutomationArchived} {
		t.Run(string(state), func(t *testing.T) {
			h := newAutomationSaveStoreHarness(t, "Deferred child handoff "+string(state))
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
			require.NoError(t, h.stores.Executions.Create(ctx, &execution))
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
			taskSvc := NewTaskService(h.taskRepo, h.stores.Attachments, nil)
			llmSvc := &LLMService{taskRepo: h.taskRepo, automationRepo: h.automationRepo, taskSvc: taskSvc}
			require.NoError(t, llmSvc.activateCompiledAutomationChild(causalCtx, *parent, "parent result\n[STATUS: SUCCESS]", config))

			deferred, err := h.taskRepo.GetByID(ctx, child.ID)
			require.NoError(t, err)
			require.Equal(t, models.CategoryBacklog, deferred.Category)
			require.Equal(t, models.StatusPending, deferred.Status)
			require.Contains(t, deferred.Prompt, "parent result")
			enteredTransitions := countAutomationHandoffs(t, h, saved.Definition, sourceNode.ID, automationNodeByKey(t, saved.Definition, "downstream").ID, models.AutomationTransitionEntered)
			require.Equal(t, 1, enteredTransitions, "inactive handoff must retain its causal transition for Resume")
			followupNode := automationNodeByKey(t, saved.Definition, "downstream")
			current, err := h.automationRepo.GetDefinitionVersion(ctx, h.project.ID, saved.Definition.Automation.ID, saved.Definition.Version.ID)
			require.NoError(t, err)
			require.NotNil(t, current)
			resumableChildren := 0
			for _, resource := range current.Resources {
				if resource.NodeID != followupNode.ID || resource.ResourceType != "task" {
					continue
				}
				task, err := h.taskRepo.GetByID(ctx, resource.ResourceID)
				require.NoError(t, err)
				if task != nil && task.ProjectID == h.project.ID && task.Category == models.CategoryBacklog && task.Status == models.StatusPending && task.ParentTaskID != nil && *task.ParentTaskID == parent.ID && countAutomationHandoffs(t, h, current, sourceNode.ID, followupNode.ID, models.AutomationTransitionEntered) > 0 {
					resumableChildren++
				}
			}

			require.Equal(t, 1, resumableChildren, "deferred child must remain eligible through the exact current graph handoff")
			metadata, err := h.automationRepo.GetAutomationGraphMetadata(ctx, h.project.ID, saved.Definition.Automation.ID, saved.Definition.Version.ID)
			require.NoError(t, err)
			require.NotNil(t, metadata)
			var savedCandidate models.AutomationDraftCandidate
			require.NoError(t, json.Unmarshal([]byte(metadata.CandidateJSON), &savedCandidate))
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
			h := newAutomationSaveStoreHarness(t, "Post-activation handoff "+string(state))
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
			require.NoError(t, h.stores.Executions.Create(ctx, &execution))
			sourceNode := automationNodeByKey(t, saved.Definition, "followup")
			binding := models.AutomationBinding{AutomationID: saved.Definition.Automation.ID, VersionID: saved.Definition.Version.ID, InvocationID: invocation.ID, NodeID: sourceNode.ID}
			causalCtx := WithAutomationContext(ctx, models.AutomationContext{ProjectID: h.project.ID, Bindings: []models.AutomationBinding{binding}})
			causalCtx = withAutomationExecution(causalCtx, parent.ID, execution.ID)
			config, err := parent.ParseChainConfig()
			require.NoError(t, err)

			worker := NewWorkerService(nil, 1, h.stores.Projects)
			worker.SetTaskRepo(h.taskRepo)
			taskSvc := NewTaskService(h.taskRepo, h.stores.Attachments, worker)
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

func TestAutomationDeleteRemovesOwnedScheduleAndTriggerTask(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Atomic delete")
	ctx := context.Background()
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	saved, err := h.compiler.Save(ctx, AutomationSaveRequest{ProjectID: h.project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	taskID := automationResourceID(t, saved.Definition, "vision_suggestions", "task")
	scheduleID := automationResourceID(t, saved.Definition, "vision_suggestions", "schedule")
	require.NoError(t, h.lifecycle.Delete(ctx, h.project.ID, saved.Definition.Automation.ID))
	task, err := h.taskRepo.GetByID(ctx, taskID)
	require.NoError(t, err)
	require.Nil(t, task)
	schedule, err := h.scheduleRepo.GetByID(ctx, scheduleID)
	require.NoError(t, err)
	require.Nil(t, schedule)
}

func TestAutomationSavePreviewRejectsUnavailableGitHubIntegrationWithoutPersistence(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Atomic GitHub validation")
	candidate, err := h.drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	plan, _, err := h.compiler.PreviewSave(context.Background(), h.project.ID, candidate)
	require.NoError(t, err)
	require.NotEmpty(t, plan.Validation)
	require.Zero(t, automationStateForTest(t, h.stores).Automations)
	require.Zero(t, automationStateForTest(t, h.stores).Tasks)
	require.Zero(t, automationStateForTest(t, h.stores).Schedules)
}

func TestAutomationSaveValidationIssueAppearsInPreviewAndBlocksPersistence(t *testing.T) {
	h := newAutomationSaveStoreHarness(t, "Shared save validation")
	candidate := customScheduledTaskCandidate("Shared save validation", "Review one request.")
	candidate.Nodes[0].Config["agent_ref"] = " missing-agent "

	plan, normalized, err := h.compiler.PreviewSave(context.Background(), h.project.ID, candidate)
	require.NoError(t, err)
	require.Equal(t, "missing-agent", normalized.Nodes[0].Config["agent_ref"], "preview must still return the normalized candidate")
	require.Contains(t, issueCodes(plan.Validation), "agent_ref")

	_, err = h.compiler.Save(context.Background(), AutomationSaveRequest{
		ProjectID: h.project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate,
	})
	require.ErrorContains(t, err, "automation graph validation failed: Agent selection cannot be resolved because project capabilities are unavailable.")
	require.Zero(t, automationStateForTest(t, h.stores).Automations)
	require.Zero(t, automationStateForTest(t, h.stores).Resources)
	require.Zero(t, automationStateForTest(t, h.stores).Tasks)
	require.Zero(t, automationStateForTest(t, h.stores).Schedules)
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

func automationStateForTest(t *testing.T, env *teststores.Env) storefixture.AutomationStateCounts {
	t.Helper()
	state, err := env.Fixtures.AutomationState(t.Context())
	require.NoError(t, err)
	return state
}

func newAutomationSaveStoreHarnessWithOptions(t *testing.T, opts []teststores.Option, name string) automationSaveHarness {
	t.Helper()
	return buildAutomationSaveHarness(t, teststores.New(t, opts...), name)
}

// countAutomationHandoffs reads every history page so a duplicate cannot hide beyond the first page.
func countAutomationHandoffs(t *testing.T, h automationSaveHarness, definition *models.AutomationDefinition, from, to string, state models.AutomationTransitionState) int {
	t.Helper()
	count := 0
	cursor := ""
	for {
		page, err := h.automationRepo.ListAutomationTransitions(t.Context(), h.project.ID, definition.Automation.ID, "", "", 100, cursor)
		require.NoError(t, err)
		for _, transition := range page.Items {
			if transition.VersionID == definition.Version.ID && transition.FromNodeID == from && transition.ToNodeID == to && transition.State == state {
				count++
			}
		}
		if page.NextCursor == "" {
			return count
		}
		require.NotEqual(t, cursor, page.NextCursor)
		cursor = page.NextCursor
	}
}
