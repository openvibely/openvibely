package transcript

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/openvibely/openvibely/internal/llm/tooloutput"
	markers "github.com/openvibely/openvibely/internal/llm/transcript"
	"github.com/openvibely/openvibely/internal/models"
)

// DisplayItem is a public projection. It never carries provider system prompts,
// encrypted context, or private provider metadata. Text is literal, not markup
// containing control markers. Tool output is also literal and may contain code.
type DisplayItem struct {
	Type      string `json:"type"`
	Text      string `json:"text"`
	Name      string `json:"name,omitempty"`
	Secondary string `json:"secondary,omitempty"`
	Status    string `json:"status,omitempty"`
	State     string `json:"state,omitempty"`
	Duration  int64  `json:"duration,omitempty"`
	Complete  bool   `json:"complete"`
	Encoded   bool   `json:"encoded,omitempty"`
	// Prefix/Suffix preserve exact historical delimiters for text exports, without
	// requiring browser or model readers to parse them. New items use canonical ones.
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

var displayControl = regexp.MustCompile(`(?m)^[\t ]*(\[Thinking\]|\[Using tool: ([^\]\r\n]+)\]|\[Tool (\S+) (done|error)\]|\[Compaction (started|done|failed)(?: \| (\d+))?\])[\t ]*\r?(?:\n|$)`)
var thinkingClose = regexp.MustCompile(`(?m)^[\t ]*\[/Thinking\][\t ]*\r?(?:\n|$)`)
var toolClose = regexp.MustCompile(`(?m)^[\t ]*\[/Tool\][\t ]*\r?(?:\n|$)`)

// ParseDisplay converts historical marker output once, during migration or an
// completed unstructured execution result. Raw streaming stays outside this parser.
func ParseDisplay(output string) []DisplayItem {
	output = markers.NormalizeMarkers(output)
	matches := displayControl.FindAllStringSubmatchIndex(output, -1)
	if len(matches) == 0 {
		if output == "" {
			return nil
		}
		return []DisplayItem{{Type: "text", Text: output, Complete: true}}
	}
	protected := markers.MarkdownCodeRanges(output)
	var items []DisplayItem
	cursor := 0
	addText := func(text string) {
		if text != "" {
			items = append(items, DisplayItem{Type: "text", Text: text, Complete: true})
		}
	}
	for _, m := range matches {
		start, end := m[0], m[1]
		if start < cursor || markers.PositionInMarkdownCode(protected, start) {
			continue
		}
		addText(output[cursor:start])
		get := func(group int) string {
			if m[group*2] < 0 {
				return ""
			}
			return output[m[group*2]:m[group*2+1]]
		}
		item := DisplayItem{Prefix: output[start:end], Complete: true}
		switch {
		case get(1) == "[Thinking]":
			item.Type = "thinking"
			close := thinkingClose.FindStringIndex(output[end:])
			if close == nil {
				item.Text = output[end:]
				item.Complete = false
				end = len(output)
			} else {
				item.Text = output[end : end+close[0]]
				item.Suffix = output[end+close[0] : end+close[1]]
				end += close[1]
			}
		case get(2) != "":
			item.Type = "using"
			parts := strings.SplitN(get(2), " | ", 2)
			item.Name = strings.TrimSpace(parts[0])
			if len(parts) > 1 {
				item.Secondary = parts[1]
			}
		case get(3) != "":
			item.Type = "tool_result"
			item.Name = get(3)
			item.Status = get(4)
			close := toolClose.FindStringIndex(output[end:])
			if close == nil {
				item.Text = output[end:]
				item.Complete = false
				end = len(output)
			} else {
				item.Text = output[end : end+close[0]]
				item.Suffix = output[end+close[0] : end+close[1]]
				end += close[1]
			}
			item.Encoded = true
		default:
			item.Type = "compaction"
			item.State = get(5)
			item.Duration, _ = strconv.ParseInt(get(6), 10, 64)
		}
		items = append(items, item)
		cursor = end
	}
	addText(output[cursor:])
	return items
}

func DisplayText(items []DisplayItem) string {
	var out strings.Builder
	for _, item := range items {
		if item.Prefix != "" || item.Suffix != "" {
			out.WriteString(item.Prefix)
			if item.Type == "tool_result" && !item.Encoded {
				out.WriteString(tooloutput.Encode(item.Text))
			} else if item.Type == "text" || item.Type == "thinking" || item.Type == "tool_result" {
				out.WriteString(item.Text)
			}
			out.WriteString(item.Suffix)
			continue
		}
		switch item.Type {
		case "text":
			out.WriteString(item.Text)
		case "thinking":
			out.WriteString("\n[Thinking]\n")
			out.WriteString(item.Text)
			if item.Complete {
				out.WriteString("\n[/Thinking]\n")
			}
		case "using":
			out.WriteString("\n[Using tool: " + item.Name)
			if item.Secondary != "" {
				out.WriteString(" | " + item.Secondary)
			}
			out.WriteString("]\n")
		case "tool_result":
			out.WriteString("[Tool " + item.Name + " " + item.Status + "]\n")
			out.WriteString(tooloutput.Encode(item.Text))
			if item.Complete {
				out.WriteString("\n[/Tool]\n")
			}
		case "compaction":
			out.WriteString("\n[Compaction " + item.State)
			if item.State != "started" {
				fmt.Fprintf(&out, " | %d", item.Duration)
			}
			out.WriteString("]\n")
		}
	}
	return out.String()
}

func DisplayRecords(items []DisplayItem) ([]models.ConversationChange, error) {
	records := make([]models.ConversationChange, 0, len(items))
	for i, item := range items {
		raw, err := json.Marshal(item)
		if err != nil {
			return nil, err
		}
		records = append(records, models.ConversationChange{ItemID: fmt.Sprintf("display:%012d", i), Position: int64(i), Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: raw})
	}
	return records, nil
}
