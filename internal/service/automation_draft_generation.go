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
	prompt := fmt.Sprintf(`Return strict JSON only for one supported Automation draft candidate.

The JSON must match schema_version 1 with exactly these top-level fields: schema_version, name, description, automation_type, adapter_key, nodes, edges, assumptions, warnings. Choose exactly one registered adapter topology: native_sdlc, github_sdlc, or vision_driver. Node keys, types, roles, and edges must match that adapter's canonical template. Do not emit database IDs, project IDs, URLs, arbitrary tools, executable code, SQL, credentials, or unknown configuration fields. Fixed trigger config supports target_node_key, run_at, repeat_type, repeat_interval, enabled. Persisted task-node config supports prompt, category, priority. Human approval remains required and does not authorize merge, release, or deployment.

Project capability snapshot:
%s

User description:
%s`, string(snapshotJSON), description)
	return s.generateCandidateWithRepair(ctx, prompt, generate)
}

func (s *AutomationDraftService) generateCandidateWithRepair(ctx context.Context, prompt string, generate AutomationDescriptionGenerator) (*models.AutomationDraftResult, error) {
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
		issues = s.ValidateCandidate(candidate)
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
	repairPrompt := fmt.Sprintf(`Repair the previous Automation candidate. Return strict JSON only, with no Markdown fence or explanation. Preserve the user's intent but use exactly one canonical registered adapter topology and only supported configuration fields.

Validation failure: %s

Previous output:
%s`, repairReason, boundedAutomationGenerationOutput(output))
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
	issues = s.ValidateCandidate(candidate)
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
