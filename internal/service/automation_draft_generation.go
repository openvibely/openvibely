package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

type AutomationDescriptionGenerator func(context.Context, string) (string, error)

const automationDescriptionPrompt = `Return strict JSON only for one supported Automation draft candidate.

The JSON must use schema_version 1 and exactly these top-level fields: schema_version, name, description, automation_type, adapter_key, nodes, edges, assumptions, warnings.
Nodes use exactly these fields: key, name, type, role, config, position. Position uses exactly x and y.
Edges use exactly these fields: key, from, to, from_port, to_port, label, condition. The from and to values are node keys. Never use source or target as edge field names.
Condition uses only state when the supported handoff below requires it. Omit optional fields rather than inventing additional fields.
Automation name must be a non-empty string of at most 200 characters. Description must be a string of at most 2000 characters. A custom graph must contain at least one node and at most 50 nodes and 100 edges.
Every node key and edge key must be non-empty and unique. Every node name must be non-empty and at most 200 characters. Each edge must reference two existing different node keys. Every edge must use from_port right and to_port left so execution runs from the source OUT port to the target IN port.

Choose the registered adapter that represents the user's request:
- Prefer adapter_key custom and automation_type custom for a user-defined graph assembled from the surfaced capabilities below. Node keys and names are user-owned.
- Use a maintained adapter only when one canonical candidate below exactly represents the user's requested lifecycle. Copy that candidate's adapter_key, automation_type, node keys, node types, node roles, and edge topology exactly. You may tailor its name, description, and supported node configuration values, but never guess or alter its topology.

Canonical maintained adapter candidates generated from the registry:
%s

Supported custom nodes and configuration:
- Every Schedule or Task priority must be an integer from 1 to 4: 1 low, 2 normal, 3 high, 4 urgent.
- Schedule: type trigger, role fixed_schedule. A Schedule is the scheduled Task. Its config owns a non-empty substantive prompt, category must be exactly scheduled, priority, optional agent_ref, run_at as HH:MM local time, repeat_type must be exactly one of once, minutes, hours, daily, weekly, monthly, repeat_interval must be an integer from 1 to 365, and enabled must be exactly true because Pause/Resume controls schedule execution for the whole Automation. Do not set target_node_key; it is derived from edges. A Schedule may perform the complete recurring job by itself or connect to supported Task, action, or Outcome capabilities. Do not add a separate Task merely to hold the recurring work.
- Task: type agent_task, role task. Its config owns a non-empty prompt, category must be exactly active or backlog, priority, and optional agent_ref selected only from the project capability snapshot. A Task may perform analysis, implementation, or other supported work. It may stand alone or connect to supported Task, action, or Outcome capabilities. Add a Task after a Schedule only when the user requests a genuinely separate follow-up step. When it is connected GitHub inbox -> Task -> Open pull request, it is a configuration-only projection for the distinct issue-linked Task created at runtime when the inbox handles an assigned issue; Save does not create that issue-specific Task. Its node name must make that projection explicit, for example "Runtime Model Support Tasks" or "Issue Task Configuration", rather than implying it is one stable implementation Task. Its category must be exactly active so human assignment starts implementation immediately; do not use another role or task type for that projection.
- Create notification: type action, role create_notification. Its config has notification_type and instructions. notification_type must be a non-empty string of at most 100 characters. instructions must be a non-empty string of at most 2000 characters. Every Create notification MUST have exactly one outgoing edge to a Human approval node, even when approval is terminal and has no Outcome branches.
- Human approval: type human_gate, role native_approval; config must be exactly approval_method native_alert.
- Approved inbox: type trigger, role native_inbox. It is itself the scheduled Task and uses the same Schedule config fields. Its prompt must perform the complete approved-notification mailbox job. Process approved notifications owned by this same Automation in the current project; connected upstream producers are sources and context, not a graph-branch eligibility limit. Runtime eligibility uses durable project + Automation + notification ownership plus this current Native inbox execution, not content similarity, model-supplied metadata, graph versions, or stable node-key chains. Call list_alerts without project_id, using decision_state=approved, implementation_task_linked=false, and bounded stable pagination. Do not pass processing_state, read, type, or source so both read states and every recovery-eligible processing state remain eligible. Inspect and claim results; create and link implementation Tasks; start each linked Task; and complete, fail, or release processing safely. Never search for or reuse a project ID from prior messages, examples, memory, tool output, the project snapshot, or the user description.
- Native implementation: type agent_task, role implementation; config must be an empty object. It is a projection stage for the real implementation Task created by Approved inbox, not a stable Task created during Save.
- Create GitHub issue: type action, role create_github_issue. Its config has instructions and labels. instructions must be a non-empty string of at most 2000 characters. labels must be an array of at most 10 non-empty plain strings of at most 100 characters without the openvibely: prefix. Never configure assignees.
- Human assignment: type human_gate, role github_assignment; config must be exactly approval_method github_assignment.
- GitHub inbox: type trigger, role github_inbox. GitHub inbox is itself the scheduled Task and uses the same Schedule config fields. Its prompt must perform the complete assigned-issue mailbox job: process open issues assigned to the PAT owner or configured GitHub Authorized Users, treating assignment as the approval signal whether the issue was created by this Automation or manually in GitHub; identify connected upstream producer stages by name and purpose as issue sources only, not as an eligibility limit; inspect and reconcile existing work; create or continue one issue-linked implementation Task per eligible approved issue using the exact issue numbers returned by the current inbox scan; and start eligible work safely. Never add a separate Schedule before it.
- Open pull request: type action, role open_pull_request. Its config has instructions, which must be a non-empty string of at most 2000 characters; base must be a string that is blank or at most 200 characters; and draft must be a boolean.
- Human review: type human_gate, role pull_request_review; config must be exactly approval_method pull_request_review.
- Outcome: type outcome, role completed; config must be an empty object.

Supported custom handoffs are deterministic capability connections, not fixed workflow recipes:
- A Schedule or Task may connect to ordinary Tasks, Create notification, Create GitHub issue, or an Outcome. A Task may fan out to several different supported targets, but may connect to at most one target of each action or Outcome kind. A Task may also stand alone.
- Each ordinary Task may have at most one Task or Schedule source because a persisted task has one parent. Schedule -> Task is an explicit downstream handoff: the scheduled Task performs its own configured work first, then OpenVibely activates the separate connected Task after successful completion. Never compile or describe this edge as the Scheduler directly targeting the connected Task.
- Create notification needs at least one Schedule or Task source and exactly one Human approval target. Human approval may be terminal or connect either or both approved/rejected results to Outcomes. Result edges use condition state approved or rejected, with at most one Outcome for each state. Multiple valid task producers may share one Create notification action.
- Native mailbox family: Schedule or Task -> Create notification -> Human approval -> Approved inbox -> Implementation -> Outcome. Human approval may also connect its rejected result to one Outcome. The approved edge uses condition state approved and the rejected edge uses condition state rejected. Approved inbox is itself the scheduled Task; never add a separate Schedule before it. It needs exactly one Human approval source and exactly one Native implementation target. Native implementation needs exactly one Outcome target.
- GitHub mailbox family: Schedule or Task -> Create GitHub issue -> Human assignment -> GitHub inbox -> Task -> Open pull request -> Human review -> Outcome. GitHub inbox is itself the scheduled mailbox Task; the downstream Task node is only the configuration/projection for issue-specific implementation Tasks that the inbox creates at runtime.
- Never combine Native mailbox nodes and GitHub mailbox nodes in one custom graph. When the user requests a mailbox and human gate, choose the complete Native family or the complete GitHub family based on where approval occurs; do not mix approval, inbox, implementation, or review stages across families.
- Create GitHub issue needs at least one Schedule or Task source and exactly one outgoing edge to Human assignment. Human assignment needs exactly one outgoing edge to GitHub inbox with condition state assigned.
- GitHub inbox needs exactly one Human assignment source. It needs exactly one Task target, and that Task must use category active, have exactly one incoming edge from the inbox, and have exactly one outgoing edge to Open pull request. The inbox prompt itself must perform the complete scheduled mailbox inspection, reconciliation, and safe issue-task creation/start work; do not add a relay Schedule or describe the projected Task as a stable Task created during Save.
- Open pull request must have exactly one incoming edge from that issue-linked Task and exactly one outgoing edge to Human review. Human review must have at least one Open pull request source. Human review must have exactly one outgoing edge to one Outcome. Outcome nodes are terminal.
- Do not add multiple task parents, create cycles, bypass a human assignment/review gate, or attach conditions to non-gate edges.

Only generate GitHub capability nodes when the supplied snapshot says GitHub is configured. Preserve human assignment, approval, pull request review, merge, release, and deployment boundaries. Do not emit database IDs, project IDs, URLs, arbitrary tools, executable code, SQL, credentials, hidden/internal capabilities, or unknown configuration fields.

If requested work depends on an external capability such as web, market-data, repository, or communication access, select agent_ref only when a listed Agent explicitly has a compatible capability. If no listed Agent has it, omit agent_ref, add an explicit warning that the capability is not shown as available and must be configured before execution, and do not claim the Task can perform that external operation.

Project capability snapshot for custom graphs:
%s

User description:
%s`

const automationDescriptionRepairPrompt = `Repair the previous Automation candidate. Return strict JSON only, with no Markdown fence or explanation. Preserve the user's intent, using either one canonical maintained adapter topology or the supported custom capability graph contract from the original request. Use only supported configuration fields and do not replace a requested custom graph with an unrelated preset.

Original request, exact schema, supported capabilities, and project snapshot:
%s

Validation failure: %s

Previous output:
%s`

type automationDescriptionProjectCapability struct {
	Name string `json:"name"`
}

type automationDescriptionCapabilitySnapshot struct {
	Project            automationDescriptionProjectCapability            `json:"project"`
	SupportedNodeTypes []models.AutomationNodeType                       `json:"supported_node_types"`
	SupportedRoles     []string                                          `json:"supported_roles"`
	Agents             []models.AutomationCapabilityRef                  `json:"agents"`
	Integrations       map[string]models.AutomationIntegrationCapability `json:"integrations"`
	SafetyBoundaries   map[string]bool                                   `json:"safety_boundaries"`
}

func customAutomationDescriptionNodeTypes() []models.AutomationNodeType {
	return []models.AutomationNodeType{
		models.AutomationNodeAction,
		models.AutomationNodeAgentTask,
		models.AutomationNodeHumanGate,
		models.AutomationNodeOutcome,
		models.AutomationNodeTrigger,
	}
}

func customAutomationDescriptionRoles() []string {
	return []string{
		"completed",
		"create_github_issue",
		"create_notification",
		"fixed_schedule",
		"github_assignment",
		"github_inbox",
		"implementation",
		"native_approval",
		"native_inbox",
		"open_pull_request",
		"pull_request_review",
		"task",
	}
}

func automationDescriptionCapabilities(snapshot models.AutomationCapabilitySnapshot) automationDescriptionCapabilitySnapshot {
	integrations := make(map[string]models.AutomationIntegrationCapability, 2)
	for _, key := range []string{"native", "github"} {
		if capability, ok := snapshot.Integrations[key]; ok {
			integrations[key] = capability
		}
	}
	safetyBoundaries := make(map[string]bool, len(snapshot.SafetyBoundaries))
	for key, enabled := range snapshot.SafetyBoundaries {
		safetyBoundaries[key] = enabled
	}
	return automationDescriptionCapabilitySnapshot{
		Project:            automationDescriptionProjectCapability{Name: snapshot.Project.Name},
		SupportedNodeTypes: customAutomationDescriptionNodeTypes(),
		SupportedRoles:     customAutomationDescriptionRoles(),
		Agents:             append([]models.AutomationCapabilityRef{}, snapshot.Agents...),
		Integrations:       integrations,
		SafetyBoundaries:   safetyBoundaries,
	}
}

func (s *AutomationDraftService) automationDescriptionMaintainedAdapters() (string, error) {
	keys := []string{AutomationAdapterNativeSDLC, AutomationAdapterGitHubSDLC}
	candidates := make([]models.AutomationDraftCandidate, 0, len(keys))
	for _, key := range keys {
		candidate, err := s.TemplateCandidate(key)
		if err != nil {
			return "", err
		}
		candidates = append(candidates, candidate)
	}
	encoded, err := json.Marshal(candidates)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (s *AutomationDraftService) PreviewDescription(ctx context.Context, description string, snapshot models.AutomationCapabilitySnapshot, generate AutomationDescriptionGenerator) (*models.AutomationDraftResult, error) {
	description = strings.TrimSpace(description)
	if description == "" {
		return nil, errors.New("automation description is required")
	}
	if len(description) > 4000 {
		return nil, errors.New("automation description exceeds 4000 characters")
	}
	if generate == nil {
		return nil, errors.New("automation description generator is unavailable")
	}
	snapshotJSON, err := json.Marshal(automationDescriptionCapabilities(snapshot))
	if err != nil {
		return nil, err
	}
	maintainedAdapters, err := s.automationDescriptionMaintainedAdapters()
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf(automationDescriptionPrompt, maintainedAdapters, string(snapshotJSON), description)
	return s.generateCandidateWithRepair(ctx, prompt, snapshot, generate)
}

func (s *AutomationDraftService) generateCandidateWithRepair(ctx context.Context, prompt string, snapshot models.AutomationCapabilitySnapshot, generate AutomationDescriptionGenerator) (*models.AutomationDraftResult, error) {
	output, err := generate(ctx, prompt)
	if err != nil {
		return nil, err
	}
	candidate, parseErr := DecodeAutomationDraftCandidate([]byte(strings.TrimSpace(output)))
	if parseErr == nil {
		candidate, parseErr = s.NormalizeCandidate(candidate)
		if parseErr == nil {
			normalizeGeneratedTaskPriorities(&candidate)
			normalizeGeneratedRequiredNativeApprovals(&candidate)
		}
	}
	var issues []models.AutomationValidationIssue
	if parseErr == nil {
		issues = s.ValidateCandidateWithCapabilities(candidate, snapshot)
		if len(issues) == 0 {
			return draftPreviewResult(candidate, nil), nil
		}
	}
	repairReason := "invalid JSON"
	if parseErr != nil {
		repairReason = parseErr.Error()
	} else if len(issues) > 0 {
		repairReason = issues[0].Code + ": " + issues[0].Message
	}
	repairPrompt := fmt.Sprintf(automationDescriptionRepairPrompt, boundedAutomationGenerationOutput(prompt), repairReason, boundedAutomationGenerationOutput(output))
	repaired, err := generate(ctx, repairPrompt)
	if err != nil {
		return nil, err
	}
	candidate, err = DecodeAutomationDraftCandidate([]byte(strings.TrimSpace(repaired)))
	if err != nil {
		return nil, fmt.Errorf("automation generation repair failed: %w", err)
	}
	candidate, err = s.NormalizeCandidate(candidate)
	if err != nil {
		return nil, err
	}
	normalizeGeneratedTaskPriorities(&candidate)
	normalizeGeneratedRequiredNativeApprovals(&candidate)
	issues = s.ValidateCandidateWithCapabilities(candidate, snapshot)
	if len(issues) > 0 {
		return nil, fmt.Errorf("automation generation repair failed: %s", issues[0].Message)
	}
	return draftPreviewResult(candidate, nil), nil
}

func normalizeGeneratedRequiredNativeApprovals(candidate *models.AutomationDraftCandidate) {
	if candidate.AdapterKey != AutomationAdapterCustom {
		return
	}

	usedNodeKeys := make(map[string]struct{}, len(candidate.Nodes))
	usedEdgeKeys := make(map[string]struct{}, len(candidate.Edges))
	nodesByKey := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	incoming := make(map[string]int, len(candidate.Nodes))
	outgoing := make(map[string][]int, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		usedNodeKeys[node.Key] = struct{}{}
		nodesByKey[node.Key] = node
	}
	for i, edge := range candidate.Edges {
		usedEdgeKeys[edge.Key] = struct{}{}
		incoming[edge.To]++
		outgoing[edge.From] = append(outgoing[edge.From], i)
	}

	for i := range candidate.Nodes {
		notification := candidate.Nodes[i]
		if notification.Type != models.AutomationNodeAction || notification.Role != "create_notification" {
			continue
		}
		outgoingEdges := outgoing[notification.Key]
		directOutcome := false
		switch len(outgoingEdges) {
		case 0:
		case 1:
			target := nodesByKey[candidate.Edges[outgoingEdges[0]].To]
			if target.Type != models.AutomationNodeOutcome {
				continue
			}
			directOutcome = true
		default:
			continue
		}

		approvalKey := ""
		for _, node := range candidate.Nodes {
			if node.Type == models.AutomationNodeHumanGate && node.Role == "native_approval" && incoming[node.Key] == 0 {
				if approvalKey != "" {
					approvalKey = ""
					break
				}
				approvalKey = node.Key
			}
		}
		if approvalKey == "" {
			base := notification.Key + "_approval"
			approvalKey = base
			for suffix := 2; ; suffix++ {
				if _, exists := usedNodeKeys[approvalKey]; !exists {
					break
				}
				approvalKey = fmt.Sprintf("%s_%d", base, suffix)
			}
			var position *models.AutomationDraftPoint
			if notification.Position != nil {
				position = &models.AutomationDraftPoint{X: notification.Position.X + 280, Y: notification.Position.Y}
			}
			candidate.Nodes = append(candidate.Nodes, models.AutomationDraftNode{
				Key: approvalKey, Name: "Human approval", Type: models.AutomationNodeHumanGate, Role: "native_approval",
				Config: map[string]any{"approval_method": "native_alert"}, Position: position,
			})
			usedNodeKeys[approvalKey] = struct{}{}
		}

		if directOutcome {
			edge := &candidate.Edges[outgoingEdges[0]]
			edge.From = approvalKey
			state, validState := customAutomationEdgeConditionState(edge.Condition)
			if !validState || (state != "approved" && state != "rejected") {
				edge.Condition = map[string]any{"state": "approved"}
			}
		}

		edgeBase := notification.Key + "_approval"
		edgeKey := edgeBase
		for suffix := 2; ; suffix++ {
			if _, exists := usedEdgeKeys[edgeKey]; !exists {
				break
			}
			edgeKey = fmt.Sprintf("%s_%d", edgeBase, suffix)
		}
		candidate.Edges = append(candidate.Edges, models.AutomationDraftEdge{
			Key: edgeKey, From: notification.Key, To: approvalKey, FromPort: "right", ToPort: "left", Condition: map[string]any{},
		})
		usedEdgeKeys[edgeKey] = struct{}{}
		incoming[approvalKey]++
		outgoing[notification.Key] = append(outgoing[notification.Key], len(candidate.Edges)-1)
	}
}

func normalizeGeneratedTaskPriorities(candidate *models.AutomationDraftCandidate) {
	for i := range candidate.Nodes {
		node := &candidate.Nodes[i]
		if node.Type != models.AutomationNodeTrigger && node.Type != models.AutomationNodeAgentTask {
			continue
		}
		priority, ok := draftInt(node.Config["priority"])
		if !ok {
			continue
		}
		switch {
		case priority < 1:
			node.Config["priority"] = 1
		case priority > 4:
			node.Config["priority"] = 4
		}
	}
}

func boundedAutomationGenerationOutput(output string) string {
	if len(output) > maxAutomationDraftBytes {
		return output[:maxAutomationDraftBytes]
	}
	return output
}

func draftPreviewResult(candidate models.AutomationDraftCandidate, definition *models.AutomationDefinition) *models.AutomationDraftResult {
	return &models.AutomationDraftResult{
		Definition: definition, Candidate: candidate, Assumptions: candidate.Assumptions,
		Warnings: candidate.Warnings, Summary: automationDraftSummary(candidate),
	}
}
