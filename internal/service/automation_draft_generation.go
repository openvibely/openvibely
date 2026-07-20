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

Choose the registered adapter that represents the user's request:
- Use a maintained adapter, native_sdlc, github_sdlc, or vision_driver, only when its canonical topology exactly matches. Its node keys, types, roles, and edges must remain canonical.
- Use adapter_key custom and automation_type custom for a user-defined graph assembled from the surfaced capabilities below. Node keys and names are user-owned.

Supported custom nodes and configuration:
- Every Schedule or Agent task priority must be an integer from 1 to 4: 1 low, 2 normal, 3 high, 4 urgent.
- Schedule: type trigger, role fixed_schedule; this is the scheduled task, with config prompt, category scheduled, priority, optional agent_ref, target_node_key when connected, run_at in HH:MM, repeat_type, repeat_interval, enabled. It may run by itself and may connect to supported task, action, or Outcome capabilities.
- Agent task: type agent_task, role task; this is an ordinary task, with config prompt, category, priority, and optional agent_ref selected only from the project capability snapshot. Never add skills or source_files to task config. It may be a standalone ordinary task or connect to supported task, action, or Outcome capabilities. Scheduling belongs only to the Schedule node.
- Create notification: type action, role create_notification; config notification_type and instructions.
- Human approval: type human_gate, role native_approval; config approval_method native_alert.
- Create GitHub issue: type action, role create_github_issue; config instructions and labels. Never configure assignees.
- Human assignment: type human_gate, role github_assignment; config approval_method github_assignment.
- GitHub inbox: type agent_task, role github_inbox; ordinary task config with category backlog. Its connected Schedule is the scheduled task.
- Implementation task template: type agent_task, role implementation; task config with category active.
- Open pull request: type action, role open_pull_request; config instructions, base, draft.
- Human review: type human_gate, role pull_request_review; config approval_method pull_request_review.
- Outcome: type outcome, role completed; empty config.

Supported custom handoffs are deterministic capability connections, not fixed workflow recipes:
- A Schedule or Agent task may connect to ordinary Agent tasks, Create notification, Create GitHub issue, or an Outcome. A task may fan out to several different supported targets. An ordinary Agent task may also stand alone.
- Each ordinary Agent task may have at most one task or Schedule parent because a persisted task has one parent. Schedule and task categories remain the user's normal task settings; a Schedule-triggered child becomes runnable when its Schedule task completes.
- Create notification connects to Human approval. Human approval may be terminal or connect either or both approved/rejected results to Outcomes. Result edges use condition state approved or rejected, with at most one Outcome for each state. Multiple valid task producers may share one Create notification action.
- The human-gated GitHub lifecycle uses Create GitHub issue -> Human assignment and a separately scheduled GitHub inbox. Human assignment -> GitHub inbox uses condition state assigned. Continue GitHub inbox -> Implementation task template -> Open pull request -> Human review -> Outcome.
- Do not add multiple task parents, create cycles, bypass a human assignment/review gate, or attach conditions to non-gate edges.

Only generate GitHub capability nodes when the supplied snapshot says GitHub is configured. Preserve human assignment, approval, pull request review, merge, release, and deployment boundaries. Do not emit database IDs, project IDs, URLs, arbitrary tools, executable code, SQL, credentials, hidden/internal capabilities, or unknown configuration fields.

Project capability snapshot:
%s

User description:
%s`

const automationDescriptionRepairPrompt = `Repair the previous Automation candidate. Return strict JSON only, with no Markdown fence or explanation. Preserve the user's intent, using either one canonical maintained adapter topology or the supported custom capability graph contract from the original request. Use only supported configuration fields and do not replace a requested custom graph with an unrelated preset.

Original request, exact schema, supported capabilities, and project snapshot:
%s

Validation failure: %s

Previous output:
%s`

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
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}
	prompt := fmt.Sprintf(automationDescriptionPrompt, string(snapshotJSON), description)
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
	issues = s.ValidateCandidateWithCapabilities(candidate, snapshot)
	if len(issues) > 0 {
		return nil, fmt.Errorf("automation generation repair failed: %s", issues[0].Message)
	}
	return draftPreviewResult(candidate, nil), nil
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
