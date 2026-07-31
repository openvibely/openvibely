package service

import (
	"context"
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
			require.Equal(t, true, draftNode.Config["clear_context_on_start"], "%s/%s must explicitly clear context on each scheduled start", adapterKey, node.Key)
		}
		invalid := candidate
		invalid.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
		for i := range invalid.Nodes {
			if _, scheduled := invalid.Nodes[i].Config["run_at"]; !scheduled {
				continue
			}
			config := make(map[string]any, len(invalid.Nodes[i].Config))
			for key, value := range invalid.Nodes[i].Config {
				config[key] = value
			}
			config["enabled"] = false
			invalid.Nodes[i].Config = config
		}
		require.Contains(t, issueCodes(drafts.ValidateCandidate(invalid)), "enabled", "%s must reject per-schedule disable intent", adapterKey)

		oversized := candidate
		oversized.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
		for i := range oversized.Nodes {
			if _, scheduled := oversized.Nodes[i].Config["run_at"]; !scheduled {
				continue
			}
			config := make(map[string]any, len(oversized.Nodes[i].Config))
			for key, value := range oversized.Nodes[i].Config {
				config[key] = value
			}
			config["repeat_interval"] = models.MaxScheduleRepeatInterval + 1
			oversized.Nodes[i].Config = config
			break
		}
		require.Contains(t, issueCodes(drafts.ValidateCandidate(oversized)), "repeat_interval", "%s must reject oversized schedule intervals", adapterKey)
	}
}

func TestNativeSDLCTemplateUsesAutomationOwnedPromptsMatchingBootstrapContract(t *testing.T) {
	candidate, err := NewAutomationDraftService(nil, NewAutomationAdapterRegistry()).TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)

	visionPrompt, _ := automationDraftNodeByKey(t, candidate, "vision_suggestions").Config["prompt"].(string)
	require.Contains(t, visionPrompt, "Choose one focused project component or workflow")
	require.Contains(t, visionPrompt, "Vary the component over time")
	require.Contains(t, visionPrompt, "project vision or source-of-truth files")
	require.Contains(t, visionPrompt, "create_notification")
	require.Contains(t, visionPrompt, "stable idempotency key")
	require.Contains(t, visionPrompt, "Do not list, search, or inspect GitHub issues for duplicate detection")
	require.Contains(t, visionPrompt, "Native notification idempotency")
	require.Contains(t, visionPrompt, "Do not modify code")
	require.Contains(t, visionPrompt, "do not create implementation tasks")

	roles := map[string]struct {
		nodeKey       string
		identity      string
		requiredScope string
		forbidden     []string
	}{
		"bug_finder":          {nodeKey: "bug_finder", identity: "You are the Bug Finder.", requiredScope: "bug_suggestion", forbidden: []string{"You are the Optimization Finder.", "You are the Redundancy Finder."}},
		"optimization_finder": {nodeKey: "optimization_finder", identity: "You are the Optimization Finder.", requiredScope: "performance_suggestion", forbidden: []string{"You are the Bug Finder.", "You are the Redundancy Finder."}},
		"redundancy_finder":   {nodeKey: "redundancy_finder", identity: "You are the Redundancy Finder.", requiredScope: "maintenance_suggestion", forbidden: []string{"You are the Bug Finder.", "You are the Optimization Finder."}},
	}
	seenPrompts := map[string]bool{}
	for role, expected := range roles {
		prompt, _ := automationDraftNodeByKey(t, candidate, expected.nodeKey).Config["prompt"].(string)
		require.Contains(t, prompt, expected.identity, "%s must receive an explicit, unambiguous role", role)
		require.Contains(t, prompt, expected.requiredScope, "%s must use its role-specific Native notification type", role)
		for _, forbidden := range expected.forbidden {
			require.NotContains(t, prompt, forbidden, "%s must not receive another finder identity", role)
		}
		require.False(t, seenPrompts[prompt], "%s must not share a prompt body with another finder", role)
		seenPrompts[prompt] = true
		require.Contains(t, prompt, "Choose one focused project component or workflow")
		require.Contains(t, prompt, "create_notification")
		require.Contains(t, prompt, "stable idempotency key")
		require.Contains(t, prompt, "Do not list, search, or inspect GitHub issues for duplicate detection")
		require.Contains(t, prompt, "Native notification idempotency")
		require.Contains(t, prompt, "evidence, scope, risk, and acceptance criteria")
		require.Contains(t, prompt, "Approval authorizes task creation only")
	}

	inboxPrompt, _ := automationDraftNodeByKey(t, candidate, "inbox").Config["prompt"].(string)
	for _, required := range []string{
		"decision_state=approved", "implementation_task_linked=false", "get_alert", "claim_alert",
		"create_alert_implementation_task", "complete_alert_processing", "fail_alert_processing", "release_alert_claim",
	} {
		require.Contains(t, inboxPrompt, required)
	}
	require.Contains(t, inboxPrompt, "atomically links at most one task")
	require.Contains(t, inboxPrompt, "The created task is the implementation task")
	require.Contains(t, inboxPrompt, "directly instruct it to implement the reviewed change")
	require.Contains(t, inboxPrompt, "must not create or look for another implementation task")
	require.Contains(t, inboxPrompt, "must not run notification intake or call get_alert")
	require.NotContains(t, inboxPrompt, "implementation approval")
	require.Contains(t, inboxPrompt, "Call list_alerts without project_id")
	require.Contains(t, inboxPrompt, "Before calling claim_alert, collect every eligible result from all pages")
	require.Contains(t, inboxPrompt, "Do not claim, link, or process any notification while paginating")
	require.Contains(t, inboxPrompt, "Only after the complete paginated snapshot is collected")
	require.Contains(t, inboxPrompt, "Do not pass the read filter")
	require.Contains(t, inboxPrompt, "both read and unread approved notifications")
	require.Contains(t, inboxPrompt, "call execute_tasks with that exact implementation task ID")
	require.Contains(t, inboxPrompt, "Only after execute_tasks succeeds")
	require.Contains(t, inboxPrompt, "Never reuse a project ID from prior messages, examples, memory, or tool output")

	auditorPrompt, _ := automationDraftNodeByKey(t, candidate, "auditor").Config["prompt"].(string)
	for _, required := range []string{"stale notifications", "expired or failed claims", "missing notification/task links", "duplicate implementation work", "blocked tasks", "create_notification"} {
		require.Contains(t, auditorPrompt, required)
	}
	require.Contains(t, auditorPrompt, "does not bypass approval")
	require.Contains(t, auditorPrompt, "Do not list, search, or inspect GitHub issues for duplicate detection")
	require.Contains(t, auditorPrompt, "Native notification and task state")
}

func TestNativeSDLCTemplateOwnsItsPrompts(t *testing.T) {
	source, err := os.ReadFile("automation_native_sdlc_prompts.go")
	require.NoError(t, err)
	sourceText := string(source)
	require.Contains(t, sourceText, "func nativeSDLCRolePrompt")
	require.Contains(t, sourceText, "const nativeSDLCBugFinderPrompt")
	require.Contains(t, sourceText, "const nativeSDLCOptimizationFinderPrompt")
	require.Contains(t, sourceText, "const nativeSDLCRedundancyFinderPrompt")
	require.NotContains(t, sourceText, "builtinskills")
	require.NotContains(t, sourceText, "SKILL.md")
}

func TestGitHubSDLCTemplateConfiguresAndValidatesIssueImplementationTask(t *testing.T) {
	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)

	implementation := automationDraftNodeByKey(t, candidate, "implementation")
	require.NotEmpty(t, implementation.Config["prompt"])
	require.Equal(t, string(models.CategoryActive), implementation.Config["category"])
	require.EqualValues(t, 2, implementation.Config["priority"])
	require.Empty(t, drafts.ValidateCandidate(candidate))

	for _, field := range []string{"prompt", "category", "priority"} {
		invalid := candidate
		invalid.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
		for i := range invalid.Nodes {
			if invalid.Nodes[i].Key != "implementation" {
				continue
			}
			invalid.Nodes[i].Config = make(map[string]any, len(candidate.Nodes[i].Config))
			for key, value := range candidate.Nodes[i].Config {
				invalid.Nodes[i].Config[key] = value
			}
			delete(invalid.Nodes[i].Config, field)
		}
		require.Contains(t, issueCodes(drafts.ValidateCandidate(invalid)), map[string]string{
			"prompt": "missing_prompt", "category": "category", "priority": "priority",
		}[field], "maintained GitHub implementation %s must be required before Save", field)
	}

	for _, category := range []models.TaskCategory{models.CategoryBacklog, models.CategoryScheduled} {
		invalidCategory := candidate
		invalidCategory.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
		for i := range invalidCategory.Nodes {
			if invalidCategory.Nodes[i].Key != "implementation" {
				continue
			}
			invalidCategory.Nodes[i].Config = make(map[string]any, len(candidate.Nodes[i].Config))
			for key, value := range candidate.Nodes[i].Config {
				invalidCategory.Nodes[i].Config[key] = value
			}
			invalidCategory.Nodes[i].Config["category"] = string(category)
		}
		require.Contains(t, issueCodes(drafts.ValidateCandidate(invalidCategory)), "category", "approved issue-specific Implementation tasks must be Active, not %s", category)
	}

	devInbox := automationDraftNodeByKey(t, candidate, "dev_inbox")
	require.Equal(t, string(models.CategoryScheduled), devInbox.Config["category"])
	require.NotContains(t, issueCodes(drafts.ValidateCandidate(candidate)), "category", "maintained Schedule nodes must retain their scheduled category")
}

func TestGitHubSDLCTemplateAcceptsBrowserSubmittedActionSettings(t *testing.T) {
	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)

	for i := range candidate.Nodes {
		switch candidate.Nodes[i].Role {
		case "create_github_issue":
			candidate.Nodes[i].Config["instructions"] = "Open one focused, reviewable GitHub issue."
			candidate.Nodes[i].Config["labels"] = []string{"suggestion"}
		case "open_pull_request":
			candidate.Nodes[i].Config["instructions"] = "Open a reviewable pull request linked to the source issue."
			candidate.Nodes[i].Config["base"] = ""
			candidate.Nodes[i].Config["draft"] = false
		}
	}

	require.Empty(t, drafts.ValidateCandidate(candidate), "the maintained template must accept the action settings emitted by its own browser form")
}

func TestGitHubSDLCPromptsUseRepositoryFallbackAndTrustedLocalDeduplication(t *testing.T) {
	require.Contains(t, githubSDLCDevInboxPrompt, "or from a GitHub remote in its local checkout when that URL is blank")
	require.NotContains(t, githubSDLCDevInboxPrompt, "restricted to the selected project's explicit repository URL")
	require.Contains(t, githubSDLCDevInboxPrompt, "category=active")
	require.Contains(t, githubSDLCDevInboxPrompt, "Do not call `execute_tasks` for a newly created Active task")
	require.Contains(t, githubSDLCDevInboxPrompt, "For a reconciled existing task, call `execute_tasks` only when `list_tasks` shows category Backlog or status failed/cancelled")
	require.Contains(t, githubSDLCDevInboxPrompt, "Never call `execute_tasks` for an Active pending, queued, running, or completed task")
	require.NotContains(t, githubSDLCDevInboxPrompt, "Finally, call execute_tasks with that exact task ID")
	require.Contains(t, githubSDLCDevInboxPrompt, "Do not leave approved implementation work in Backlog")
	for name, prompt := range map[string]string{
		"offering manager":    githubSDLCOfferingManagerPrompt,
		"bug finder":          githubSDLCBugFinderPrompt,
		"optimization finder": githubSDLCOptimizationFinderPrompt,
		"redundancy finder":   githubSDLCRedundancyFinderPrompt,
	} {
		require.Contains(t, prompt, "Do not list, search, or inspect existing GitHub issues for duplicate detection", name)
		require.Contains(t, prompt, "Do not require a repository-wide issue or pull-request listing/search before publication", name)
		require.Contains(t, prompt, "Do not block publication because such a listing/search is unavailable, unauthenticated, incomplete, or unpaginated", name)
		require.Contains(t, prompt, "Call github_create_issue for each actionable finding; the server performs trusted local duplicate prevention", name)
		require.NotContains(t, prompt, "searching/inspecting existing visible work", name)
	}

	candidate := models.AutomationDraftCandidate{
		AdapterKey: AutomationAdapterCustom,
		Nodes: []models.AutomationDraftNode{
			{Key: "inbox", Type: models.AutomationNodeAgentTask, Role: "github_inbox", Config: map[string]any{"prompt": "Process assigned issues."}},
			{Key: "implementation", Type: models.AutomationNodeAgentTask, Role: "task", Config: map[string]any{"prompt": "Implement the issue.", "category": "active", "priority": 2}},
			{Key: "open_pr", Type: models.AutomationNodeAction, Role: "open_pull_request", Config: map[string]any{"instructions": "Open a reviewable pull request.", "base": "", "draft": false}},
		},
		Edges: []models.AutomationDraftEdge{
			{Key: "inbox_implementation", From: "inbox", To: "implementation"},
			{Key: "implementation_open_pr", From: "implementation", To: "open_pr"},
		},
	}
	prompt := automationCompiledTaskPrompt(candidate, candidate.Nodes[0])
	require.Contains(t, prompt, "or from a GitHub remote in its local checkout when that URL is blank")
	require.NotContains(t, prompt, "restricted to this project's explicit repository URL")
	require.Contains(t, prompt, "Set source_github_issue_number to the exact issue number returned by this inbox execution")
	require.Contains(t, prompt, "Create each new issue task with category active; Active creation submits it automatically")
	require.Contains(t, prompt, "Do not call execute_tasks for a newly created Active task")
	require.Contains(t, prompt, "For a reconciled existing task, call execute_tasks only when list_tasks shows category Backlog or status failed/cancelled")
	require.Contains(t, prompt, "Never call execute_tasks for an Active pending, queued, running, or completed task")
	require.NotContains(t, prompt, "Finally, call execute_tasks with that exact task ID")
}

func TestGitHubSDLCTemplateOwnsItsPrompts(t *testing.T) {
	source, err := os.ReadFile("automation_github_sdlc_prompts.go")
	require.NoError(t, err)
	sourceText := string(source)
	require.Contains(t, sourceText, "const githubSDLCDevInboxPrompt")
	require.Contains(t, sourceText, "const githubSDLCOfferingManagerPrompt")
	require.Contains(t, sourceText, "const githubSDLCBugFinderPrompt")
	require.Contains(t, sourceText, "const githubSDLCOptimizationFinderPrompt")
	require.Contains(t, sourceText, "const githubSDLCRedundancyFinderPrompt")
	require.Contains(t, sourceText, "const githubSDLCLoopAuditorPrompt")
	require.NotContains(t, sourceText, "builtinskills")
	require.NotContains(t, sourceText, "SKILL.md")
	require.NotContains(t, sourceText, "dev-inbox-execution-invariants.md")
}

func TestGitHubSDLCTemplateUsesAutomationOwnedPrompts(t *testing.T) {
	candidate, err := NewAutomationDraftService(nil, NewAutomationAdapterRegistry()).TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)

	promptsByNode := map[string]string{
		"vision_suggestions":  githubSDLCOfferingManagerPrompt,
		"bug_finder":          mustGitHubSDLCRolePrompt(t, "bug_finder"),
		"optimization_finder": mustGitHubSDLCRolePrompt(t, "optimization_finder"),
		"redundancy_finder":   mustGitHubSDLCRolePrompt(t, "redundancy_finder"),
		"dev_inbox":           githubSDLCDevInboxPrompt,
		"auditor":             githubSDLCLoopAuditorPrompt,
	}
	finderExpectations := map[string]struct {
		identity  string
		label     string
		forbidden []string
	}{
		"bug_finder":          {identity: "You are the Bug Finder.", label: "bug", forbidden: []string{"You are the Optimization Finder.", "You are the Redundancy Finder."}},
		"optimization_finder": {identity: "You are the Optimization Finder.", label: "performance", forbidden: []string{"You are the Bug Finder.", "You are the Redundancy Finder."}},
		"redundancy_finder":   {identity: "You are the Redundancy Finder.", label: "duplication", forbidden: []string{"You are the Bug Finder.", "You are the Optimization Finder."}},
	}
	seenFinderPrompts := map[string]bool{}
	for nodeKey, expected := range finderExpectations {
		prompt := promptsByNode[nodeKey]
		require.Contains(t, prompt, expected.identity, "%s must receive an explicit, unambiguous role", nodeKey)
		require.Contains(t, prompt, "label `"+expected.label+"`", "%s must use its role-specific GitHub label", nodeKey)
		for _, forbidden := range expected.forbidden {
			require.NotContains(t, prompt, forbidden, "%s must not receive another finder identity", nodeKey)
		}
		require.False(t, seenFinderPrompts[prompt], "%s must not share a prompt body with another finder", nodeKey)
		seenFinderPrompts[prompt] = true
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
	for nodeKey, prompt := range promptsByNode {
		node := automationDraftNodeByKey(t, candidate, nodeKey)
		require.Equal(t, prompt, node.Config["prompt"], "%s must use the Automation template prompt", nodeKey)
		require.Equal(t, prompt, automationCompiledTaskPrompt(candidate, node), "%s must persist the exact Automation template prompt without custom-graph additions", nodeKey)
		require.NotContains(t, node.Config["prompt"], "references/dev-inbox-execution-invariants.md")
		require.NotContains(t, node.Config["prompt"], "repository-wide current issue listing or search")
		require.Equal(t, expectedCadence[nodeKey].repeatType, node.Config["repeat_type"], "%s cadence must match the maintained template", nodeKey)
		require.EqualValues(t, expectedCadence[nodeKey].interval, node.Config["repeat_interval"])
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

func mustGitHubSDLCRolePrompt(t *testing.T, role string) string {
	t.Helper()
	prompt, err := githubSDLCRolePrompt(role)
	require.NoError(t, err)
	return prompt
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

func TestCustomAutomationValidatesClosedNativeMailboxFlowAndRejectsMixedMailboxFamilies(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate := customNativeMailboxCandidate("Native approval loop")
	require.Empty(t, svc.ValidateCandidate(candidate), "the complete Native mailbox family must be a supported custom construction")
	reopened, err := svc.normalizeReopenedCandidate(candidate)
	require.NoError(t, err)
	require.Equal(t, "implementation", automationDraftNodeByKey(t, reopened, "custom_implementation").Role, "trusted reopen must preserve the Native projection stage")
	mixed := candidate
	mixed.Nodes = append(append([]models.AutomationDraftNode(nil), candidate.Nodes...), models.AutomationDraftNode{Key: "github_assignment", Name: "GitHub assignment", Type: models.AutomationNodeHumanGate, Role: "github_assignment", Config: map[string]any{"approval_method": "github_assignment"}})
	require.Contains(t, issueCodes(svc.ValidateCandidate(mixed)), "mixed_mailbox_families")
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

	backlogImplementation := candidate
	backlogImplementation.Nodes = append([]models.AutomationDraftNode(nil), candidate.Nodes...)
	for i := range backlogImplementation.Nodes {
		if backlogImplementation.Nodes[i].Key == "implementation" {
			backlogImplementation.Nodes[i].Config = map[string]any{"prompt": "Implement the accepted issue and run relevant validation.", "category": "backlog", "priority": 3}
		}
	}
	require.Contains(t, issueCodes(svc.ValidateCandidate(backlogImplementation)), "category", "the issue-linked Task must be Active because GitHub assignment approves immediate submission")

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

func TestAutomationDraftReopenLimitsLegacySemanticCleanupToTrustedSavedGraphs(t *testing.T) {
	svc := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := svc.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Name = "Legacy custom graph"
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run scheduled work.", "category": "scheduled", "priority": 2, "target_node_key": "implementation", "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true}},
		{Key: "implementation", Name: "Implementation", Type: models.AutomationNodeAgentTask, Role: "implementation", Config: map[string]any{"prompt": "Implement the work.", "category": "backlog", "priority": 2}},
	}
	candidate.Edges = []models.AutomationDraftEdge{{Key: "schedule_implementation", From: "schedule", To: "implementation", Condition: map[string]any{}}}

	strict, err := svc.NormalizeCandidate(candidate)
	require.NoError(t, err)
	require.Equal(t, "implementation", strict.Nodes[0].Config["target_node_key"], "strict submitted-candidate normalization must preserve explicit semantic values for validation")
	require.Equal(t, "implementation", strict.Nodes[1].Role)
	require.Contains(t, issueCodes(svc.ValidateCandidate(strict)), "native_implementation_source", "the implementation role is valid only in the closed Native inbox topology")
	require.Empty(t, strict.Edges[0].FromPort)
	require.Empty(t, strict.Edges[0].ToPort)

	reopened, err := svc.normalizeReopenedCandidate(candidate)
	require.NoError(t, err)
	require.NotContains(t, reopened.Nodes[0].Config, "target_node_key")
	require.Equal(t, "task", reopened.Nodes[1].Role)
	require.Equal(t, "right", reopened.Edges[0].FromPort)
	require.Equal(t, "left", reopened.Edges[0].ToPort)
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
		preview, err := svc.PreviewCandidate(context.Background(), project.ID, candidate, nil)
		require.NoError(t, err)
		require.Contains(t, issueCodes(preview.ValidationErrors), "schema_version")
	}
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
	preview, err := svc.PreviewCandidate(ctx, project.ID, candidate, nil)
	require.NoError(t, err)
	require.Contains(t, issueCodes(preview.ValidationErrors), "agent_ref", "unresolved references must remain visible without being guessed")

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

func issueCodes(issues []models.AutomationValidationIssue) []string {
	codes := make([]string, len(issues))
	for i := range issues {
		codes[i] = issues[i].Code
	}
	return codes
}
