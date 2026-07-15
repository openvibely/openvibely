package output

import (
	"fmt"
	"regexp"
	"strings"

	llmtranscript "github.com/openvibely/openvibely/internal/llm/transcript"
)

// ExtractMarker looks for a complete terminal reason-bearing marker like
// "[STATUS: FAILED | reason]" in the output. Returns the non-empty reason text
// and whether the marker was found.
//
// Status markers are control syntax, so only a complete final standalone
// non-empty line is accepted. Mentions in prose, malformed or incomplete forms,
// examples, bullets, quotes, or code fences are not terminal status reports.
func ExtractMarker(output, prefix string) (string, bool) {
	line, _, ok := finalStandaloneLine(output)
	if !ok || !strings.HasPrefix(line, prefix) {
		return "", false
	}

	rest := line[len(prefix):]
	end := strings.Index(rest, "]")
	if end == -1 || strings.TrimSpace(rest[end+1:]) != "" {
		return "", false
	}
	reason := strings.TrimSpace(rest[:end])
	if reason == "" || strings.Contains(reason, "|") {
		return "", false
	}
	return reason, true
}

func finalStandaloneLine(output string) (string, int, bool) {
	lines := llmtranscript.MarkdownLineRanges(output)
	lastNonEmpty := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(output[lines[i].Start:lines[i].End]) != "" {
			lastNonEmpty = i
			break
		}
	}
	if lastNonEmpty == -1 {
		return "", -1, false
	}

	sourceLine := lines[lastNonEmpty]
	rawLine := output[sourceLine.Start:sourceLine.End]
	line := strings.TrimSpace(rawLine)
	markerStart := sourceLine.Start + strings.Index(rawLine, line)
	if positionInAnyRange(markdownCodeRanges(output), markerStart) {
		return "", -1, false
	}

	return line, lastNonEmpty, true
}

// StripFinalStatusControl removes a complete canonical status control only when
// it is the final standalone non-empty line. Marker-shaped prose, examples,
// bullets, quotes, fenced content, and non-final lines remain visible.
func StripFinalStatusControl(output string) string {
	line, lastNonEmpty, ok := finalStandaloneLine(output)
	if !ok || !isCanonicalStatusLine(line) {
		return output
	}
	lines := llmtranscript.MarkdownLineRanges(output)
	statusLine := lines[lastNonEmpty]
	removeStart := statusLine.Start
	removeEnd := statusLine.Next
	if statusLine.End == len(output) && lastNonEmpty > 0 {
		removeStart = lines[lastNonEmpty-1].End
	}
	if removeEnd > len(output) {
		removeEnd = len(output)
	}
	return output[:removeStart] + output[removeEnd:]
}

func isCanonicalStatusLine(line string) bool {
	if line == "[STATUS: SUCCESS]" {
		return true
	}
	for _, prefix := range []string{"[STATUS: FAILED |", "[STATUS: NEEDS_FOLLOWUP |"} {
		if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, "]") {
			continue
		}
		reason := strings.TrimSpace(line[len(prefix) : len(line)-1])
		return reason != "" && !strings.ContainsAny(reason, "]|")
	}
	return false
}

// Pre-compiled regexes for cleanChatOutput — compiled once at package init
// instead of on every call for better performance.
var (
	reCleanTool             = regexp.MustCompile(`\[Using tool:\s*[^\]]+\]`)
	reCleanProposedPlanTag  = regexp.MustCompile(`(?i)</?\s*proposed_plan\s*>`)
	reCleanThinking         = regexp.MustCompile(`(?s)\[Thinking\].*?\[/Thinking\]`)
	reCleanToolResult       = regexp.MustCompile(`(?s)\[Tool\s+\S+\s+(?:done|error)\](?:\r\n|\r|\n)?.*?(?:\r\n|\r|\n)?\[/Tool\](?:\r\n|\r|\n)?`)
	reCleanToolResultLegacy = regexp.MustCompile(`\[Tool\s+\S+\s+(?:done|error):[^\]\r\n]*\](?:\r\n|\r|\n)?`)
	reCleanProtocolArtifact = regexp.MustCompile(`(?m)(^|(?:\r\n|\r|\n))[}\t {]*(?:to=)?multi_tool_use\.\S+[^\r\n]*(?:(\r\n|\r|\n)|$)`)
	reCleanSummary          = regexp.MustCompile(`(?s)(?:\r\n|\r|\n)---(?:\r\n|\r|\n)(?:Created \d+ task|Edited \d+ task|Failed to (?:create|edit) \d+ task|Task Execution Results|Thread Messages|Schedule Results|Schedule Delete Results|Schedule Modify Results|Available Personalities|Personality Settings|Configured Models|Model Settings|App Settings|Project Info|Alert Results|Alert Create Results|Alert Delete Results|Configured Agents|Alert Toggle Results|Available Projects|Project Switch Results|\*\*Thread history for task|Could not find task|Error retrieving thread for task).*`)
	reCleanTaskID           = regexp.MustCompile(`\[TASK_ID:[^\]]+\]`)
	reCleanEdited           = regexp.MustCompile(`\[TASK_EDITED:[^\]]+\]`)
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

// CleanChatOutputForDisplay strips internal transcript control syntax while
// preserving historical summary/result sections that should remain user-visible on
// channel surfaces.
func CleanChatOutputForDisplay(output string) string {
	return doCleanChatOutput(output, false)
}

// CleanChatOutput strips thinking blocks, status markers, and other internal
// markers from assistant output so they don't clutter conversation history.
func CleanChatOutput(output string) string {
	return doCleanChatOutput(output, true)
}

// ReplaceOutsideMarkdownCode applies re as a global replacement with repl but
// skips matches whose control marker starts inside an inline code span or fenced
// code block. This preserves literal transcript-control examples without allowing
// a real control outside code to be shielded by overlapping later code.
func ReplaceOutsideMarkdownCode(s string, re *regexp.Regexp, repl string) string {
	return replaceOutsideMarkdownCode(s, re, repl, true)
}

// replaceMatchOutsideMarkdownCode applies re using the match start, rather than
// the first control marker, to decide whether a whole structured block is code.
func replaceMatchOutsideMarkdownCode(s string, re *regexp.Regexp, repl string) string {
	return replaceOutsideMarkdownCode(s, re, repl, false)
}

func replaceOutsideMarkdownCode(s string, re *regexp.Regexp, repl string, locateControlMarker bool) string {
	ranges := markdownCodeRanges(s)
	var buf strings.Builder
	previous := 0
	searchAt := 0
	for searchAt <= len(s) {
		match := re.FindStringIndex(s[searchAt:])
		if match == nil {
			break
		}
		start, end := searchAt+match[0], searchAt+match[1]
		protectedAt := start
		if locateControlMarker {
			if markerOffset := strings.IndexByte(s[start:end], '['); markerOffset >= 0 {
				protectedAt += markerOffset
			}
		}
		if positionInAnyRange(ranges, protectedAt) {
			// A protected match can span a later real control or structured block.
			// Resume just after the protected start so overlapping real content is
			// still discovered.
			searchAt = protectedAt + 1
			continue
		}
		buf.WriteString(s[previous:start])
		if protectedAt > start && positionInAnyRange(ranges, start) {
			buf.WriteString(s[start:protectedAt])
		}
		buf.WriteString(repl)
		previous = end
		searchAt = end
		if end == start {
			searchAt++
		}
	}
	buf.WriteString(s[previous:])
	return buf.String()
}

// DeduplicateTaskSummaries removes earlier real Created/Edited task summary
// blocks while preserving literal summaries inside Markdown code. The final real
// summary of each kind is retained for task-result link hydration.
func DeduplicateTaskSummaries(text string) string {
	for _, prefix := range []string{"Created ", "Edited "} {
		lines := llmtranscript.MarkdownLineRanges(text)
		ranges := markdownCodeRanges(text)
		type summaryBlock struct {
			start int
			end   int
		}
		var blocks []summaryBlock
		for i := 0; i+1 < len(lines); i++ {
			delimiter := lines[i]
			header := lines[i+1]
			if text[delimiter.Start:delimiter.End] != "---" ||
				!strings.HasPrefix(text[header.Start:header.End], prefix) ||
				positionInAnyRange(ranges, delimiter.Start) {
				continue
			}

			start := delimiter.Start
			if i > 0 {
				// Match the historical removal boundary at the line ending directly
				// before the delimiter, preserving any earlier blank separator.
				start = lines[i-1].End
			}
			end := min(header.Next, len(text))
			for j := i + 2; j < len(lines); j++ {
				line := lines[j]
				if !strings.HasPrefix(text[line.Start:line.End], "- ") {
					break
				}
				end = min(line.Next, len(text))
			}
			blocks = append(blocks, summaryBlock{start: start, end: end})
		}

		for i := len(blocks) - 2; i >= 0; i-- {
			text = text[:blocks[i].start] + text[blocks[i].end:]
		}
	}
	return text
}

// codeRange aliases the shared transcript Markdown range type so classification,
// normalization, and cleanup all use the same inline/fence grammar.
type codeRange = llmtranscript.MarkdownCodeRange

func markdownCodeRanges(s string) []codeRange {
	return llmtranscript.MarkdownCodeRanges(s)
}

func positionInAnyRange(ranges []codeRange, position int) bool {
	return llmtranscript.PositionInMarkdownCode(ranges, position)
}

func cleanUnclosedThinkingBlocks(text string) string {
	for {
		lines := llmtranscript.MarkdownLineRanges(text)
		openLine := -1
		for i, line := range lines {
			if text[line.Start:line.End] == "[Thinking]" {
				openLine = i
				break
			}
		}
		if openLine == -1 {
			return text
		}

		// Historical unclosed thinking ends at the first blank-line boundary
		// followed by ordinary response text. Marker-only sections continue the
		// internal block until a later blank boundary.
		visibleStart := len(text)
		sawBlank := false
		for i := openLine + 1; i < len(lines); i++ {
			line := lines[i]
			content := text[line.Start:line.End]
			if strings.TrimSpace(content) == "" {
				sawBlank = true
				continue
			}
			if !sawBlank {
				continue
			}
			if strings.HasPrefix(content, "[Thinking]") || strings.HasPrefix(content, "[Using tool:") {
				sawBlank = false
				continue
			}
			visibleStart = line.Start
			break
		}

		openStart := lines[openLine].Start
		text = text[:openStart] + text[visibleStart:]
	}
}

// doCleanChatOutput is the shared implementation for cleaning chat output.
// When stripSummaries is true, it also removes the result/summary sections
// (used for LLM history context). When false, summaries are preserved (used for display).
func doCleanChatOutput(output string, stripSummaries bool) string {
	result := llmtranscript.NormalizeMarkers(output)
	// Scope status handling against the original normalized message before any
	// thinking/tool cleanup can make an earlier inert status line appear final.
	result = StripFinalStatusControl(result)

	// Shield Markdown code while applying the legacy thinking cleanup below. The
	// unclosed-thinking heuristic predates Markdown-aware control replacement and
	// splits on standalone [Thinking] lines, so fenced examples must be hidden from
	// both that split and the closed-block regex, then restored verbatim.
	type protectedCode struct {
		token string
		text  string
	}
	var protected []protectedCode
	if ranges := markdownCodeRanges(result); len(ranges) > 0 {
		var masked strings.Builder
		previous := 0
		for i, r := range ranges {
			token := fmt.Sprintf("\x00openvibely-code-%d\x00", i)
			masked.WriteString(result[previous:r.Start])
			masked.WriteString(token)
			protected = append(protected, protectedCode{token: token, text: result[r.Start:r.End]})
			previous = r.End
		}
		masked.WriteString(result[previous:])
		result = masked.String()
	}

	// Remove properly closed thinking blocks first (regex handles [Thinking]...[/Thinking])
	result = reCleanThinking.ReplaceAllString(result, "")

	// Handle remaining unclosed thinking blocks (legacy data where [/Thinking] is missing).
	// Use CommonMark line boundaries so LF, CRLF, and bare CR behave identically
	// without normalizing visible source bytes.
	result = cleanUnclosedThinkingBlocks(result)
	for _, code := range protected {
		result = strings.ReplaceAll(result, code.token, code.text)
	}

	result = ReplaceOutsideMarkdownCode(result, reCleanTool, "")
	result = reCleanProposedPlanTag.ReplaceAllString(result, "")
	result = ReplaceOutsideMarkdownCode(result, reCleanToolResult, "")
	result = ReplaceOutsideMarkdownCode(result, reCleanToolResultLegacy, "")
	result = reCleanProtocolArtifact.ReplaceAllString(result, "$1$2")
	if stripSummaries {
		result = replaceMatchOutsideMarkdownCode(result, reCleanSummary, "")
	}
	result = ReplaceOutsideMarkdownCode(result, reCleanTaskID, "")
	result = ReplaceOutsideMarkdownCode(result, reCleanEdited, "")

	return strings.TrimSpace(result)
}
