package output

import (
	"fmt"
	"regexp"
	"strings"
)

// ExtractMarker looks for a marker like "[STATUS: FAILED | reason]" in the output.
// Returns the reason text and whether the marker was found.
func ExtractMarker(output, prefix string) (string, bool) {
	idx := strings.LastIndex(output, prefix)
	if idx == -1 {
		return "", false
	}
	rest := output[idx+len(prefix):]
	end := strings.Index(rest, "]")
	if end == -1 {
		return strings.TrimSpace(rest), true
	}
	return strings.TrimSpace(rest[:end]), true
}

// Pre-compiled regexes for cleanChatOutput — compiled once at package init
// instead of on every call for better performance.
var (
	reCleanStatus            = regexp.MustCompile(`\[STATUS:\s*(?:SUCCESS|FAILED|NEEDS_FOLLOWUP)(?:\s*\|[^\]]*)?\]`)
	reCleanTool              = regexp.MustCompile(`\[Using tool:\s*[^\]]+\]`)
	reCleanThinking          = regexp.MustCompile(`(?s)\[Thinking\].*?\[/Thinking\]`)
	reCleanToolResult        = regexp.MustCompile(`(?s)\[Tool\s+\S+\s+(?:done|error)\]\n.*?\[/Tool\]\n?`)
	reCleanToolResultLegacy  = regexp.MustCompile(`\[Tool\s+\S+\s+(?:done|error):[^\n]*\]\n?`)
	reCleanProtocolArtifact  = regexp.MustCompile(`(?m)^[}\s{]*(?:to=)?multi_tool_use\.\S+[^\n]*$`)
	reCleanCreate            = regexp.MustCompile(`(?s)\[CREATE_TASK\].*?\[/CREATE_TASK\]`)
	reCleanEdit              = regexp.MustCompile(`(?s)\[EDIT_TASK\].*?\[/EDIT_TASK\]`)
	reCleanExec              = regexp.MustCompile(`(?s)\[EXECUTE_TASKS\].*?\[/EXECUTE_TASKS\]`)
	reCleanViewChat          = regexp.MustCompile(`(?s)\[VIEW_TASK_CHAT\].*?\[/VIEW_TASK_CHAT\]`)
	reCleanSendTask          = regexp.MustCompile(`(?s)\[SEND_TO_TASK\].*?\[/SEND_TO_TASK\]`)
	reCleanScheduleTask      = regexp.MustCompile(`(?s)\[SCHEDULE_TASK\].*?\[/SCHEDULE_TASK\]`)
	reCleanDeleteSchedule    = regexp.MustCompile(`(?s)\[DELETE_SCHEDULE\].*?\[/DELETE_SCHEDULE\]`)
	reCleanModifySchedule    = regexp.MustCompile(`(?s)\[MODIFY_SCHEDULE\].*?\[/MODIFY_SCHEDULE\]`)
	reCleanListPersonalities = regexp.MustCompile(`\[LIST_PERSONALITIES\]`)
	reCleanSetPersonality    = regexp.MustCompile(`(?s)\[SET_PERSONALITY\].*?\[/SET_PERSONALITY\]`)
	reCleanListModels        = regexp.MustCompile(`\[LIST_MODELS\]`)
	reCleanViewSettings      = regexp.MustCompile(`\[VIEW_SETTINGS\]`)
	reCleanProjectInfo       = regexp.MustCompile(`\[PROJECT_INFO\]`)
	reCleanListAgents        = regexp.MustCompile(`\[LIST_AGENTS\]`)
	reCleanListAlerts        = regexp.MustCompile(`\[LIST_ALERTS\]`)
	reCleanCreateAlert       = regexp.MustCompile(`(?s)\[CREATE_ALERT\].*?\[/CREATE_ALERT\]`)
	reCleanDeleteAlert       = regexp.MustCompile(`(?s)\[DELETE_ALERT\].*?\[/DELETE_ALERT\]`)
	reCleanToggleAlert       = regexp.MustCompile(`(?s)\[TOGGLE_ALERT\].*?\[/TOGGLE_ALERT\]`)
	reCleanListProjects      = regexp.MustCompile(`\[LIST_PROJECTS\]`)
	reCleanSwitchProject     = regexp.MustCompile(`(?s)\[SWITCH_PROJECT\].*?\[/SWITCH_PROJECT\]`)
	reCleanSummary           = regexp.MustCompile(`(?s)\n---\n(?:Created \d+ task|Edited \d+ task|Failed to (?:create|edit) \d+ task|Task Execution Results|Thread Messages|Schedule Results|Schedule Delete Results|Schedule Modify Results|Available Personalities|Personality Settings|Configured Models|Model Settings|App Settings|Project Info|Alert Results|Alert Create Results|Alert Delete Results|Configured Agents|Alert Toggle Results|Available Projects|Project Switch Results|\*\*Thread history for task|Could not find task|Error retrieving thread for task).*`)
	reCleanTaskID            = regexp.MustCompile(`\[TASK_ID:[^\]]+\]`)
	reCleanEdited            = regexp.MustCompile(`\[TASK_EDITED:[^\]]+\]`)
)

// nonZeroExitCodeRe matches common patterns for non-zero exit codes in agent output.
// Examples: "exit code 1", "exited with code 127", "Exit status: 2"
var nonZeroExitCodeRe = regexp.MustCompile(`(?i)exit(?:ed with)?\s+(?:code|status)[:\s]+([1-9]\d*)`)

// DetectToolFailures scans agent output for signs that tool executions failed
// (e.g., non-zero exit codes from bash commands). Returns a reason string if
// failures are detected, empty string otherwise.
func DetectToolFailures(output string) string {
	matches := nonZeroExitCodeRe.FindAllStringSubmatch(output, -1)
	if len(matches) > 0 {
		// Use the last match (most recent failure)
		lastMatch := matches[len(matches)-1]
		return fmt.Sprintf("command exited with code %s", lastMatch[1])
	}
	return ""
}

// IsImageMediaType returns true if the media type is a supported image type
// for multimodal API calls (Anthropic vision).
func IsImageMediaType(mediaType string) bool {
	return strings.HasPrefix(mediaType, "image/")
}

// CleanChatOutputForDisplay strips marker syntax from output but preserves
// the summary/result sections appended by processChatMarkers/processChatResponse.
// Used for Telegram display where marker results (Project Info, task summaries, etc.)
// should be visible to the user.
func CleanChatOutputForDisplay(output string) string {
	return doCleanChatOutput(output, false)
}

// CleanChatOutput strips thinking blocks, status markers, and other internal
// markers from assistant output so they don't clutter conversation history.
func CleanChatOutput(output string) string {
	return doCleanChatOutput(output, true)
}

// doCleanChatOutput is the shared implementation for cleaning chat output.
// When stripSummaries is true, it also removes the result/summary sections
// (used for LLM history context). When false, summaries are preserved (used for display).
func doCleanChatOutput(output string, stripSummaries bool) string {
	result := output

	// Remove properly closed thinking blocks first (regex handles [Thinking]...[/Thinking])
	result = reCleanThinking.ReplaceAllString(result, "")

	// Handle remaining unclosed thinking blocks (legacy data where [/Thinking] is missing).
	// Normalize leading [Thinking] so the split below always works.
	if strings.HasPrefix(result, "[Thinking]\n") {
		result = "\n" + result
	}
	parts := strings.Split(result, "\n[Thinking]\n")
	if len(parts) > 1 {
		var cleaned []string
		cleaned = append(cleaned, parts[0])
		for _, part := range parts[1:] {
			// If there's a closing marker, take everything after it
			if closingIdx := strings.Index(part, "[/Thinking]"); closingIdx != -1 {
				after := part[closingIdx+len("[/Thinking]"):]
				cleaned = append(cleaned, strings.TrimSpace(after))
				continue
			}
			// No closing marker — heuristic: thinking ends at double-newline
			// followed by non-marker text
			idx := strings.Index(part, "\n\n")
			for idx != -1 && idx+2 < len(part) {
				rest := part[idx+2:]
				if strings.HasPrefix(rest, "[Thinking]") || strings.HasPrefix(rest, "[Using tool:") {
					nextIdx := strings.Index(rest, "\n\n")
					if nextIdx != -1 {
						idx = idx + 2 + nextIdx
					} else {
						idx = -1
					}
					continue
				}
				cleaned = append(cleaned, strings.TrimSpace(rest))
				break
			}
		}
		result = strings.Join(cleaned, "\n")
	}

	// Remove markers using pre-compiled regexes (see package-level vars)
	result = reCleanStatus.ReplaceAllString(result, "")
	result = reCleanTool.ReplaceAllString(result, "")
	result = reCleanToolResult.ReplaceAllString(result, "")
	result = reCleanToolResultLegacy.ReplaceAllString(result, "")
	result = reCleanProtocolArtifact.ReplaceAllString(result, "")
	result = reCleanCreate.ReplaceAllString(result, "")
	result = reCleanEdit.ReplaceAllString(result, "")
	result = reCleanExec.ReplaceAllString(result, "")
	result = reCleanViewChat.ReplaceAllString(result, "")
	result = reCleanSendTask.ReplaceAllString(result, "")
	result = reCleanScheduleTask.ReplaceAllString(result, "")
	result = reCleanDeleteSchedule.ReplaceAllString(result, "")
	result = reCleanModifySchedule.ReplaceAllString(result, "")
	result = reCleanListPersonalities.ReplaceAllString(result, "")
	result = reCleanSetPersonality.ReplaceAllString(result, "")
	result = reCleanListModels.ReplaceAllString(result, "")
	result = reCleanViewSettings.ReplaceAllString(result, "")
	result = reCleanProjectInfo.ReplaceAllString(result, "")
	result = reCleanListAgents.ReplaceAllString(result, "")
	result = reCleanListAlerts.ReplaceAllString(result, "")
	result = reCleanCreateAlert.ReplaceAllString(result, "")
	result = reCleanDeleteAlert.ReplaceAllString(result, "")
	result = reCleanToggleAlert.ReplaceAllString(result, "")
	result = reCleanListProjects.ReplaceAllString(result, "")
	result = reCleanSwitchProject.ReplaceAllString(result, "")
	if stripSummaries {
		result = reCleanSummary.ReplaceAllString(result, "")
	}
	result = reCleanTaskID.ReplaceAllString(result, "")
	result = reCleanEdited.ReplaceAllString(result, "")

	return strings.TrimSpace(result)
}
