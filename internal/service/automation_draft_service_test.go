package service

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestMaintainedSDLCTemplatesKeepDiscoveryParityAndSchedulesOwnTheirTasks(t *testing.T) {
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(nil, registry)
	discoveryRoles := []string{"offering_manager", "bug_finder", "optimization_finder", "redundancy_finder"}

	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
		adapter, ok := registry.Get(adapterKey)
		require.True(t, ok)
		for _, role := range append(discoveryRoles, "loop_auditor") {
			node := automationAdapterNodeByRole(t, adapter, role)
			require.Equal(t, "trigger", node.Type, "%s/%s must be represented as one Schedule node", adapterKey, role)
			require.True(t, node.AllowedResources["task"], "%s/%s Schedule must own its Task", adapterKey, role)
			require.True(t, node.AllowedResources["schedule"], "%s/%s Schedule must own its Scheduler row", adapterKey, role)
		}

		candidate, err := drafts.TemplateCandidate(adapterKey)
		require.NoError(t, err)
		require.Empty(t, drafts.ValidateCandidate(candidate))
		for _, node := range adapter.Nodes {
			if !node.AllowedResources["schedule"] {
				continue
			}
			require.True(t, node.AllowedResources["task"], "%s/%s must not be an empty scheduler relay", adapterKey, node.Key)
			require.Equal(t, node.Key, adapterScheduleTarget(adapter, node.Key), "%s/%s scheduler must target its own Task", adapterKey, node.Key)
			draftNode := automationDraftNodeByKey(t, candidate, node.Key)
			require.Equal(t, node.Key, draftNode.Config["target_node_key"])
			require.Equal(t, string(models.CategoryScheduled), draftNode.Config["category"])
			require.NotEmpty(t, draftNode.Config["prompt"])
		}
	}
}

func TestGitHubSDLCTemplateUsesShippedBootstrapSkillPrompts(t *testing.T) {
	shipped := shippedGitHubBootstrapPromptsForTest(t)
	candidate, err := NewAutomationDraftService(nil, NewAutomationAdapterRegistry()).TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)

	rolesByNode := map[string]string{
		"vision_suggestions":  "offering_manager",
		"bug_finder":          "finder",
		"optimization_finder": "finder",
		"redundancy_finder":   "finder",
		"dev_inbox":           "dev_inbox",
		"auditor":             "loop_auditor",
	}
	expectedCadence := map[string]struct {
		repeatType string
		interval   int
	}{
		"vision_suggestions":  {repeatType: string(models.RepeatDaily), interval: 1},
		"bug_finder":          {repeatType: string(models.RepeatDaily), interval: 1},
		"optimization_finder": {repeatType: string(models.RepeatDaily), interval: 1},
		"redundancy_finder":   {repeatType: string(models.RepeatDaily), interval: 1},
		"dev_inbox":           {repeatType: string(models.RepeatHours), interval: 1},
		"auditor":             {repeatType: string(models.RepeatWeekly), interval: 1},
	}
	for nodeKey, role := range rolesByNode {
		node := automationDraftNodeByKey(t, candidate, nodeKey)
		require.Equal(t, shipped[role], node.Config["prompt"], "%s must use the prompt shipped in the bootstrap skill", nodeKey)
		require.Equal(t, shipped[role], automationCompiledTaskPrompt(candidate, node), "%s must persist the exact shipped prompt without custom-graph additions", nodeKey)
		require.NotContains(t, node.Config["prompt"], "references/dev-inbox-execution-invariants.md")
		require.NotContains(t, node.Config["prompt"], "repository-wide current issue listing or search")
		require.Equal(t, expectedCadence[nodeKey].repeatType, node.Config["repeat_type"], "%s cadence must match bootstrap setup", nodeKey)
		require.EqualValues(t, expectedCadence[nodeKey].interval, node.Config["repeat_interval"])
	}
}

func shippedGitHubBootstrapPromptsForTest(t *testing.T) map[string]string {
	t.Helper()
	path := filepath.Join("..", "builtinskills", "builtin", "skills", "openvibely_github_autonomous_sdlc_bootstrap", "SKILL.md")
	data, err := os.ReadFile(path)
	require.NoError(t, err, "read the GitHub bootstrap skill shipped by OpenVibely")
	body := string(data)

	extractFence := func(heading string) string {
		t.Helper()
		marker := "## " + heading + "\n"
		start := strings.Index(body, marker)
		require.NotEqual(t, -1, start, "missing shipped skill section %s", heading)
		fenceStart := strings.Index(body[start+len(marker):], "```text\n")
		require.NotEqual(t, -1, fenceStart, "missing shipped prompt fence for %s", heading)
		promptStart := start + len(marker) + fenceStart + len("```text\n")
		fenceEnd := strings.Index(body[promptStart:], "\n```")
		require.NotEqual(t, -1, fenceEnd, "missing shipped prompt closing fence for %s", heading)
		return strings.TrimSpace(body[promptStart : promptStart+fenceEnd])
	}

	const auditorPrefix = "- `GitHub Loop Auditor`, weekly. "
	auditorStart := strings.Index(body, auditorPrefix)
	require.NotEqual(t, -1, auditorStart, "missing shipped Loop Auditor task description")
	auditorStart += len(auditorPrefix)
	auditorEnd := strings.IndexByte(body[auditorStart:], '\n')
	require.NotEqual(t, -1, auditorEnd, "unterminated shipped Loop Auditor task description")

	return map[string]string{
		"dev_inbox":        extractFence("Prompt Pattern For Dev Inbox"),
		"offering_manager": extractFence("Prompt Pattern For Offering Manager"),
		"finder":           extractFence("Prompt Pattern For Bug / Optimization / Redundancy Finders"),
		"loop_auditor":     strings.TrimSpace(body[auditorStart : auditorStart+auditorEnd]),
	}
}

func TestMaintainedTemplatesLeaveVisibleConnectorRunway(t *testing.T) {
	registry := NewAutomationAdapterRegistry()
	const minimumStageSpacing = 220.0 // 170-unit cards plus 50 units of visible connector.

	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC, AutomationAdapterVisionDriver} {
		adapter, ok := registry.Get(adapterKey)
		require.True(t, ok)
		nodes := make(map[string]AutomationAdapterNode, len(adapter.Nodes))
		for _, node := range adapter.Nodes {
			nodes[node.Key] = node
		}
		for _, edge := range adapter.Edges {
			source, sourceOK := nodes[edge.From]
			target, targetOK := nodes[edge.To]
			require.True(t, sourceOK, "%s edge %s source must exist", adapterKey, edge.Key)
			require.True(t, targetOK, "%s edge %s target must exist", adapterKey, edge.Key)
			require.GreaterOrEqual(t, target.X-source.X, minimumStageSpacing,
				"%s edge %s must leave a visible line between its node cards", adapterKey, edge.Key)
		}
	}
}

func TestVisionDriverTemplateScheduleOwnsItsTask(t *testing.T) {
	registry := NewAutomationAdapterRegistry()
	adapter, ok := registry.Get(AutomationAdapterVisionDriver)
	require.True(t, ok)
	driver := automationAdapterNodeByRole(t, adapter, "vision_driver")
	require.Equal(t, "trigger", driver.Type)
	require.True(t, driver.AllowedResources["task"])
	require.True(t, driver.AllowedResources["schedule"])
	require.Equal(t, driver.Key, adapterScheduleTarget(adapter, driver.Key))

	candidate, err := NewAutomationDraftService(nil, registry).TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	require.Len(t, candidate.Nodes, 5, "Vision Driver must not add a separate empty schedule relay node")
}

func automationAdapterNodeByRole(t *testing.T, adapter AutomationAdapter, role string) AutomationAdapterNode {
	t.Helper()
	for _, node := range adapter.Nodes {
		if node.Role == role {
			return node
		}
	}
	t.Fatalf("adapter %s has no %s role", adapter.Key, role)
	return AutomationAdapterNode{}
}

func automationDraftNodeByKey(t *testing.T, candidate models.AutomationDraftCandidate, key string) models.AutomationDraftNode {
	t.Helper()
	for _, node := range candidate.Nodes {
		if node.Key == key {
			return node
		}
	}
	t.Fatalf("candidate has no %s node", key)
	return models.AutomationDraftNode{}
}

func TestAutomationDraftServiceNormalizesRegisteredTemplatesDeterministically(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())

	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC, AutomationAdapterVisionDriver} {
		first, err := svc.TemplateCandidate(adapterKey)
		require.NoError(t, err)
		second, err := svc.NormalizeCandidate(first)
		require.NoError(t, err)
		require.Equal(t, first, second)
		require.Empty(t, svc.ValidateCandidate(second))
		for _, node := range second.Nodes {
			require.NotNil(t, node.Position, "layout must be server-owned for %s/%s", adapterKey, node.Key)
		}
	}
}

func TestAutomationBlankDraftStartsEmptyAndPersistsUserLayout(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Visual blank")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())

	blank, err := svc.BlankCandidate("")
	require.NoError(t, err)
	require.Equal(t, AutomationAdapterCustom, blank.AdapterKey)
	require.Empty(t, blank.Nodes, "Blank must start with an empty custom canvas")
	require.Empty(t, blank.Edges)
	require.Contains(t, issueCodes(svc.ValidateCandidate(blank)), "empty_graph")

	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: blank})
	require.NoError(t, err)
	require.Empty(t, created.Candidate.Nodes)
	require.NotEmpty(t, created.ValidationErrors)

	dragged := models.AutomationDraftNode{Key: "my_schedule", Name: "My schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}, Position: &models.AutomationDraftPoint{X: 37, Y: 83}}
	created.Candidate.Nodes = append(created.Candidate.Nodes, dragged)
	updated, err := svc.UpdateDraft(context.Background(), created.Definition.Automation.ID, created.Definition.Version.ID, project.ID, created.Candidate)
	require.NoError(t, err)
	require.Equal(t, &models.AutomationDraftPoint{X: 37, Y: 83}, updated.Candidate.Nodes[0].Position, "normalization must preserve user-positioned nodes")
}

func TestCustomAutomationValidatesComposableTaskHandoffsAndRejectsUnsupportedJoinsOrCycles(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "research", Name: "Research", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Research the request.", "category": "backlog", "priority": 2}},
		{Key: "implement", Name: "Implement", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the researched request.", "category": "active", "priority": 2}},
		{Key: "done", Name: "Done", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_research", From: "schedule", To: "research", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "research_implement", From: "research", To: "implement", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "implement_done", From: "implement", To: "done", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}

	require.Empty(t, svc.ValidateCandidate(candidate), "a linear Schedule → Agent task → Agent task → Outcome path must publish")

	scheduleOnly := candidate
	scheduleOnly.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes[:1]...)
	scheduleOnly.Edges = nil
	require.Empty(t, svc.ValidateCandidate(scheduleOnly), "a Schedule is itself a runnable scheduled task and does not require an Agent Task connection")

	scheduledAgent := candidate
	scheduledAgent.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
	for i := range scheduledAgent.Nodes {
		if scheduledAgent.Nodes[i].Key == "research" {
			scheduledAgent.Nodes[i].Config = map[string]any{"prompt": "Research the request.", "category": "scheduled", "priority": 2}
		}
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(scheduledAgent)), "category", "Agent Task nodes are ordinary tasks and must reject the scheduled category")

	augmentedAgent := candidate
	augmentedAgent.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
	for i := range augmentedAgent.Nodes {
		if augmentedAgent.Nodes[i].Key == "research" {
			augmentedAgent.Nodes[i].Config = map[string]any{"prompt": "Research the request.", "category": "backlog", "priority": 2, "skills": []any{"researcher:review"}, "source_files": []any{"README.md"}}
		}
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(augmentedAgent)), "unknown_config", "ordinary Agent Task nodes must reject Agent-only Skills and Source files")

	branch := candidate
	branch.Nodes = append(append([]models.AutomationDraftNode(nil), candidate.Nodes...), models.AutomationDraftNode{
		Key: "review", Name: "Review", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Review the implementation.", "category": "backlog", "priority": 2},
	})
	branch.Edges = append(append([]models.AutomationDraftEdge(nil), candidate.Edges...), models.AutomationDraftEdge{
		Key: "research_review", From: "research", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{},
	})
	require.Empty(t, svc.ValidateCandidate(branch), "one completed task may fan out to multiple ordinary tasks through the existing task-chain machinery")

	ambiguousOutcome := candidate
	ambiguousOutcome.Nodes = append(append([]models.AutomationDraftNode(nil), candidate.Nodes...), models.AutomationDraftNode{
		Key: "also_done", Name: "Also done", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{},
	})
	ambiguousOutcome.Edges = append(append([]models.AutomationDraftEdge(nil), candidate.Edges...), models.AutomationDraftEdge{
		Key: "implement_also_done", From: "implement", To: "also_done", FromPort: "right", ToPort: "left", Condition: map[string]any{},
	})
	require.Contains(t, issueCodes(svc.ValidateCandidate(ambiguousOutcome)), "ambiguous_handoff", "a task must not publish duplicate same-kind targets that the existing runtime cannot distinguish")

	standaloneTask := candidate
	standaloneTask.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes[1:2]...)
	standaloneTask.Edges = nil
	require.Empty(t, svc.ValidateCandidate(standaloneTask), "an ordinary task is a valid independently runnable Automation resource")

	cycle := candidate
	cycle.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_research", From: "schedule", To: "research", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "research_implement", From: "research", To: "implement", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "implement_research", From: "implement", To: "research", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(cycle)), "unsupported_cycle")

	multipleParents := candidate
	multipleParents.Nodes = append(append([]models.AutomationDraftNode(nil), candidate.Nodes...), models.AutomationDraftNode{
		Key: "second_schedule", Name: "Second schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule",
		Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "10:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true},
	})
	multipleParents.Edges = append(append([]models.AutomationDraftEdge(nil), candidate.Edges...), models.AutomationDraftEdge{
		Key: "second_implement", From: "second_schedule", To: "implement", FromPort: "right", ToPort: "left", Condition: map[string]any{},
	})
	require.Contains(t, issueCodes(svc.ValidateCandidate(multipleParents)), "task_parents")
}

func TestCustomAutomationValidatesNativeAlertApprovalHandoffsAndRejectsAnalogousUnsafeBranches(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Daily review", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "review", Name: "Review changes", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Review likely changes.", "category": "backlog", "priority": 2}},
		{Key: "request", Name: "Request approval", Type: models.AutomationNodeAction, Role: "create_notification", Config: map[string]any{"notification_type": "change_proposal", "instructions": "Summarize the proposed change for a human reviewer."}},
		{Key: "human", Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval", Config: map[string]any{"approval_method": "native_alert"}},
		{Key: "accepted", Name: "Accepted", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
		{Key: "declined", Name: "Declined", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_review", From: "schedule", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "review_request", From: "review", To: "request", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "request_human", From: "request", To: "human", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "human_accepted", From: "human", To: "accepted", FromPort: "right", ToPort: "left", Label: "approved", Condition: map[string]any{"state": "approved"}},
		{Key: "human_declined", From: "human", To: "declined", FromPort: "right", ToPort: "left", Label: "rejected", Condition: map[string]any{"state": "rejected"}},
	}

	require.Empty(t, svc.ValidateCandidate(candidate), "a custom native Alert approval path must be publishable")

	multiTask := candidate
	multiTask.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
	multiTask.Nodes = append(multiTask.Nodes, models.AutomationDraftNode{
		Key: "research", Name: "Research first", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Research likely changes.", "category": "backlog", "priority": 2},
	})
	for i := range multiTask.Nodes {
		if multiTask.Nodes[i].Key == "review" {
			multiTask.Nodes[i].Config = map[string]any{"prompt": "Review likely changes.", "category": "active", "priority": 2}
		}
	}
	multiTask.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	multiTask.Edges[0].To = "research"
	multiTask.Edges = append(multiTask.Edges, models.AutomationDraftEdge{Key: "research_review", From: "research", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}})
	require.Empty(t, svc.ValidateCandidate(multiTask), "native approval must compose after an existing task-to-task handoff")

	missingRejected := candidate
	missingRejected.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges[:len(candidate.Edges)-1]...)
	require.Empty(t, svc.ValidateCandidate(missingRejected), "users may observe only the approval result they care about")

	terminalGate := missingRejected
	terminalGate.Nodes = []models.AutomationDraftNode{candidate.Nodes[0], candidate.Nodes[2], candidate.Nodes[3]}
	terminalGate.Edges = []models.AutomationDraftEdge{
		{Key: "schedule_request", From: "schedule", To: "request", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "request_human", From: "request", To: "human", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}
	require.Empty(t, svc.ValidateCandidate(terminalGate), "a Schedule is a task and may create a notification whose approval gate is terminal")

	sharedAction := terminalGate
	sharedAction.Nodes = append(sharedAction.Nodes, models.AutomationDraftNode{
		Key: "manual_review", Name: "Manual review", Type: models.AutomationNodeAgentTask, Role: "task",
		Config: map[string]any{"prompt": "Review on demand.", "category": "active", "priority": 2},
	})
	sharedAction.Edges = append(sharedAction.Edges, models.AutomationDraftEdge{Key: "manual_request", From: "manual_review", To: "request", FromPort: "right", ToPort: "left", Condition: map[string]any{}})
	require.Empty(t, svc.ValidateCandidate(sharedAction), "a real action capability may be reused by multiple valid task producers")

	duplicateApproved := candidate
	duplicateApproved.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	duplicateApproved.Edges[len(duplicateApproved.Edges)-1].Condition = map[string]any{"state": "approved"}
	require.Contains(t, issueCodes(svc.ValidateCandidate(duplicateApproved)), "approval_branches")

	unsafeTarget := candidate
	unsafeTarget.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	unsafeTarget.Edges[len(unsafeTarget.Edges)-1].To = "review"
	require.Contains(t, issueCodes(svc.ValidateCandidate(unsafeTarget)), "unsupported_handoff")

	unsupportedCondition := candidate
	unsupportedCondition.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	unsupportedCondition.Edges[1].Condition = map[string]any{"state": "approved"}
	require.Contains(t, issueCodes(svc.ValidateCandidate(unsupportedCondition)), "unsupported_condition")
}

func TestCustomAutomationValidatesGitHubHandoffsAndRejectsHumanBoundaryBypasses(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "producer_schedule", Name: "Daily suggestions", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "producer", Name: "Find improvements", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Find one focused improvement.", "category": "backlog", "priority": 2}},
		{Key: "issue", Name: "Create issue", Type: models.AutomationNodeAction, Role: "create_github_issue", Config: map[string]any{"instructions": "Open one reviewable suggestion issue.", "labels": []any{"suggestion"}}},
		{Key: "assignment", Name: "Human assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{"approval_method": "github_assignment"}},
		{Key: "inbox_schedule", Name: "Hourly inbox", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:15", "repeat_type": "hours", "repeat_interval": 1, "enabled": true}},
		{Key: "inbox", Name: "Process assigned issues", Type: models.AutomationNodeAgentTask, Role: "github_inbox", Config: map[string]any{"prompt": "Process newly assigned issues.", "category": "backlog", "priority": 3}},
		{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the accepted issue and run relevant validation.", "category": "active", "priority": 3}},
		{Key: "open_pr", Name: "Open pull request", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open a reviewable pull request linked to the source issue.", "base": "main", "draft": false}},
		{Key: "review", Name: "Human review", Type: models.AutomationNodeHumanGate, Role: "pull_request_review", Config: map[string]any{"approval_method": "pull_request_review"}},
		{Key: "complete", Name: "Merged", Type: models.AutomationNodeOutcome, Role: "completed", Config: map[string]any{}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "producer_schedule_to_producer", From: "producer_schedule", To: "producer", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "producer_to_issue", From: "producer", To: "issue", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "issue_to_assignment", From: "issue", To: "assignment", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "inbox_schedule_to_inbox", From: "inbox_schedule", To: "inbox", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "assignment_to_inbox", From: "assignment", To: "inbox", FromPort: "right", ToPort: "left", Label: "assigned", Condition: map[string]any{"state": "assigned"}},
		{Key: "inbox_to_implementation", From: "inbox", To: "implementation", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "implementation_to_pr", From: "implementation", To: "open_pr", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "pr_to_review", From: "open_pr", To: "review", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "review_to_complete", From: "review", To: "complete", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
	}

	require.Empty(t, svc.ValidateCandidate(candidate), "the GitHub graph must map to the existing assignment, inbox, task, PR, and review machinery")

	missingAssignment := candidate
	missingAssignment.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	missingAssignment.Edges[1].To = "inbox"
	require.Contains(t, issueCodes(svc.ValidateCandidate(missingAssignment)), "unsupported_handoff", "a producer must not bypass human assignment")

	autoAssigned := candidate
	autoAssigned.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
	for i := range autoAssigned.Nodes {
		if autoAssigned.Nodes[i].Key == "issue" {
			autoAssigned.Nodes[i].Config = map[string]any{"instructions": "Open one issue.", "labels": []any{"suggestion"}, "assignees": []any{"bot"}}
		}
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(autoAssigned)), "unknown_config", "issue actions must not assign around the human gate")

	wrongGateResult := candidate
	wrongGateResult.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	wrongGateResult.Edges[4].Condition = map[string]any{"state": "approved"}
	require.Contains(t, issueCodes(svc.ValidateCandidate(wrongGateResult)), "unsupported_condition")

	reviewBypass := candidate
	reviewBypass.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	reviewBypass.Edges[6].To = "complete"
	require.Contains(t, issueCodes(svc.ValidateCandidate(reviewBypass)), "github_task_connections", "GitHub issue work must retain the pull request and human review boundary")
}

func TestAutomationDraftNormalizesAndValidatesDirectionalPorts(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	require.NotEmpty(t, candidate.Edges)
	for _, edge := range candidate.Edges {
		require.Equal(t, "right", edge.FromPort, "template edges must leave an output port")
		require.Equal(t, "left", edge.ToPort, "template edges must enter an input port")
	}

	candidate.Edges[0].FromPort = "left"
	candidate.Edges[0].ToPort = "right"
	require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "invalid_edge",
		"explicit input-to-output geometry must fail strict validation")

	normalized, err := svc.NormalizeCandidate(candidate)
	require.NoError(t, err)
	require.Equal(t, "left", normalized.Edges[0].FromPort,
		"strict normalization must preserve an explicitly reversed source port for validation")
	require.Equal(t, "right", normalized.Edges[0].ToPort,
		"strict normalization must preserve an explicitly reversed target port for validation")
	require.Contains(t, issueCodes(svc.ValidateCandidate(normalized)), "invalid_edge")

	missing := candidate
	missing.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
	missing.Edges[0].FromPort = ""
	missing.Edges[0].ToPort = ""
	normalizedMissing, err := svc.NormalizeCandidate(missing)
	require.NoError(t, err)
	require.Empty(t, normalizedMissing.Edges[0].FromPort,
		"strict normalization must not repair a missing source port on a newly submitted candidate")
	require.Empty(t, normalizedMissing.Edges[0].ToPort,
		"strict normalization must not repair a missing target port on a newly submitted candidate")
	require.Contains(t, issueCodes(svc.ValidateCandidate(normalizedMissing)), "invalid_edge")

	reopened, err := svc.normalizeReopenedCandidate(missing)
	require.NoError(t, err)
	require.Equal(t, "right", reopened.Edges[0].FromPort, "older saved connector metadata migrates only when reopened")
	require.Equal(t, "left", reopened.Edges[0].ToPort, "older saved connector metadata migrates only when reopened")
	require.NotContains(t, issueCodes(svc.ValidateCandidate(reopened)), "invalid_edge")
}

func TestAutomationFreeformDraftPersistsCustomNodesAndCyclesButCannotPublish(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Freeform draft")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "alpha", Name: "Alpha", Type: models.AutomationNodeAgentTask, Role: "custom_agent_task", Config: map[string]any{"prompt": "Do alpha work", "category": "backlog", "priority": 2}, Position: &models.AutomationDraftPoint{X: 0, Y: 0}},
		{Key: "beta", Name: "Beta", Type: models.AutomationNodeCondition, Role: "custom_condition", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 260, Y: 0}},
		{Key: "gamma", Name: "Gamma", Type: models.AutomationNodeAction, Role: "custom_action", Config: map[string]any{}, Position: &models.AutomationDraftPoint{X: 520, Y: 0}},
	}
	candidate.Edges = []models.AutomationDraftEdge{
		{Key: "edge_alpha_beta", From: "alpha", To: "beta", FromPort: "left", ToPort: "right", Condition: map[string]any{}},
		{Key: "edge_beta_gamma", From: "beta", To: "gamma", FromPort: "right", ToPort: "left", Condition: map[string]any{}},
		{Key: "edge_gamma_alpha", From: "gamma", To: "alpha", FromPort: "left", ToPort: "right", Condition: map[string]any{}},
	}

	issues := svc.ValidateCandidate(candidate)
	require.Contains(t, issueCodes(issues), "unsupported_capability", "unsupported capability nodes must remain visibly unpublished")
	require.Contains(t, issueCodes(issues), "invalid_edge", "strict validation must reject the legacy non-directional connector geometry")
	for i := range candidate.Edges {
		candidate.Edges[i].FromPort = "right"
		candidate.Edges[i].ToPort = "left"
	}
	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "manual", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.Len(t, created.Candidate.Nodes, 3)
	require.Len(t, created.Candidate.Edges, 3)
	require.Equal(t, "right", created.Candidate.Edges[0].FromPort)
	require.Equal(t, "left", created.Candidate.Edges[0].ToPort)
	require.NotContains(t, issueCodes(created.ValidationErrors), "invalid_edge", "corrected cycles retain valid directional geometry")
	require.Contains(t, issueCodes(created.ValidationErrors), "unsupported_capability")

	planner := NewAutomationPublicationPlanner(repo, nil, nil, svc.registry, svc)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	require.Contains(t, issueCodes(plan.Validation), "unsupported_capability", "unsupported capability graphs must not publish")
	require.Empty(t, plan.Effects, "unsupported topology must not produce runtime resource mutations")
}

func TestAutomationDraftRejectsMissingAndUnsupportedSchemaVersions(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Schema versions")
	repo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())

	for _, version := range []int{0, 2} {
		candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
		require.NoError(t, err)
		candidate.SchemaVersion = version
		normalized, err := svc.NormalizeCandidate(candidate)
		require.NoError(t, err)
		require.Equal(t, version, normalized.SchemaVersion, "normalization must preserve the supplied schema version")
		require.Contains(t, issueCodes(svc.ValidateCandidate(normalized)), "schema_version")
		_, err = svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
		require.ErrorContains(t, err, "schema version")
	}

	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	candidate.SchemaVersion = 2
	_, err = svc.UpdateDraft(context.Background(), created.Definition.Automation.ID, created.Definition.Version.ID, project.ID, candidate)
	require.ErrorContains(t, err, "schema version")
	unsupportedJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE automation_draft_metadata SET candidate_json = ? WHERE version_id = ?`, string(unsupportedJSON), created.Definition.Version.ID)
	require.NoError(t, err)
	_, err = svc.GetDraft(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.ErrorContains(t, err, "schema version")
	_, err = svc.ClonePublishedVersion(context.Background(), project.ID, created.Definition.Automation.ID)
	require.ErrorContains(t, err, "schema version")
}

func TestAutomationTaskReferencesResolveInsideSelectedProject(t *testing.T) {
	db := testutil.NewTestDB(t)
	ctx := context.Background()
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Reference project")
	project.RepoPath = t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project.RepoPath, "VISION.md"), []byte("vision"), 0o600))
	require.NoError(t, projectRepo.Update(ctx, &project))
	other := automationTestProject(t, projectRepo, "Other reference project")
	agentRepo := repository.NewAgentRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	settingsRepo := repository.NewSettingsRepo(db)
	agent := models.Agent{Name: "Project Architect", Key: "project_architect", Scope: models.AgentScopeProject, ProjectID: project.ID,
		Enabled: true, SelectableAsPrimary: true, Skills: []models.SkillConfig{{Name: "project-guidance", Description: "Guide project work", Content: "safe"}}}
	require.NoError(t, agentRepo.Create(ctx, &agent))
	foreign := models.Agent{Name: "Foreign Architect", Key: "foreign_architect", Scope: models.AgentScopeProject, ProjectID: other.ID,
		Enabled: true, SelectableAsPrimary: true}
	require.NoError(t, agentRepo.Create(ctx, &foreign))

	capabilities := NewAutomationCapabilitySnapshotBuilder(projectRepo, agentRepo, taskRepo, settingsRepo)
	snapshot, err := capabilities.Build(ctx, project.ID)
	require.NoError(t, err)
	require.Contains(t, snapshot.Agents, models.AutomationCapabilityRef{ID: "project_architect", Name: "Project Architect"})
	require.Contains(t, snapshot.Skills, models.AutomationCapabilityRef{ID: "project_architect:project-guidance", Name: "project-guidance"})

	svc := NewAutomationDraftService(repository.NewAutomationRepo(db), NewAutomationAdapterRegistry())
	svc.SetCapabilitySnapshotBuilder(capabilities)
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	driverIndex := automationDraftNodeIndexByKey(t, candidate, "vision_driver")
	candidate.Nodes[driverIndex].Config["agent_ref"] = "project_architect"
	candidate.Nodes[driverIndex].Config["skills"] = []any{"project_architect:project-guidance"}
	candidate.Nodes[driverIndex].Config["source_files"] = []any{"VISION.md"}
	require.Empty(t, svc.ValidateCandidateWithCapabilities(candidate, snapshot))

	candidate.Nodes[driverIndex].Config["agent_ref"] = "foreign_architect"
	issues := svc.ValidateCandidateWithCapabilities(candidate, snapshot)
	require.Contains(t, issueCodes(issues), "agent_ref")
	created, err := svc.CreateDraft(ctx, AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	require.Contains(t, issueCodes(created.ValidationErrors), "agent_ref", "unresolved references must remain visible without being guessed")

	candidate.Nodes[driverIndex].Config["agent_ref"] = "project_architect"
	candidate.Nodes[driverIndex].Config["skills"] = []any{"project_architect:missing"}
	require.Contains(t, issueCodes(svc.ValidateCandidateWithCapabilities(candidate, snapshot)), "skill_ref")
	candidate.Nodes[driverIndex].Config["skills"] = []any{"project_architect:project-guidance"}
	candidate.Nodes[driverIndex].Config["source_files"] = []any{"missing.md"}
	require.Contains(t, issueCodes(svc.ValidateCandidateWithCapabilities(candidate, snapshot)), "source_file")
}

func TestAutomationDraftServiceRejectsArbitraryTopologyAndUnsafeConfiguration(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)

	candidate.Nodes[0].Config["tool"] = "create_task"
	issues := svc.ValidateCandidate(candidate)
	require.NotEmpty(t, issues)
	require.Equal(t, "unknown_config", issues[0].Code)

	candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{Key: "arbitrary", Name: "Arbitrary", Type: models.AutomationNodeAction, Role: "execute_code", Config: map[string]any{"code": "rm -rf /"}})
	issues = svc.ValidateCandidate(candidate)
	require.NotEmpty(t, issues)
	require.Contains(t, issueCodes(issues), "unsupported_topology")

	candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	candidate.Edges[0].FromPort = "top"
	require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "invalid_edge", "unknown visual connector sides must not persist")

	_, err = DecodeAutomationDraftCandidate([]byte(`{"schema_version":1,"name":"x","description":"","automation_type":"vision_driver","adapter_key":"vision_driver","nodes":[],"edges":[],"database_id":"forbidden"}`))
	require.Error(t, err)

	for _, forbidden := range []string{"https://example.com/hook", "```sh\nrm -rf /\n```", "DROP TABLE tasks"} {
		candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
		require.NoError(t, err)
		driverIndex := automationDraftNodeIndexByKey(t, candidate, "vision_driver")
		candidate.Nodes[driverIndex].Config["prompt"] = forbidden
		require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "unsafe_config")
	}

	candidate, err = svc.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)
	for i := range candidate.Nodes {
		if _, ok := candidate.Nodes[i].Config["prompt"]; ok {
			candidate.Nodes[i].Config["prompt"] = strings.Repeat("x", 20000)
		}
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "graph_size")

	candidate, err = svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	driverIndex := automationDraftNodeIndexByKey(t, candidate, "vision_driver")
	candidate.Nodes[driverIndex].Config["priority"] = math.NaN()
	require.Contains(t, issueCodes(svc.ValidateCandidate(candidate)), "invalid_json")
}

func automationDraftNodeIndexByKey(t *testing.T, candidate models.AutomationDraftCandidate, key string) int {
	t.Helper()
	for i := range candidate.Nodes {
		if candidate.Nodes[i].Key == key {
			return i
		}
	}
	t.Fatalf("candidate has no %s node", key)
	return -1
}

func TestAutomationDraftCreationPersistsDefinitionOnly(t *testing.T) {
	db := testutil.NewTestDB(t)
	projectRepo := repository.NewProjectRepo(db)
	project := automationTestProject(t, projectRepo, "Draft only")
	automationRepo := repository.NewAutomationRepo(db)
	svc := NewAutomationDraftService(automationRepo, NewAutomationAdapterRegistry())
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)

	result, err := svc.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", CreatedVia: "web", Candidate: candidate})
	require.NoError(t, err)
	require.NotNil(t, result.Definition)
	require.Equal(t, models.AutomationVersionDraft, result.Definition.Version.State)
	require.Nil(t, result.Definition.Automation.PublishedVersionID)

	for _, table := range []string{"tasks", "schedules", "alerts", "executions", "workflow_executions", "task_pull_requests"} {
		var count int
		require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM `+table).Scan(&count))
		require.Zero(t, count, "draft creation must not mutate %s", table)
	}

	metadata, err := automationRepo.GetAutomationDraftMetadata(context.Background(), project.ID, result.Definition.Automation.ID, result.Definition.Version.ID)
	require.NoError(t, err)
	require.NotNil(t, metadata)
	require.Equal(t, candidate.AdapterKey, result.Candidate.AdapterKey)
}

func TestAutomationDraftClonePreservesPublishedVersion(t *testing.T) {
	db := testutil.NewTestDB(t)
	project := automationTestProject(t, repository.NewProjectRepo(db), "Clone")
	automationRepo := repository.NewAutomationRepo(db)
	taskRepo := repository.NewTaskRepo(db, nil)
	scheduleRepo := repository.NewScheduleRepo(db)
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(automationRepo, registry)
	candidate, err := drafts.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	created, err := drafts.CreateDraft(context.Background(), AutomationDraftCreateRequest{ProjectID: project.ID, Source: "template", Candidate: candidate})
	require.NoError(t, err)
	planner := NewAutomationPublicationPlanner(automationRepo, taskRepo, scheduleRepo, registry, drafts)
	plan, err := planner.Plan(context.Background(), project.ID, created.Definition.Automation.ID, created.Definition.Version.ID)
	require.NoError(t, err)
	compiler := NewAutomationCompiler(automationRepo, NewTaskService(taskRepo, repository.NewAttachmentRepo(db), nil), taskRepo, scheduleRepo, planner)
	published, err := compiler.Publish(context.Background(), AutomationPublishRequest{ProjectID: project.ID,
		AutomationID: created.Definition.Automation.ID, VersionID: created.Definition.Version.ID, PlanRevision: plan.PlanRevision})
	require.NoError(t, err)
	candidate.Edges[0].FromPort = "left"
	candidate.Edges[0].ToPort = "right"
	legacyJSON, err := json.Marshal(candidate)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE automation_draft_metadata SET candidate_json = ? WHERE version_id = ?`, string(legacyJSON), published.Definition.Version.ID)
	require.NoError(t, err)

	cloned, err := drafts.ClonePublishedVersion(context.Background(), project.ID, published.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, 2, cloned.Definition.Version.Version)
	require.Equal(t, models.AutomationVersionDraft, cloned.Definition.Version.State)
	require.NotEqual(t, published.Definition.Version.ID, cloned.Definition.Version.ID)
	require.Equal(t, "right", cloned.Candidate.Edges[0].FromPort, "editing after apply must migrate the source to its OUT port")
	require.Equal(t, "left", cloned.Candidate.Edges[0].ToPort, "editing after apply must migrate the target to its IN port")
	current, err := automationRepo.GetDefinition(context.Background(), project.ID, published.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, published.Definition.Version.ID, current.Version.ID, "cloning must not replace the active topology")

	cloned.Candidate.Nodes[0].Name = "Draft-only name"
	_, err = drafts.UpdateDraft(context.Background(), cloned.Definition.Automation.ID, cloned.Definition.Version.ID, project.ID, cloned.Candidate)
	require.NoError(t, err)
	reopened, err := drafts.ClonePublishedVersion(context.Background(), project.ID, published.Definition.Automation.ID)
	require.NoError(t, err)
	require.Equal(t, cloned.Definition.Version.ID, reopened.Definition.Version.ID, "Edit must reopen the current working design instead of creating another draft")
	require.Equal(t, "Draft-only name", reopened.Candidate.Nodes[0].Name, "saved edits must be reopened on the same Automation")
	var editableCount int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM automation_versions WHERE automation_id = ? AND state = 'draft'`, published.Definition.Automation.ID).Scan(&editableCount))
	require.Equal(t, 1, editableCount)
	original, err := automationRepo.GetDefinitionVersion(context.Background(), project.ID, published.Definition.Automation.ID, published.Definition.Version.ID)
	require.NoError(t, err)
	require.NotEqual(t, "Draft-only name", original.Nodes[0].Name)
}

func issueCodes(issues []models.AutomationValidationIssue) []string {
	codes := make([]string, len(issues))
	for i := range issues {
		codes[i] = issues[i].Code
	}
	return codes
}
