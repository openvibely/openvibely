package sqlite

import (
	"crypto/sha256"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	markers "github.com/openvibely/openvibely/internal/llm/transcript"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/transcript"
)

// runningDisplayWindow bounds the public seed, not provider replay. Raw storage
// still needs one server-side scan; the browser receives only this window and
// the call headers needed to interpret results crossing its boundary.
func runningDisplayWindow(output string, before int64, budget int, revision string) (models.DisplayTail, error) {
	end := len(output)
	if revision != "" {
		fields := strings.Split(revision, "-")
		if len(fields) != 3 || fields[0] != "live" {
			return models.DisplayTail{}, sql.ErrNoRows
		}
		n, err := strconv.Atoi(fields[1])
		if err != nil || n < 0 || n > len(output) || fmt.Sprintf("%x", sha256.Sum256([]byte(output[:n]))) != fields[2] {
			return models.DisplayTail{}, sql.ErrNoRows
		}
	} else {
		revision = fmt.Sprintf("live-%d-%x", end, sha256.Sum256([]byte(output)))
	}
	if before != 0 {
		if before < 0 || before > int64(end) {
			return models.DisplayTail{}, sql.ErrNoRows
		}
		end = int(before)
	}
	items := transcript.ParseDisplay(output[:end])
	offsets := make([]int, len(items)+1)
	// Preserve original bytes, including CRLF and encoded tool output.
	for i, item := range items {
		offsets[i+1] = offsets[i] + len(item.Prefix) + len(item.Text) + len(item.Suffix)
	}
	if offsets[len(items)] != end {
		return models.DisplayTail{}, fmt.Errorf("display parser did not preserve source")
	}
	start := len(items)
	used := 0
	for start > 0 {
		size := offsets[start] - offsets[start-1]
		if start < len(items) && (len(items)-start >= 75 || used+size > budget) {
			break
		}
		start--
		used += size
	}
	// Never drop text that a future backtick might turn into a code span. A
	// conservative text-item fallback also preserves reference definitions and
	// unfinished Markdown. Tool result bodies are opaque and do not affect this.
	for i := 0; i < start; i++ {
		if items[i].Type == "text" && runningUnstableText(items[i].Text) {
			start = i
			break
		}
	}
	// Plans are supplied separately by the persisted plan projection.
	pending := make(map[string][]int)
	compaction := -1
	for i := 0; i < start; i++ {
		item := items[i]
		item.Name = runningToolName(item.Name)
		switch item.Type {
		case "compaction":
			if item.State == "started" {
				compaction = i
			} else {
				compaction = -1
			}
		case "using":
			pending[item.Name] = append(pending[item.Name], i)
		case "tool_result":
			if q := pending[item.Name]; len(q) > 0 {
				pending[item.Name] = q[1:]
			}
		}
	}
	retained := make(map[int]bool)
	if compaction >= 0 {
		retained[compaction] = true
	}
	for _, q := range pending {
		for _, i := range q {
			retained[i] = true
		}
	}
	var context strings.Builder
	contextToolOffsets := make(map[int]int64)
	for i := 0; i < start; i++ {
		if retained[i] {
			raw := output[offsets[i]:offsets[i+1]]
			if items[i].Type == "using" {
				// The browser's marker match starts at the beginning of the line,
				// including indentation. Use that same position for stable IDs.
				contextToolOffsets[context.Len()] = int64(offsets[i])
			}
			context.WriteString(raw)
		}
	}
	prefix := context.String()
	return models.DisplayTail{Text: strings.Clone(prefix + output[offsets[start]:end]), Context: prefix, ContextToolOffsets: contextToolOffsets, Before: int64(offsets[start]), EndOffset: int64(end), Revision: revision}, nil
}

// Keep FIFO association consistent with the browser's provider aliases.
func runningToolName(name string) string {
	switch name {
	case "Read":
		return "read_file"
	case "Write":
		return "write_file"
	case "Edit":
		return "edit_file"
	case "Bash":
		return "bash"
	case "Shell":
		return "shell"
	case "Grep", "Search":
		return "grep_search"
	case "Glob", "List Files":
		return "list_files"
	case "WebSearch", "web.run", "web_search_20250305", "web_search_20260209":
		return "web_search"
	case "WebFetch", "web_fetch_20250910", "web_fetch_20260209", "web_fetch_20260309":
		return "web_fetch"
	default:
		return name
	}
}

var runningReferenceDefinition = regexp.MustCompile(`(?m)^ {0,3}\[[^]\r\n]+\]:`)

func runningUnstableText(text string) bool {
	// Legacy inline result controls are still supported by the browser, but not
	// by ParseDisplay. Keep their original context rather than reconstruct FIFO
	// state from an incomplete interpretation.
	if strings.Contains(text, "[Tool ") || runningReferenceDefinition.MatchString(text) {
		return true
	}
	ranges := markers.MarkdownCodeRanges(text)
	r := 0
	for i := 0; i < len(text); i++ {
		for r < len(ranges) && ranges[r].End <= i {
			r++
		}
		if r < len(ranges) && ranges[r].Start <= i {
			i = ranges[r].End - 1
			continue
		}
		if text[i] != '`' {
			continue
		}
		slashes := 0
		for j := i - 1; j >= 0 && text[j] == '\\'; j-- {
			slashes++
		}
		// Escaping the first tick of a longer run still leaves an opener.
		if slashes%2 == 0 || (i+1 < len(text) && text[i+1] == '`') {
			return true
		}
	}
	return false
}
