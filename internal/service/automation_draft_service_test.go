package service

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestGitHubSDLCCapabilityValidationFollowsRetainedGraph(t *testing.T) {
	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	candidate, err := drafts.TemplateCandidate(AutomationAdapterGitHubSDLC)
	require.NoError(t, err)

	for _, node := range candidate.Nodes {
		require.NotContains(t, node.Config, "required_capabilities", node.Key)
	}

	for _, retainedKey := range []string{"vision_suggestions"} {
		reduced := candidate
		for _, node := range append([]models.AutomationDraftNode(nil), candidate.Nodes...) {
			if node.Key != retainedKey {
				reduced = automationCandidateWithoutNode(reduced, node.Key)
			}
		}
		require.Empty(t, drafts.ValidateCandidate(reduced), retainedKey)
		require.NotContains(t, issueCodes(drafts.ValidateCandidateWithCapabilities(reduced, models.AutomationCapabilitySnapshot{})), "github_unavailable", retainedKey)
	}

	require.Contains(t, issueCodes(drafts.ValidateCandidateWithCapabilities(candidate, models.AutomationCapabilitySnapshot{})), "github_unavailable")
}

func TestMaintainedSDLCTemplatesTreatEveryTemplateNodeAsOptional(t *testing.T) {
	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
		t.Run(adapterKey, func(t *testing.T) {
			candidate, err := drafts.TemplateCandidate(adapterKey)
			require.NoError(t, err)

			customizedProducer, err := drafts.TemplateCandidate(adapterKey)
			require.NoError(t, err)
			for _, node := range append([]models.AutomationDraftNode(nil), customizedProducer.Nodes...) {
				if node.Key != "vision_suggestions" {
					customizedProducer = automationCandidateWithoutNode(customizedProducer, node.Key)
				}
			}
			customizedProducer.Nodes[0].Config["prompt"] = "Review the local project notes and summarize one improvement."
			require.Empty(t, drafts.ValidateCandidate(customizedProducer), "a customized producer may run as a standalone schedule")
			if adapterKey == AutomationAdapterGitHubSDLC {
				require.NotContains(t, issueCodes(drafts.ValidateCandidateWithCapabilities(customizedProducer, models.AutomationCapabilitySnapshot{})), "github_unavailable")
			}

			for _, key := range []string{"vision_suggestions", "bug_finder", "optimization_finder", "redundancy_finder"} {
				withoutOneTemplateNode := automationCandidateWithoutNode(candidate, key)
				require.Empty(t, drafts.ValidateCandidate(withoutOneTemplateNode), key)
			}

			for _, producerKey := range []string{"vision_suggestions", "bug_finder", "optimization_finder", "redundancy_finder"} {
				producerOnly := candidate
				for _, node := range append([]models.AutomationDraftNode(nil), candidate.Nodes...) {
					if node.Key != producerKey {
						producerOnly = automationCandidateWithoutNode(producerOnly, node.Key)
					}
				}
				producerIssues := drafts.ValidateCandidate(producerOnly)
				require.Empty(t, producerIssues, producerKey)

				withoutProducer := automationCandidateWithoutNode(candidate, producerKey)
				require.Empty(t, drafts.ValidateCandidate(withoutProducer), producerKey)
			}

			reconnected := candidate
			reconnected.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
			reconnected.Edges[0].Key = "user_reconnected_edge"
			require.Empty(t, drafts.ValidateCandidate(reconnected))

			duplicateHandoff := candidate
			duplicateHandoff.Edges = append([]models.AutomationDraftEdge(nil), candidate.Edges...)
			duplicate := duplicateHandoff.Edges[0]
			duplicate.Key = "forged_duplicate_handoff"
			duplicateHandoff.Edges = append(duplicateHandoff.Edges, duplicate)
			require.Contains(t, issueCodes(drafts.ValidateCandidate(duplicateHandoff)), "ambiguous_handoff")

			stranded := automationCandidateWithoutNode(candidate, "completed")
			issues := drafts.ValidateCandidate(stranded)
			require.NotContains(t, issueCodes(issues), "missing_node")
			require.NotContains(t, issueCodes(issues), "missing_edge")
			if adapterKey == AutomationAdapterGitHubSDLC {
				require.Contains(t, issueCodes(issues), "pull_request_review_target")
			} else {
				require.Contains(t, issueCodes(issues), "native_implementation_target")
			}

			missingTerminalEdge := candidate
			if adapterKey == AutomationAdapterGitHubSDLC {
				missingTerminalEdge = automationCandidateWithoutEdge(missingTerminalEdge, "review_to_completed")
			} else {
				missingTerminalEdge = automationCandidateWithoutEdge(missingTerminalEdge, "implementation_to_completed")
			}
			edgeIssues := drafts.ValidateCandidate(missingTerminalEdge)
			require.NotContains(t, issueCodes(edgeIssues), "missing_edge")
			if adapterKey == AutomationAdapterGitHubSDLC {
				require.Contains(t, issueCodes(edgeIssues), "pull_request_review_target")
			} else {
				require.Contains(t, issueCodes(edgeIssues), "native_implementation_target")
			}
		})
	}
}

func TestDefaultAutomationDraftNodeConfigProvidesCanonicalStructuralDefaults(t *testing.T) {
	taskConfig, err := DefaultAutomationDraftNodeConfig(AutomationAdapterCustom, "review", models.AutomationNodeAgentTask, "task", map[string]bool{"task": true})
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"prompt":          "Describe the work this node should perform.",
		"goal":            "",
		"category":        string(models.CategoryBacklog),
		"priority":        2,
		"model_config_id": automationDefaultModelConfigID,
	}, taskConfig)

	scheduleConfig, err := DefaultAutomationDraftNodeConfig(AutomationAdapterCustom, "daily_review", models.AutomationNodeTrigger, "fixed_schedule", map[string]bool{"task": true, "schedule": true})
	require.NoError(t, err)
	require.Equal(t, "Describe the scheduled work this node should perform.", scheduleConfig["prompt"])
	require.Equal(t, string(models.CategoryScheduled), scheduleConfig["category"])
	require.Equal(t, automationDefaultModelConfigID, scheduleConfig["model_config_id"])
	require.Equal(t, "09:00", scheduleConfig["run_at"])
	require.Equal(t, string(models.RepeatDaily), scheduleConfig["repeat_type"])
	require.Equal(t, 1, scheduleConfig["repeat_interval"])
	require.Equal(t, true, scheduleConfig["enabled"])
	require.Equal(t, true, scheduleConfig["clear_context_on_start"])
	require.NotContains(t, scheduleConfig, "target_node_key")

	githubInboxConfig, err := DefaultAutomationDraftNodeConfig(AutomationAdapterGitHubSDLC, "manual_dev_inbox", models.AutomationNodeTrigger, "github_inbox", map[string]bool{"task": true, "schedule": true})
	require.NoError(t, err)
	require.Equal(t, "10:00", githubInboxConfig["run_at"])
	require.Equal(t, automationDefaultModelConfigID, githubInboxConfig["model_config_id"])

	nativeImplementationConfig, err := DefaultAutomationDraftNodeConfig(AutomationAdapterNativeSDLC, "manual_implementation", models.AutomationNodeAgentTask, "implementation", nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"goal": "", "model_config_id": automationDefaultModelConfigID}, nativeImplementationConfig)

	issueConfig, err := DefaultAutomationDraftNodeConfig(AutomationAdapterCustom, "issue", models.AutomationNodeAction, "create_github_issue", nil)
	require.NoError(t, err)
	require.Equal(t, "Open one focused, reviewable GitHub issue.", issueConfig["instructions"])
	require.Equal(t, []string{}, issueConfig["labels"])

	prConfig, err := DefaultAutomationDraftNodeConfig(AutomationAdapterCustom, "open_pr", models.AutomationNodeAction, "open_pull_request", nil)
	require.NoError(t, err)
	require.Equal(t, "Open a reviewable pull request linked to the source issue.", prConfig["instructions"])
	require.Equal(t, "", prConfig["base"])
	require.Equal(t, false, prConfig["draft"])

	approvalConfig, err := DefaultAutomationDraftNodeConfig(AutomationAdapterCustom, "review", models.AutomationNodeHumanGate, "pull_request_review", nil)
	require.NoError(t, err)
	require.Equal(t, map[string]any{"approval_method": "pull_request_review"}, approvalConfig)
}

func TestMaintainedSDLCTemplatesDoNotIncludeLoopAuditor(t *testing.T) {
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(nil, registry)

	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
		t.Run(adapterKey, func(t *testing.T) {
			adapter, ok := registry.Get(adapterKey)
			require.True(t, ok)
			for _, node := range adapter.Nodes {
				require.NotEqual(t, "auditor", node.Key)
				require.NotEqual(t, "loop_auditor", node.Role)
				require.NotEqual(t, "Loop Auditor", node.Name)
			}

			candidate, err := drafts.TemplateCandidate(adapterKey)
			require.NoError(t, err)
			requireNoLoopAuditorNode(t, candidate)
			require.Empty(t, drafts.ValidateCandidate(candidate))
		})
	}
}

func TestMaintainedSDLCTemplatesKeepDiscoveryParityAndSchedulesOwnTheirTasks(t *testing.T) {
	registry := NewAutomationAdapterRegistry()
	drafts := NewAutomationDraftService(nil, registry)
	discoveryRoles := []string{"offering_manager", "bug_finder", "optimization_finder", "redundancy_finder"}

	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
		adapter, ok := registry.Get(adapterKey)
		require.True(t, ok)
		for _, role := range discoveryRoles {
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

func TestAutomationDraftScheduleConfigValidationMatchesForCustomAndMaintainedSchedules(t *testing.T) {
	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	cases := []struct {
		name    string
		field   string
		value   any
		code    string
		message string
	}{
		{name: "malformed run_at", field: "run_at", value: "25:99", code: "run_at", message: "Trigger time must use HH:MM local time."},
		{name: "unsupported repeat_type", field: "repeat_type", value: "yearly", code: "repeat_type", message: "Unsupported schedule repeat type."},
		{name: "oversized repeat_interval", field: "repeat_interval", value: models.MaxScheduleRepeatInterval + 1, code: "repeat_interval", message: "Schedule interval must be between 1 and 365."},
		{name: "disabled schedule", field: "enabled", value: false, code: "enabled", message: "Schedule execution is controlled by the Automation lifecycle and must be enabled."},
		{name: "non-bool clear_context_on_start", field: "clear_context_on_start", value: "yes", code: "clear_context_on_start", message: "Clear context on start must be true or false."},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			custom := customFixedScheduleCandidate(t, drafts)
			custom.Nodes[0].Config[tc.field] = tc.value
			customIssue := validationIssueByNodeAndCode(t, drafts.ValidateCandidate(custom), "schedule", tc.code)
			require.Equal(t, tc.message, customIssue.Message)

			maintained, err := drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
			require.NoError(t, err)
			maintained = cloneAutomationDraftCandidate(maintained)
			idx := automationDraftNodeIndexByKey(t, maintained, "vision_suggestions")
			maintained.Nodes[idx].Config[tc.field] = tc.value
			maintainedIssue := validationIssueByNodeAndCode(t, drafts.ValidateCandidate(maintained), "vision_suggestions", tc.code)
			require.Equal(t, customIssue.Message, maintainedIssue.Message)
		})
	}
}

func TestAutomationDraftScheduleTargetValidationRemainsMaintainedAdapterSpecific(t *testing.T) {
	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())

	custom := customFixedScheduleCandidate(t, drafts)
	custom.Nodes[0].Config["target_node_key"] = "other"
	customCodes := issueCodes(drafts.ValidateCandidate(custom))
	require.NotContains(t, customCodes, "schedule_target")
	require.Contains(t, customCodes, "unknown_config")

	maintained, err := drafts.TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)
	maintained = cloneAutomationDraftCandidate(maintained)
	idx := automationDraftNodeIndexByKey(t, maintained, "vision_suggestions")
	maintained.Nodes[idx].Config["target_node_key"] = "implementation"
	issue := validationIssueByNodeAndCode(t, drafts.ValidateCandidate(maintained), "vision_suggestions", "schedule_target")
	require.Equal(t, "Trigger target is fixed by the registered adapter.", issue.Message)
}

func TestNativeSDLCTemplateUsesAutomationOwnedPromptsMatchingBootstrapContract(t *testing.T) {
	candidate, err := NewAutomationDraftService(nil, NewAutomationAdapterRegistry()).TemplateCandidate(AutomationAdapterNativeSDLC)
	require.NoError(t, err)

	visionPrompt, _ := automationDraftNodeByKey(t, candidate, "vision_suggestions").Config["prompt"].(string)
	require.Contains(t, visionPrompt, "Choose one focused project component or workflow")
	require.Contains(t, visionPrompt, "Vary the component over time")
	require.Contains(t, visionPrompt, "VISION.md")
	require.Contains(t, visionPrompt, "read it before choosing the finding")
	require.Contains(t, visionPrompt, "project vision, or other source-of-truth files")
	require.Contains(t, visionPrompt, "create_notification")
	require.NotContains(t, visionPrompt, "idempotency_key")
	require.Contains(t, visionPrompt, "list_existing_automation_notifications")
	require.Contains(t, visionPrompt, "next_offset")
	require.Contains(t, visionPrompt, "skip that candidate and keep searching")
	require.Contains(t, visionPrompt, "Do not modify code")
	require.Contains(t, visionPrompt, "do not create implementation tasks")
	require.Contains(t, visionPrompt, "## Summary")
	require.Contains(t, visionPrompt, "Technical detail is useful and must not be omitted")
	require.Contains(t, visionPrompt, "Make the title and message understandable to a product user")

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
		require.Contains(t, prompt, "## Summary", "%s Native notification must open with a readable summary", role)
		require.Contains(t, prompt, "Technical detail is useful and must not be omitted")
		require.Contains(t, prompt, "Make the title and message understandable to a product user")
		require.Contains(t, prompt, "Choose one focused project component or workflow")
		require.Contains(t, prompt, "create_notification")
		require.NotContains(t, prompt, "idempotency_key")
		require.Contains(t, prompt, "list_existing_automation_notifications")
		require.Contains(t, prompt, "next_offset")
		require.Contains(t, prompt, "skip that candidate and keep searching")
		require.Contains(t, prompt, "acceptance criteria")
		require.Contains(t, prompt, "Approval authorizes task creation only")
	}

	inboxNode := automationDraftNodeByKey(t, candidate, "inbox")
	inboxPrompt, _ := inboxNode.Config["prompt"].(string)
	for _, required := range []string{
		"every recovery-eligible processing state", "get_alert", "claim_alert",
		"create_alert_implementation_task", "complete_alert_processing", "fail_alert_processing", "release_alert_claim",
	} {
		require.Contains(t, inboxPrompt, required)
	}
	require.Equal(t, string(models.RepeatDaily), inboxNode.Config["repeat_type"], "inbox checker must run once daily, not hourly")
	require.EqualValues(t, 1, inboxNode.Config["repeat_interval"])
	require.Equal(t, "10:00", inboxNode.Config["run_at"], "inbox checker must run an hour after the daily drivers")
	require.Contains(t, inboxPrompt, "atomically links at most one task")
	require.Contains(t, inboxPrompt, "The created task is the implementation task")
	require.Contains(t, inboxPrompt, "directly instruct it to implement the reviewed change")
	require.Contains(t, inboxPrompt, "must not create or look for another implementation task")
	require.Contains(t, inboxPrompt, "must not run notification intake or call get_alert")
	require.NotContains(t, inboxPrompt, "implementation approval")
	require.Contains(t, inboxPrompt, "Call list_alerts without project_id, using decision_state=approved, implementation_task_linked=false")
	require.Contains(t, inboxPrompt, "Before calling claim_alert, collect every eligible result from all pages")
	require.Contains(t, inboxPrompt, "Do not claim, link, or process any notification while paginating")
	require.Contains(t, inboxPrompt, "Only after the complete paginated snapshot is collected")
	require.Contains(t, inboxPrompt, "Do not pass processing_state, read, type, or source")
	require.Contains(t, inboxPrompt, "both read states")
	require.Contains(t, inboxPrompt, "call execute_tasks with that exact implementation task ID")
	require.Contains(t, inboxPrompt, "Only after execute_tasks succeeds")
	require.Contains(t, inboxPrompt, "Never reuse a project ID from prior messages, examples, memory, or tool output")

	requireNoLoopAuditorNode(t, candidate)
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
	require.Contains(t, githubSDLCOfferingManagerPrompt, "VISION.md")
	require.Contains(t, githubSDLCOfferingManagerPrompt, "read it before choosing the finding")
	require.Contains(t, githubSDLCDevInboxPrompt, "or from a GitHub remote in its local checkout when that URL is blank")
	require.NotContains(t, githubSDLCDevInboxPrompt, "restricted to the selected project's explicit repository URL")
	require.Contains(t, githubSDLCDevInboxPrompt, "category=active")
	require.Contains(t, githubSDLCDevInboxPrompt, "Use `github_list_assigned_issues` and `github_list_my_assigned_issues` as compact body-free discovery lists")
	require.Contains(t, githubSDLCDevInboxPrompt, "Do not call `github_get_issue` for every listed issue as a default scan step")
	require.Contains(t, githubSDLCDevInboxPrompt, "call it only for the specific listed issue that needs body or acceptance-note details")
	require.Contains(t, githubSDLCDevInboxPrompt, "Use the provided GitHub runtime tools as the only source for inbox discovery")
	require.Contains(t, githubSDLCDevInboxPrompt, "do not use local shell commands, `gh`, Python scripts, `curl`, or direct GitHub API calls")
	require.NotContains(t, githubSDLCDevInboxPrompt, "For each returned issue, inspect it with `github_get_issue`")
	require.Contains(t, githubSDLCDevInboxPrompt, "Include the GitHub issue number, URL, title, body or acceptance notes, relevant labels, and assignment context")
	require.Contains(t, githubSDLCDevInboxPrompt, "perform one existing-task lookup with `list_tasks` using the issue number or URL as the `query`")
	require.Contains(t, githubSDLCDevInboxPrompt, "Treat that single lookup result as the reconciliation result for that issue")
	require.Contains(t, githubSDLCDevInboxPrompt, "Do not retry the same issue lookup")
	require.Contains(t, githubSDLCDevInboxPrompt, "do not vary task lifecycle filters to search again")
	require.Contains(t, githubSDLCDevInboxPrompt, "do not run title or fragment searches for the same issue")
	require.NotContains(t, githubSDLCDevInboxPrompt, "Use a different query such as the exact proposed task title")
	require.NotContains(t, githubSDLCDevInboxPrompt, "issue-number/URL matching is inconclusive")
	require.NotContains(t, githubSDLCDevInboxPrompt, "A returned `filter` object only echoes")
	require.NotContains(t, githubSDLCDevInboxPrompt, "runtime defaults")
	require.NotContains(t, githubSDLCDevInboxPrompt, "lifecycle state combinations")
	require.Contains(t, githubSDLCDevInboxPrompt, "Do not call `execute_tasks` for a newly created Active task")
	require.Contains(t, githubSDLCDevInboxPrompt, "After `create_task` succeeds for a newly created Active task, do not call `list_tasks` again for that issue")
	require.Contains(t, githubSDLCDevInboxPrompt, "the successful `create_task` response is the confirmation")
	require.Contains(t, githubSDLCDevInboxPrompt, "For a reconciled existing task, call `execute_tasks` only when `list_tasks` shows category Backlog or status failed/cancelled")
	require.Contains(t, githubSDLCDevInboxPrompt, "Never call `execute_tasks` for an Active pending, queued, running, or completed task")
	require.NotContains(t, githubSDLCDevInboxPrompt, "Finally, call execute_tasks with that exact task ID")
	require.Contains(t, githubSDLCDevInboxPrompt, "Do not leave approved implementation work in Backlog")
	require.Contains(t, githubSDLCDevInboxPrompt, "Do not post status comments on GitHub issues")
	require.NotContains(t, githubSDLCDevInboxPrompt, "github_comment_on_issue")
	require.Contains(t, githubSDLCDevInboxPrompt, "github_open_pull_request")
	require.Contains(t, githubSDLCImplementationPrompt, "Supply pr_body with a concise factual Markdown summary of the changes and validation")
	require.Contains(t, githubSDLCImplementationPrompt, "Closes #<source issue number>")
	require.Contains(t, githubSDLCImplementationPrompt, "Do not include task IDs, product or automation boilerplate, or process narration")
	for name, prompt := range map[string]string{
		"vision suggestions":  githubSDLCOfferingManagerPrompt,
		"bug finder":          githubSDLCBugFinderPrompt,
		"optimization finder": githubSDLCOptimizationFinderPrompt,
		"redundancy finder":   githubSDLCRedundancyFinderPrompt,
	} {
		require.Contains(t, prompt, "## Summary", "%s output must open with a readable summary", name)
		require.Contains(t, prompt, "Technical detail is useful and must not be omitted", name)
		require.Contains(t, prompt, "Write this for a product user", name)
		require.Contains(t, prompt, "Pass the required category label", name)
		require.Contains(t, prompt, "github_list_existing_automation_issues", name)
		require.Contains(t, prompt, "skip that candidate and keep searching", name)
		require.Contains(t, prompt, "Try to create at most one new GitHub issue this run", name)
		require.Contains(t, prompt, "Only call `github_create_issue` after you believe the finding is not already represented", name)
		require.NotContains(t, prompt, "idempotency_key", name)
		require.NotContains(t, prompt, "Do not list, search, or inspect existing GitHub issues for duplicate detection", name)
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
	require.Contains(t, prompt, "Always call github_get_project_inbox and call github_list_assigned_issues for every returned Authorized User")
	require.Contains(t, prompt, "also call github_list_my_assigned_issues")
	require.Contains(t, prompt, "Use the listed assigned issues as compact body-free discovery data")
	require.Contains(t, prompt, "call github_get_issue only for the specific listed issue that needs body or acceptance-note details")
	require.Contains(t, prompt, "Deduplicate issues by repository plus issue number")
	require.Contains(t, prompt, "or from a GitHub remote in its local checkout when that URL is blank")
	require.NotContains(t, prompt, "restricted to this project's explicit repository URL")
	require.Contains(t, prompt, "Set source_github_issue_number to the exact issue number returned by this inbox execution")
	require.Contains(t, prompt, "The task prompt must include the GitHub issue number, URL, title, body or acceptance notes, relevant labels, assignment context")
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
		runAt      string
	}{
		"vision_suggestions":  {repeatType: string(models.RepeatDaily), interval: 1, runAt: "09:00"},
		"bug_finder":          {repeatType: string(models.RepeatDaily), interval: 1, runAt: "09:00"},
		"optimization_finder": {repeatType: string(models.RepeatDaily), interval: 1, runAt: "09:00"},
		"redundancy_finder":   {repeatType: string(models.RepeatDaily), interval: 1, runAt: "09:00"},
		"dev_inbox":           {repeatType: string(models.RepeatDaily), interval: 1, runAt: "10:00"},
	}
	for nodeKey, prompt := range promptsByNode {
		node := automationDraftNodeByKey(t, candidate, nodeKey)
		require.Equal(t, prompt, node.Config["prompt"], "%s must use the Automation template prompt", nodeKey)
		require.Equal(t, prompt, automationCompiledTaskPrompt(candidate, node), "%s must persist the exact Automation template prompt without custom-graph additions", nodeKey)
		require.NotContains(t, node.Config["prompt"], "references/dev-inbox-execution-invariants.md")
		require.NotContains(t, node.Config["prompt"], "repository-wide current issue listing or search")
		if nodeKey != "dev_inbox" {
			require.Contains(t, node.Config["prompt"], "github_list_existing_automation_issues")
			require.Contains(t, node.Config["prompt"], "keep searching")
		}
		require.Equal(t, expectedCadence[nodeKey].repeatType, node.Config["repeat_type"], "%s cadence must match the maintained template", nodeKey)
		require.EqualValues(t, expectedCadence[nodeKey].interval, node.Config["repeat_interval"])
		require.Equal(t, expectedCadence[nodeKey].runAt, node.Config["run_at"], "%s run_at must match the maintained template", nodeKey)
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
	require.Contains(t, issueCodes(svc.ValidateCandidate(augmentedAgent)), "unknown_config", "ordinary Agent Task nodes must reject deprecated skill and source-file config")

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

	var inboxNode models.AutomationDraftNode
	for _, node := range candidate.Nodes {
		if node.Key == "inbox" {
			inboxNode = node
			break
		}
	}
	prompt := automationCompiledTaskPrompt(candidate, inboxNode)
	require.Contains(t, prompt, "Use the listed assigned issues as compact body-free discovery data")
	require.Contains(t, prompt, "call github_get_issue only for the specific listed issue that needs body or acceptance-note details")
	require.Contains(t, prompt, "Use the provided GitHub runtime tools as the only source for inbox discovery")
	require.Contains(t, prompt, "do not use local shell commands, gh, Python scripts, curl, or direct GitHub API calls")
	require.Contains(t, prompt, "perform one existing-task lookup with list_tasks using the issue number or URL as the query")
	require.Contains(t, prompt, "Treat that single lookup result as the reconciliation result for that issue")
	require.Contains(t, prompt, "Do not retry the same issue lookup")
	require.Contains(t, prompt, "do not vary task lifecycle filters to search again")
	require.Contains(t, prompt, "do not run title or fragment searches for the same issue")
	require.NotContains(t, prompt, "Use a different query such as the exact proposed task title")
	require.NotContains(t, prompt, "issue-number/URL matching is inconclusive")
	require.NotContains(t, prompt, "A returned filter object only echoes")
	require.NotContains(t, prompt, "runtime defaults")
	require.Contains(t, prompt, "After create_task succeeds for a newly created Active task, do not call list_tasks again for that issue")

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

	svc := NewAutomationDraftService(repository.NewAutomationRepo(db), NewAutomationAdapterRegistry())
	svc.SetCapabilitySnapshotBuilder(capabilities)
	candidate, err := svc.TemplateCandidate(AutomationAdapterVisionDriver)
	require.NoError(t, err)
	driverIndex := automationDraftNodeIndexByKey(t, candidate, "vision_driver")
	candidate.Nodes[driverIndex].Config["agent_ref"] = "project_architect"
	require.Empty(t, svc.ValidateCandidateWithCapabilities(candidate, snapshot))

	candidate.Nodes[driverIndex].Config["skills"] = []any{"project_architect:project-guidance"}
	candidate.Nodes[driverIndex].Config["source_files"] = []any{"VISION.md"}
	normalized, err := svc.NormalizeCandidate(candidate)
	require.NoError(t, err)
	normalizedDriver := automationDraftNodeByKey(t, normalized, "vision_driver")
	require.NotContains(t, normalizedDriver.Config, "skills")
	require.NotContains(t, normalizedDriver.Config, "source_files")
	require.Empty(t, svc.ValidateCandidateWithCapabilities(normalized, snapshot))

	candidate.Nodes[driverIndex].Config["agent_ref"] = "foreign_architect"
	delete(candidate.Nodes[driverIndex].Config, "skills")
	delete(candidate.Nodes[driverIndex].Config, "source_files")
	issues := svc.ValidateCandidateWithCapabilities(candidate, snapshot)
	require.Contains(t, issueCodes(issues), "agent_ref")
	preview, err := svc.PreviewCandidate(ctx, project.ID, candidate, nil)
	require.NoError(t, err)
	require.Contains(t, issueCodes(preview.ValidationErrors), "agent_ref", "unresolved references must remain visible without being guessed")
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

func TestAutomationCandidateWithoutNodeDeepCopiesMutableData(t *testing.T) {
	position := &models.AutomationDraftPoint{X: 10, Y: 20}
	source := models.AutomationDraftCandidate{
		Nodes: []models.AutomationDraftNode{
			{
				Key:      "kept",
				Config:   map[string]any{"list": []string{"original"}, "nested": map[string]any{"values": []any{"original"}}},
				Position: position,
			},
			{Key: "removed", Config: map[string]any{}},
		},
		Edges: []models.AutomationDraftEdge{
			{Key: "kept_edge", From: "kept", To: "kept", Condition: map[string]any{"nested": map[string]any{"value": "original"}}},
			{Key: "removed_edge", From: "removed", To: "kept"},
		},
		Assumptions: []string{"original assumption"},
		Warnings:    []string{"original warning"},
	}

	reduced := automationCandidateWithoutNode(source, "removed")
	reduced.Nodes[0].Config["list"].([]string)[0] = "changed"
	reduced.Nodes[0].Config["nested"].(map[string]any)["values"].([]any)[0] = "changed"
	reduced.Nodes[0].Position.X = 99
	reduced.Edges[0].Condition["nested"].(map[string]any)["value"] = "changed"
	reduced.Assumptions[0] = "changed assumption"
	reduced.Warnings[0] = "changed warning"

	require.Equal(t, []string{"original"}, source.Nodes[0].Config["list"])
	require.Equal(t, "original", source.Nodes[0].Config["nested"].(map[string]any)["values"].([]any)[0])
	require.Equal(t, float64(10), source.Nodes[0].Position.X)
	require.Equal(t, "original", source.Edges[0].Condition["nested"].(map[string]any)["value"])
	require.Equal(t, []string{"original assumption"}, source.Assumptions)
	require.Equal(t, []string{"original warning"}, source.Warnings)
}

func automationCandidateWithoutNode(candidate models.AutomationDraftCandidate, key string) models.AutomationDraftCandidate {
	candidate = cloneAutomationDraftCandidate(candidate)
	nodes := make([]models.AutomationDraftNode, 0, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		if node.Key != key {
			nodes = append(nodes, node)
		}
	}
	edges := make([]models.AutomationDraftEdge, 0, len(candidate.Edges))
	for _, edge := range candidate.Edges {
		if edge.From != key && edge.To != key {
			edges = append(edges, edge)
		}
	}
	candidate.Nodes = nodes
	candidate.Edges = edges
	return candidate
}

func cloneAutomationDraftCandidate(candidate models.AutomationDraftCandidate) models.AutomationDraftCandidate {
	cloned := candidate
	cloned.Nodes = make([]models.AutomationDraftNode, len(candidate.Nodes))
	for i, node := range candidate.Nodes {
		cloned.Nodes[i] = node
		cloned.Nodes[i].Config = cloneAutomationDraftMap(node.Config)
		if node.Position != nil {
			position := *node.Position
			cloned.Nodes[i].Position = &position
		}
	}
	cloned.Edges = make([]models.AutomationDraftEdge, len(candidate.Edges))
	for i, edge := range candidate.Edges {
		cloned.Edges[i] = edge
		cloned.Edges[i].Condition = cloneAutomationDraftMap(edge.Condition)
	}
	cloned.Assumptions = append([]string(nil), candidate.Assumptions...)
	cloned.Warnings = append([]string(nil), candidate.Warnings...)
	return cloned
}

func cloneAutomationDraftMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneAutomationDraftValue(value)
	}
	return cloned
}

func cloneAutomationDraftValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneAutomationDraftMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, item := range typed {
			cloned[i] = cloneAutomationDraftValue(item)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}

func automationCandidateWithoutEdge(candidate models.AutomationDraftCandidate, key string) models.AutomationDraftCandidate {
	edges := make([]models.AutomationDraftEdge, 0, len(candidate.Edges))
	for _, edge := range candidate.Edges {
		if edge.Key != key {
			edges = append(edges, edge)
		}
	}
	candidate.Edges = edges
	return candidate
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

func customFixedScheduleCandidate(t *testing.T, drafts *AutomationDraftService) models.AutomationDraftCandidate {
	t.Helper()
	candidate, err := drafts.BlankCandidate(AutomationAdapterCustom)
	require.NoError(t, err)
	candidate.Nodes = []models.AutomationDraftNode{
		{Key: "schedule", Name: "Schedule", Type: models.AutomationNodeTrigger, Role: "fixed_schedule", Config: map[string]any{"prompt": "Run the scheduled work.", "category": "scheduled", "priority": 2, "run_at": "09:00", "repeat_type": "daily", "repeat_interval": 1, "enabled": true, "clear_context_on_start": true}},
	}
	candidate.Edges = nil
	return candidate
}

func validationIssueByNodeAndCode(t *testing.T, issues []models.AutomationValidationIssue, nodeKey, code string) models.AutomationValidationIssue {
	t.Helper()
	for _, issue := range issues {
		if issue.NodeKey == nodeKey && issue.Code == code {
			return issue
		}
	}
	t.Fatalf("no %s issue for %s in %#v", code, nodeKey, issues)
	return models.AutomationValidationIssue{}
}

func issueCodes(issues []models.AutomationValidationIssue) []string {
	codes := make([]string, len(issues))
	for i := range issues {
		codes[i] = issues[i].Code
	}
	return codes
}

func TestMaintainedSDLCTemplatesDecodeCanonicalYAML(t *testing.T) {
	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	for _, adapterKey := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
		t.Run(adapterKey, func(t *testing.T) {
			document, ok := maintainedAutomationTemplateYAML(adapterKey)
			require.True(t, ok)
			expected, err := DecodeAutomationDraftYAML([]byte(document))
			require.NoError(t, err)
			actual, err := drafts.TemplateCandidate(adapterKey)
			require.NoError(t, err)
			require.Equal(t, expected, actual)
			require.Empty(t, drafts.ValidateCandidate(actual))
		})
	}
}

func TestAutomationDraftYAMLPositionlessCustomNodesReceiveDeterministicLayout(t *testing.T) {
	raw := `schema_version: 1
name: Positionless custom graph
description: ''
automation_type: custom
adapter_key: custom
nodes:
  - key: task
    name: Follow up
    type: agent_task
    role: task
    config:
      prompt: Follow up on the request.
      category: backlog
      priority: 2
  - key: complete
    name: Complete
    type: outcome
    role: completed
    config: {}
edges:
  - key: task_complete
    from: task
    to: complete
    from_port: right
    to_port: left
`
	candidate, err := DecodeAutomationDraftYAML([]byte(raw))
	require.NoError(t, err)

	drafts := NewAutomationDraftService(nil, NewAutomationAdapterRegistry())
	preview, err := drafts.PreviewCandidate(context.Background(), "", candidate, nil)
	require.NoError(t, err)
	require.Empty(t, preview.ValidationErrors)
	for _, node := range preview.Candidate.Nodes {
		require.NotNil(t, node.Position, node.Key)
	}
	for _, edge := range preview.Candidate.Edges {
		require.NotNil(t, automationDraftNodeByKey(t, preview.Candidate, edge.From).Position, edge.Key)
		require.NotNil(t, automationDraftNodeByKey(t, preview.Candidate, edge.To).Position, edge.Key)
	}

	repeated, err := drafts.PreviewCandidate(context.Background(), "", candidate, nil)
	require.NoError(t, err)
	require.Equal(t, preview.Candidate, repeated.Candidate)
}

func TestAutomationDraftYAMLRoundTripAndStrictDecoding(t *testing.T) {
	repo := repository.NewAutomationRepo(testutil.NewTestDB(t))
	drafts := NewAutomationDraftService(repo, NewAutomationAdapterRegistry())
	for _, adapter := range []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC} {
		candidate, err := drafts.TemplateCandidate(adapter)
		require.NoError(t, err)
		first, err := EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)
		second, err := EncodeAutomationDraftYAML(candidate)
		require.NoError(t, err)
		require.Equal(t, first, second)

		decoded, err := DecodeAutomationDraftYAML([]byte(first))
		require.NoError(t, err)
		require.Equal(t, candidate.Name, decoded.Name)
		require.Equal(t, candidate.AdapterKey, decoded.AdapterKey)
		require.Len(t, decoded.Nodes, len(candidate.Nodes))
		require.Len(t, decoded.Edges, len(candidate.Edges))
		require.IsType(t, int(0), automationDraftNodeByKey(t, decoded, "vision_suggestions").Config["priority"])
	}

	for _, document := range []string{
		"name: duplicate\nname: duplicate\n",
		"schema_version: 1\nunknown: value\n",
		"schema_version: 1\nname: x\ndescription: ''\nautomation_type: custom\nadapter_key: custom\nnodes: &nodes []\nedges: *nodes\n",
	} {
		_, err := DecodeAutomationDraftYAML([]byte(document))
		require.Error(t, err)
	}
	invalidTopology, err := DecodeAutomationDraftYAML([]byte("schema_version: 1\nname: x\ndescription: ''\nautomation_type: custom\nadapter_key: custom\nnodes: []\nedges:\n  - key: dangling\n    from: missing\n    to: missing\n"))
	require.NoError(t, err)
	require.NotEmpty(t, drafts.ValidateCandidate(invalidTopology))
}

func requireNoLoopAuditorNode(t *testing.T, candidate models.AutomationDraftCandidate) {
	t.Helper()
	for _, node := range candidate.Nodes {
		require.NotEqual(t, "auditor", node.Key)
		require.NotEqual(t, "loop_auditor", node.Role)
		require.NotEqual(t, "Loop Auditor", node.Name)
	}
}
