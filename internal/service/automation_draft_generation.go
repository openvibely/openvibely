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

Choose the registered adapter that represents the user's request:
- Use a maintained adapter, native_sdlc, github_sdlc, or vision_driver, only when its canonical topology exactly matches. Its node keys, types, roles, and edges must remain canonical.
- Use adapter_key custom and automation_type custom for a user-defined graph assembled from the surfaced capabilities below. Node keys and names are user-owned.

Supported custom nodes and configuration:
- Schedule: type trigger, role fixed_schedule; this is the scheduled task, with config prompt, category scheduled, priority, optional agent_ref, target_node_key when connected, run_at in HH:MM, repeat_type, repeat_interval, enabled. It may run by itself or hand off to one Agent task.
- Agent task: type agent_task, role task; this is an ordinary task, with config prompt, category, priority, and optional agent_ref selected only from the project capability snapshot. Never add skills or source_files to task config. An Agent task connected after a Schedule remains backlog until the Schedule task completes; a task started by another task uses active. Scheduling belongs only to the Schedule node.
- Create notification: type action, role create_notification; config notification_type and instructions.
- Human approval: type human_gate, role native_approval; config approval_method native_alert.
- Create GitHub issue: type action, role create_github_issue; config instructions and labels. Never configure assignees.
- Human assignment: type human_gate, role github_assignment; config approval_method github_assignment.
- GitHub inbox: type agent_task, role github_inbox; ordinary task config with category backlog. Its connected Schedule is the scheduled task.
- Implementation task template: type agent_task, role implementation; task config with category active.
- Open pull request: type action, role open_pull_request; config instructions, base, draft.
- Human review: type human_gate, role pull_request_review; config approval_method pull_request_review.
- Outcome: type outcome, role completed; empty config.

Supported custom handoffs are deterministic:
- A Schedule may run as a standalone scheduled task, or use Schedule -> Agent task -> zero or more linear Agent tasks -> Outcome.
- A terminal Agent task may instead use Agent task -> Create notification -> Human approval -> one approved Outcome and one rejected Outcome. Those two gate edges use condition state approved and rejected.
- The GitHub lifecycle uses producer Schedule -> Agent task -> Create GitHub issue -> Human assignment, plus inbox Schedule -> GitHub inbox. Human assignment -> GitHub inbox uses condition state assigned. Continue GitHub inbox -> Implementation task template -> Open pull request -> Human review -> Outcome.
- Do not branch Agent tasks, add multiple task parents, create cycles, bypass a human assignment/review gate, or attach conditions to other edges.

Only generate GitHub capability nodes when the supplied snapshot says GitHub is configured. Preserve human assignment, approval, pull request review, merge, release, and deployment boundaries. Do not emit database IDs, project IDs, URLs, arbitrary tools, executable code, SQL, credentials, hidden/internal capabilities, or unknown configuration fields.

Project capability snapshot:
%s

User description:
%s`

const automationDescriptionRepairPrompt = `Repair the previous Automation candidate. Return strict JSON only, with no Markdown fence or explanation. Preserve the user's intent, using either one canonical maintained adapter topology or the supported custom capability graph contract from the original request. Use only supported configuration fields and do not replace a requested custom graph with an unrelated preset.

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
	repairPrompt := fmt.Sprintf(automationDescriptionRepairPrompt, repairReason, boundedAutomationGenerationOutput(output))
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
	issues = s.ValidateCandidateWithCapabilities(candidate, snapshot)
	if len(issues) > 0 {
		return nil, fmt.Errorf("automation generation repair failed: %s", issues[0].Message)
	}
	return draftPreviewResult(candidate, nil), nil
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
