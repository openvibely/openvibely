package transcript

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	reLegacyThinkingOpenTag  = regexp.MustCompile(`(?i)<\s*thinking\s*>`)
	reLegacyThinkingCloseTag = regexp.MustCompile(`(?i)</\s*thinking\s*>`)
	reMalformedToolInvoke    = regexp.MustCompile(`(?s)\[Using tool:\s*([A-Za-z0-9_.-]+)"(?:\]|>)\s*<parameter\s+name="command">(.*?)</parameter>\s*</invoke>\s*`)
)

// NormalizeMarkers canonicalizes provider/client marker variants before
// persistence or rendering. Historical rows may contain XML-ish thinking/tool tags
// produced by older stream parsers; converting them here keeps thinking and normal
// assistant content in separate renderable sections without changing the message text.
func NormalizeMarkers(text string) string {
	if text == "" {
		return text
	}
	text = replaceOutsideMarkdownCode(text, reMalformedToolInvoke, func(match string) string {
		parts := reMalformedToolInvoke.FindStringSubmatch(match)
		if len(parts) != 3 {
			return match
		}
		tool := sanitizeMarkerPart(parts[1])
		cmd := sanitizeMarkerPart(parts[2])
		if tool == "" {
			return ""
		}
		if cmd == "" {
			return fmt.Sprintf("\n[Using tool: %s]\n", tool)
		}
		return fmt.Sprintf("\n[Using tool: %s | %s]\n", tool, cmd)
	})
	text = replaceOutsideMarkdownCode(text, reLegacyThinkingOpenTag, func(string) string { return "\n[Thinking]\n" })
	text = replaceOutsideMarkdownCode(text, reLegacyThinkingCloseTag, func(string) string { return "\n[/Thinking]\n" })
	return text
}

func replaceOutsideMarkdownCode(text string, re *regexp.Regexp, replace func(string) string) string {
	ranges := MarkdownCodeRanges(text)
	var result strings.Builder
	previous := 0
	searchAt := 0
	for searchAt <= len(text) {
		match := re.FindStringIndex(text[searchAt:])
		if match == nil {
			break
		}
		start, end := searchAt+match[0], searchAt+match[1]
		if PositionInMarkdownCode(ranges, start) {
			// A protected incomplete alias can make one regex match span a later real
			// alias. Resume just after the protected start so the later alias is found.
			searchAt = start + 1
			continue
		}
		result.WriteString(text[previous:start])
		result.WriteString(replace(text[start:end]))
		previous = end
		searchAt = end
		if end == start {
			searchAt++
		}
	}
	result.WriteString(text[previous:])
	return result.String()
}

// MarkdownLineRange is a source-preserving half-open line range. End excludes
// the line ending and Next points after an LF, CRLF, or bare CR ending.
type MarkdownLineRange struct {
	Start int
	End   int
	Next  int
}

// MarkdownLineRanges splits text using the line endings recognized by CommonMark
// without normalizing the original bytes.
func MarkdownLineRanges(text string) []MarkdownLineRange {
	var lines []MarkdownLineRange
	for start := 0; start <= len(text); {
		end, next := markdownLineEnd(text, start)
		lines = append(lines, MarkdownLineRange{Start: start, End: end, Next: next})
		if end == len(text) {
			break
		}
		start = next
	}
	return lines
}

// MarkdownCodeRange is a half-open byte range [Start, End) occupied by an
// inline Markdown code span or fenced code block.
type MarkdownCodeRange struct {
	Start int
	End   int
}

// MarkdownCodeRanges returns protected ranges for Markdown inline code spans
// and fenced code blocks. LF, CRLF, and bare CR are treated as line endings.
// Inline spans may cross line endings and close only on a backtick run exactly
// matching the opener. Fences may use at least three backticks or tildes, may
// be indented by up to three spaces, and protect the rest of the message when
// unclosed. Closing fences must use the same character and at least the opening
// delimiter length.
func MarkdownCodeRanges(text string) []MarkdownCodeRange {
	var ranges []MarkdownCodeRange
	plainStart := 0
	fenceStart := -1
	var fenceChar byte
	fenceLen := 0

	for _, sourceLine := range MarkdownLineRanges(text) {
		lineStart := sourceLine.Start
		lineEnd := sourceLine.End
		nextLine := sourceLine.Next
		line := text[lineStart:lineEnd]

		if fenceStart >= 0 {
			if isClosingCodeFence(line, fenceChar, fenceLen) {
				end := lineEnd
				if lineEnd < len(text) {
					end = nextLine
				}
				ranges = append(ranges, MarkdownCodeRange{Start: fenceStart, End: end})
				fenceStart = -1
				plainStart = end
			}
		} else if char, runLen, ok := openingCodeFence(line); ok {
			ranges = append(ranges, inlineCodeRanges(text[plainStart:lineStart], plainStart)...)
			fenceStart = lineStart
			fenceChar = char
			fenceLen = runLen
		}

		if lineEnd == len(text) {
			break
		}
	}
	if fenceStart >= 0 {
		ranges = append(ranges, MarkdownCodeRange{Start: fenceStart, End: len(text)})
	} else {
		ranges = append(ranges, inlineCodeRanges(text[plainStart:], plainStart)...)
	}
	return ranges
}

func markdownLineEnd(text string, start int) (lineEnd, nextLine int) {
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '\n':
			return i, i + 1
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				return i, i + 2
			}
			return i, i + 1
		}
	}
	return len(text), len(text) + 1
}

func openingCodeFence(line string) (byte, int, bool) {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	if i >= len(line) || (line[i] != '`' && line[i] != '~') {
		return 0, 0, false
	}
	char := line[i]
	start := i
	for i < len(line) && line[i] == char {
		i++
	}
	if i-start < 3 || (char == '`' && strings.ContainsRune(line[i:], '`')) {
		return 0, 0, false
	}
	return char, i - start, true
}

func isClosingCodeFence(line string, char byte, minimumLen int) bool {
	i := 0
	for i < len(line) && i < 3 && line[i] == ' ' {
		i++
	}
	start := i
	for i < len(line) && line[i] == char {
		i++
	}
	if i-start < minimumLen {
		return false
	}
	return strings.Trim(line[i:], " \t") == ""
}

func inlineCodeRanges(text string, offset int) []MarkdownCodeRange {
	type backtickRun struct {
		start       int
		end         int
		openerStart int
		openerLen   int
		closer      int
	}

	var runs []backtickRun
	for i := 0; i < len(text); {
		if text[i] != '`' {
			i++
			continue
		}
		start := i
		for i < len(text) && text[i] == '`' {
			i++
		}
		openerStart := start
		openerLen := i - start
		backslashes := 0
		for j := start - 1; j >= 0 && text[j] == '\\'; j-- {
			backslashes++
		}
		// Outside code, an odd backslash count escapes the first backtick.
		// Any remaining backticks are still an independent opener candidate.
		if backslashes%2 == 1 {
			openerStart++
			openerLen--
		}
		runs = append(runs, backtickRun{
			start:       start,
			end:         i,
			openerStart: openerStart,
			openerLen:   openerLen,
			closer:      -1,
		})
	}

	lastRawRunByLength := make(map[int]int)
	// A candidate opener pairs with the nearest later raw run of the exact
	// effective length. Raw runs are used as closers because backslashes are
	// literal inside code spans. Unmatched candidates stay literal, allowing
	// later runs to begin independent spans.
	for i := len(runs) - 1; i >= 0; i-- {
		if runs[i].openerLen > 0 {
			if next, ok := lastRawRunByLength[runs[i].openerLen]; ok {
				runs[i].closer = next
			}
		}
		rawLength := runs[i].end - runs[i].start
		lastRawRunByLength[rawLength] = i
	}

	var ranges []MarkdownCodeRange
	for i := 0; i < len(runs); {
		closeIndex := runs[i].closer
		if closeIndex == -1 {
			i++
			continue
		}
		ranges = append(ranges, MarkdownCodeRange{
			Start: offset + runs[i].openerStart,
			End:   offset + runs[closeIndex].end,
		})
		i = closeIndex + 1
	}
	return ranges
}

// PositionInMarkdownCode reports whether a byte offset is protected by one of
// the ranges returned from MarkdownCodeRanges.
func PositionInMarkdownCode(ranges []MarkdownCodeRange, position int) bool {
	for _, r := range ranges {
		if position >= r.Start && position < r.End {
			return true
		}
	}
	return false
}

func sanitizeMarkerPart(value string) string {
	if value == "" {
		return ""
	}
	value = strings.ReplaceAll(value, "]", ")")
	value = strings.ReplaceAll(value, "\r", " ")
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}
