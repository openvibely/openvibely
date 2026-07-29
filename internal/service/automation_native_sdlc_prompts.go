package service

import (
	"fmt"
	"strings"
)

// These prompts are owned by the maintained Native SDLC Automation template.
// They intentionally mirror the behavior of the bootstrap skill shipped when
// the template was defined, but template execution does not depend on that
// skill being installed or retained.
const nativeSDLCVisionSuggestionsPrompt = `Choose one focused project component or workflow to inspect this run. Compare that area with the configured project vision or source-of-truth files and identify small, reviewable gaps. Vary the component over time instead of repeatedly auditing the same files.

Do not modify code and do not create implementation tasks. Do not list, search, or inspect GitHub issues for duplicate detection. Native notification idempotency is the duplicate-prevention boundary: for each actionable finding, call create_notification with a generic type such as product_suggestion, a concise title and message, a detailed body with evidence, scope, risk, and acceptance criteria, structured metadata identifying the inspected area and evidence, and a stable idempotency key derived from the project-independent finding identity.

The notification remains pending until a human approves or rejects it on Alerts. Approval authorizes task creation only, not merge, release, or deployment.`

const nativeSDLCFinderPrompt = `Choose one focused project component or workflow to inspect this run. Vary the component over time instead of repeatedly auditing the same files.

Look only for findings in this task's scope:
- Bug Finder: likely defects, edge-case failures, broken behavior, or missing regression coverage.
- Optimization Finder: measurable performance, latency, memory, build, or workflow efficiency improvements.
- Redundancy Finder: duplicated or redundant code that could be made generic without over-engineering.

Do not modify code and do not create implementation tasks. Do not list, search, or inspect GitHub issues for duplicate detection. Native notification idempotency is the duplicate-prevention boundary: for each actionable finding, call create_notification with a generic type matching the scope, such as bug_suggestion, performance_suggestion, or maintenance_suggestion; a concise title and message; a detailed body with evidence, scope, risk, and acceptance criteria; structured metadata identifying the inspected component and evidence; and a stable idempotency key derived from the project-independent finding identity.

The notification remains pending until a human approves or rejects it on Alerts. Approval authorizes task creation only, not merge, release, or deployment.`

const NativeSDLCNotificationInboxPrompt = `Process approved actionable notifications for this scheduled task's own project.

Call list_alerts without project_id, using decision_state=approved, implementation_task_linked=false, a bounded limit, and stable pagination. The runtime automatically uses this scheduled task's persisted project. Never reuse a project ID from prior messages, examples, memory, or tool output. For each result, call get_alert and inspect the full body and metadata before claiming it.

Call claim_alert for each notification you can process. If the claim succeeds, call create_alert_implementation_task with a focused Backlog task title and prompt. Include the notification ID, reviewed context, acceptance criteria, and the rule that human approval authorized task creation only. The operation atomically links at most one task and is safe to retry after a crash.

Call complete_alert_processing after the implementation task is linked. If creation or linkage fails before a task is linked, call fail_alert_processing with a concise error so a later scan can retry. Call release_alert_claim only when no task was linked and immediate retry by another scan is appropriate.`

const nativeSDLCLoopAuditorPrompt = `Audit this project's Native SDLC loop for stale notifications, expired or failed claims, missing notification/task links, duplicate implementation work, and blocked tasks.

Inspect only project-scoped OpenVibely notification and task state. Do not list, search, or inspect GitHub issues for duplicate detection. Native notification and task state is authoritative for this loop. Report each actionable audit finding through create_notification with concrete evidence and a stable idempotency key. The auditor does not bypass approval, create or alter implementation work, merge, release, or deploy.`

func nativeSDLCRolePrompt(role string) (string, error) {
	switch strings.TrimSpace(role) {
	case "offering_manager":
		return nativeSDLCVisionSuggestionsPrompt, nil
	case "bug_finder", "optimization_finder", "redundancy_finder":
		return nativeSDLCFinderPrompt, nil
	case "native_inbox":
		return NativeSDLCNotificationInboxPrompt, nil
	case "loop_auditor":
		return nativeSDLCLoopAuditorPrompt, nil
	default:
		return "", fmt.Errorf("unsupported Native SDLC prompt role %q", role)
	}
}
