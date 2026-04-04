package stream

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"strings"
)

// parseJSONStream reads JSON events from the Claude CLI stream-json output
// and extracts natural language text content, writing it to the streamingWriter.
//
// The CLI outputs two kinds of events:
//   - With --include-partial-messages: Anthropic streaming events (content_block_delta,
//     content_block_start, etc.) for token-level real-time streaming.
//   - Always: Complete message objects with role field (assistant messages, tool results,
//     system metadata) emitted per agentic turn.
//
// When partial events are present, we extract text from deltas and skip complete
// assistant messages to avoid duplication. Without partial events, we fall back
// to extracting text from complete messages.
func ParseJSONStream(reader io.Reader, writer *Writer, skipThinking bool) error {
	scanner := bufio.NewScanner(reader)
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	// Track whether we've seen streaming delta events.
	// When --include-partial-messages is active, content arrives via delta events
	// and we skip complete message objects to avoid duplication.
	// We track text and thinking separately so the result fallback works correctly
	// when the model only streams thinking (e.g., when it uses tools and the actual
	// response text appears only in the final result event).
	hasSeenTextDeltas := false
	hasSeenActualText := false // true only for text_delta, not thinking_delta

	// Track current content block type so we can emit [/Thinking] end markers.
	currentBlockType := ""

	// Accumulate tool input JSON from input_json_delta events.
	// Used to extract tool details (file paths, commands) for display.
	var toolInputJSON strings.Builder
	currentToolName := ""

	// Track accumulated text length from "assistant" wrapper events for dedup.
	// With --include-partial-messages, the CLI sends multiple {"type":"assistant","message":{...}}
	// events where each contains the full content so far (not just the delta).
	// We also track the message ID to detect new agentic turns - each turn produces
	// a fresh message with its own content, so we must reset the counter.
	lastAssistantTextLen := 0
	lastAssistantMsgID := ""

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		// Route based on event structure.
		// The CLI wraps streaming events as {"type":"stream_event","event":{...}}.
		// Unwrap so the inner event type is dispatched correctly.
		eventType, hasType := event["type"].(string)
		if hasType && eventType == "stream_event" {
			if inner, ok := event["event"].(map[string]interface{}); ok {
				event = inner
				eventType, hasType = event["type"].(string)
			}
		}

		if hasType {
			switch eventType {
			case "content_block_delta":
				// Token-level streaming (from --include-partial-messages)
				if delta, ok := event["delta"].(map[string]interface{}); ok {
					dt, _ := delta["type"].(string)
					switch dt {
					case "text_delta":
						if text, ok := delta["text"].(string); ok && text != "" {
							log.Printf("[agent-svc] parseJSONStream: text_delta received, len=%d", len(text))
							hasSeenTextDeltas = true
							hasSeenActualText = true
							WriteEvent(writer, Event{Type: EventTextDelta, Text: text}, skipThinking)
						}
					case "input_json_delta":
						if partial, ok := delta["partial_json"].(string); ok && partial != "" {
							toolInputJSON.WriteString(partial)
						}
					case "thinking_delta":
						if thinking, ok := delta["thinking"].(string); ok && thinking != "" {
							log.Printf("[agent-svc] parseJSONStream: thinking_delta received, len=%d, skipThinking=%v", len(thinking), skipThinking)
							hasSeenTextDeltas = true
							WriteEvent(writer, Event{Type: EventThinkingText, Text: thinking}, skipThinking)
						}
					}
				}

			case "content_block_start":
				// Detect tool use and thinking blocks
				if cb, ok := event["content_block"].(map[string]interface{}); ok {
					bt, _ := cb["type"].(string)
					// Close any unclosed thinking block before starting a new block.
					// The CLI may not emit content_block_stop for thinking blocks
					// (especially with extended thinking), so we close them here.
					if currentBlockType == "thinking" && bt != "thinking" {
						WriteEvent(writer, Event{Type: EventThinkingEnd}, skipThinking)
					}
					currentBlockType = bt
					switch bt {
					case "tool_use":
						if name, ok := cb["name"].(string); ok && name != "" {
							log.Printf("[agent-svc] parseJSONStream: tool_use started: %s", name)
							currentToolName = name
							toolInputJSON.Reset()
							// Don't emit the marker yet — wait for content_block_stop
							// so we can include tool input details (file path, command, etc.)
						}
					case "thinking":
						log.Printf("[agent-svc] parseJSONStream: thinking block started, skipThinking=%v", skipThinking)
						WriteEvent(writer, Event{Type: EventThinkingOpen}, skipThinking)
					}
				}

			case "assistant":
				// CLI wraps assistant messages: {"type":"assistant","message":{...}}
				// With --include-partial-messages, each event contains the full content
				// accumulated so far, so we track length to emit only the new portion.
				if hasSeenTextDeltas {
					continue // Already getting content via top-level text deltas
				}
				if msg, ok := event["message"].(map[string]interface{}); ok {
					// Detect new agentic turns by checking message ID.
					// Each turn produces a new message with fresh content,
					// so we must reset the dedup counter.
					msgID, _ := msg["id"].(string)
					if msgID != "" && msgID != lastAssistantMsgID {
						if lastAssistantMsgID != "" {
							// New turn - add separator and reset
							WriteEvent(writer, Event{Type: EventRawOutput, Text: "\n"}, skipThinking)
						}
						lastAssistantMsgID = msgID
						lastAssistantTextLen = 0
					}

					fullText := collectMessageText(msg, skipThinking)
					if len(fullText) > lastAssistantTextLen {
						newText := fullText[lastAssistantTextLen:]
						WriteEvent(writer, Event{Type: EventRawOutput, Text: newText}, skipThinking)
						// Also write text-only content (no thinking/tool markers)
						textOnly := collectMessageTextOnly(msg)
						WriteEvent(writer, Event{Type: EventTextOnly, Text: textOnly}, skipThinking)
						hasSeenActualText = true
						lastAssistantTextLen = len(fullText)
					}
				}

			case "result":
				// Final result event from CLI: {"type":"result","result":"...","is_error":bool,"subtype":"..."}
				// Use as fallback if no actual text was captured via streaming.
				// When the model only streams thinking (e.g., uses tools), the actual
				// response text appears only in this result event.
				if result, ok := event["result"].(string); ok && result != "" {
					if !hasSeenActualText {
						log.Printf("[agent-svc] parseJSONStream: appending result (no text_delta seen), len=%d", len(result))
						if writer.String() != "" {
							WriteEvent(writer, Event{Type: EventRawOutput, Text: "\n"}, skipThinking)
						}
						WriteEvent(writer, Event{Type: EventRawOutput, Text: result}, skipThinking)
						WriteEvent(writer, Event{Type: EventTextOnly, Text: result}, skipThinking)
					} else {
						log.Printf("[agent-svc] parseJSONStream: ignoring result (already have text output)")
					}
				}
				// Capture is_error and subtype for failure detection
				if isErr, ok := event["is_error"].(bool); ok && isErr {
					subtype, _ := event["subtype"].(string)
					WriteEvent(writer, Event{Type: EventError, IsError: true, Subtype: subtype}, skipThinking)
					log.Printf("[agent-svc] parseJSONStream: result is_error=true subtype=%s", subtype)
				}

			case "message":
				// Complete assistant message (legacy/Anthropic format).
				// Skip if text deltas were seen (content already streamed).
				if !hasSeenTextDeltas {
					log.Printf("[agent-svc] parseJSONStream: extracting message text (no deltas seen)")
					extractMessageText(event, writer, skipThinking)
				} else {
					log.Printf("[agent-svc] parseJSONStream: skipping message (already got deltas)")
				}

			case "content_block_stop":
				// Emit end marker for thinking blocks so downstream can strip them
				if currentBlockType == "thinking" {
					WriteEvent(writer, Event{Type: EventThinkingEnd}, skipThinking)
				}
				// Emit tool marker with input details now that we have the full input JSON
				if currentBlockType == "tool_use" && currentToolName != "" {
					detail := extractToolDetail(currentToolName, toolInputJSON.String())
					// Strip ] from detail to avoid breaking the [Using tool: ...] marker delimiter
					detail = strings.ReplaceAll(detail, "]", ")")
					WriteEvent(writer, Event{Type: EventToolUse, ToolName: currentToolName, Secondary: detail}, skipThinking)
					currentToolName = ""
					toolInputJSON.Reset()
				}
				currentBlockType = ""

			case "message_start", "message_delta", "message_stop", "ping":
				// On message_stop, close any unclosed thinking block from this turn.
				// The CLI may not emit content_block_stop for thinking blocks.
				if eventType == "message_stop" && currentBlockType == "thinking" {
					WriteEvent(writer, Event{Type: EventThinkingEnd}, skipThinking)
					currentBlockType = ""
				}
				continue

			case "error":
				if errData, ok := event["error"].(map[string]interface{}); ok {
					errType, _ := errData["type"].(string)
					errMsg, _ := errData["message"].(string)
					log.Printf("[agent-svc] parseJSONStream: API error: type=%s message=%s", errType, errMsg)
				}
			}
		} else if role, ok := event["role"].(string); ok {
			// Role-based events from Claude CLI (complete messages, tool results, metadata)
			switch role {
			case "assistant":
				// Complete assistant message without standard streaming "type" field.
				// Only extract text when no text deltas are available.
				if !hasSeenTextDeltas {
					log.Printf("[agent-svc] parseJSONStream: extracting role=assistant message (no deltas)")
					extractMessageText(event, writer, skipThinking)
				} else {
					log.Printf("[agent-svc] parseJSONStream: skipping role=assistant (already got deltas)")
				}
			case "system":
				if sessionID, ok := event["session_id"].(string); ok && sessionID != "" {
					WriteEvent(writer, Event{Type: EventSessionID, SessionID: sessionID}, skipThinking)
					log.Printf("[agent-svc] parseJSONStream: session_id=%s", sessionID)
				}
				if cost, ok := event["cost_usd"].(float64); ok {
					log.Printf("[agent-svc] parseJSONStream: session cost=$%.4f", cost)
				}
			}
		}
	}

	// Close any unclosed thinking block at end of stream.
	// The CLI may not emit content_block_stop for thinking blocks,
	// so we ensure proper closure here.
	if currentBlockType == "thinking" {
		WriteEvent(writer, Event{Type: EventThinkingEnd}, skipThinking)
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return nil
}

// parseCodexJSONStream reads JSONL events from `codex exec --json` and extracts
// assistant text/tool activity into the streaming writer.
func ParseCodexJSONStream(reader io.Reader, writer *Writer, skipThinking bool) error {
	scanner := bufio.NewScanner(reader)
	const maxCapacity = 1024 * 1024 // 1MB
	buf := make([]byte, maxCapacity)
	scanner.Buffer(buf, maxCapacity)

	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}

		var event map[string]interface{}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)
		switch eventType {
		// Older codex event schema
		case "agent_message":
			if text := codexExtractAgentText(event); text != "" {
				WriteEvent(writer, Event{Type: EventTextDelta, Text: text}, skipThinking)
			}
		case "agent_message_delta":
			if delta := codexString(event["delta"]); delta != "" {
				WriteEvent(writer, Event{Type: EventTextDelta, Text: delta}, skipThinking)
			}
		case "agent_reasoning":
			if text := codexExtractReasoningText(event); text != "" {
				WriteEvent(writer, Event{Type: EventThinkingOpen}, skipThinking)
				WriteEvent(writer, Event{Type: EventThinkingText, Text: text}, skipThinking)
				WriteEvent(writer, Event{Type: EventThinkingEnd}, skipThinking)
			}
		case "exec.command_begin":
			if cmd := codexString(event["command"]); cmd != "" {
				WriteEvent(writer, Event{Type: EventToolUse, ToolName: "Bash", Secondary: "$ " + cmd}, skipThinking)
			}
		case "exec.command_output_delta":
			if delta := codexString(event["delta"]); delta != "" {
				WriteEvent(writer, Event{Type: EventRawOutput, Text: delta}, skipThinking)
			}

		// Newer codex event schema
		case "item.started":
			if item, ok := event["item"].(map[string]interface{}); ok {
				handleCodexItemStarted(item, writer, skipThinking)
			}
		case "item.completed":
			if item, ok := event["item"].(map[string]interface{}); ok {
				handleCodexItemCompleted(item, writer, skipThinking)
			}

		case "turn.failed":
			subtype := "turn_failed"
			if errData, ok := event["error"].(map[string]interface{}); ok {
				if st := codexString(errData["type"]); st != "" {
					subtype = st
				}
				if msg := codexString(errData["message"]); msg != "" && strings.TrimSpace(writer.String()) == "" {
					WriteEvent(writer, Event{Type: EventRawOutput, Text: msg}, skipThinking)
				}
			} else if msg := codexString(event["message"]); msg != "" && strings.TrimSpace(writer.String()) == "" {
				WriteEvent(writer, Event{Type: EventRawOutput, Text: msg}, skipThinking)
			}
			WriteEvent(writer, Event{Type: EventError, IsError: true, Subtype: subtype}, skipThinking)

		case "thread.started":
			if threadID := codexString(event["thread_id"]); threadID != "" {
				WriteEvent(writer, Event{Type: EventSessionID, SessionID: threadID}, skipThinking)
				log.Printf("[agent-svc] parseCodexJSONStream: thread_id=%s", threadID)
			}

		case "error":
			// Codex emits transient reconnect errors during network retries.
			// Keep these out of user-visible output to avoid noisy duplication.
			if msg := codexString(event["message"]); msg != "" {
				log.Printf("[agent-svc] parseCodexJSONStream notice: %s", msg)
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanner error: %w", err)
	}

	return nil
}

func handleCodexItemStarted(item map[string]interface{}, writer *Writer, skipThinking bool) {
	itemType := codexString(item["type"])
	switch itemType {
	case "command_execution":
		if cmd := codexString(item["command"]); cmd != "" {
			WriteEvent(writer, Event{Type: EventToolUse, ToolName: "Bash"}, skipThinking)
			WriteEvent(writer, Event{Type: EventRawOutput, Text: "$ " + cmd + "\n"}, skipThinking)
		}
	}
}

func handleCodexItemCompleted(item map[string]interface{}, writer *Writer, skipThinking bool) {
	itemType := codexString(item["type"])
	switch itemType {
	case "agent_message":
		if text := codexExtractAgentText(item); text != "" {
			WriteEvent(writer, Event{Type: EventTextDelta, Text: text}, skipThinking)
		}
	case "command_execution":
		if output := codexString(item["output"]); output != "" {
			WriteEvent(writer, Event{Type: EventRawOutput, Text: output}, skipThinking)
		}
		if stdout := codexString(item["stdout"]); stdout != "" {
			WriteEvent(writer, Event{Type: EventRawOutput, Text: stdout}, skipThinking)
		}
		if stderr := codexString(item["stderr"]); stderr != "" {
			WriteEvent(writer, Event{Type: EventRawOutput, Text: stderr}, skipThinking)
		}
		if exitCode, ok := codexInt(item["exit_code"]); ok && exitCode != 0 {
			WriteEvent(writer, Event{Type: EventRawOutput, Text: fmt.Sprintf("\ncommand exited with code %d\n", exitCode)}, skipThinking)
		}
	case "reasoning":
		if !skipThinking {
			if text := codexExtractReasoningText(item); text != "" {
				WriteEvent(writer, Event{Type: EventThinkingOpen}, skipThinking)
				WriteEvent(writer, Event{Type: EventThinkingText, Text: text}, skipThinking)
				WriteEvent(writer, Event{Type: EventThinkingEnd}, skipThinking)
			}
		}
	}
}

func codexString(v interface{}) string {
	s, _ := v.(string)
	return s
}

func codexInt(v interface{}) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	default:
		return 0, false
	}
}

func codexExtractAgentText(item map[string]interface{}) string {
	if text := codexString(item["text"]); text != "" {
		return text
	}
	if text := codexString(item["output_text"]); text != "" {
		return text
	}
	if msg, ok := item["message"].(map[string]interface{}); ok {
		if text := codexExtractAgentText(msg); text != "" {
			return text
		}
	}
	if content, ok := item["content"].([]interface{}); ok {
		var sb strings.Builder
		for _, raw := range content {
			block, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			blockType := codexString(block["type"])
			switch blockType {
			case "output_text", "text":
				if text := codexString(block["text"]); text != "" {
					sb.WriteString(text)
				}
			case "output_image":
				// Ignore image blocks for text output.
			default:
				// Some event versions omit block type; still try text key.
				if text := codexString(block["text"]); text != "" {
					sb.WriteString(text)
				}
			}
		}
		return sb.String()
	}
	return ""
}

func codexExtractReasoningText(item map[string]interface{}) string {
	if text := codexString(item["text"]); text != "" {
		return text
	}
	if summary, ok := item["summary"].([]interface{}); ok {
		var sb strings.Builder
		for _, raw := range summary {
			part, ok := raw.(map[string]interface{})
			if !ok {
				continue
			}
			if text := codexString(part["text"]); text != "" {
				if sb.Len() > 0 {
					sb.WriteString("\n")
				}
				sb.WriteString(text)
			}
		}
		return sb.String()
	}
	return ""
}

// extractToolDetail parses tool input JSON and returns a short summary
// for display (e.g., file path for Read/Edit, command for Bash).
func extractToolDetail(toolName, inputJSON string) string {
	if inputJSON == "" {
		return ""
	}
	var input map[string]interface{}
	if err := json.Unmarshal([]byte(inputJSON), &input); err != nil {
		return ""
	}
	switch toolName {
	case "Read", "read_file":
		if fp, ok := input["file_path"].(string); ok {
			return filepath.Base(fp)
		}
	case "Write", "write_file":
		if fp, ok := input["file_path"].(string); ok {
			return filepath.Base(fp)
		}
	case "Edit", "edit_file":
		if fp, ok := input["file_path"].(string); ok {
			return filepath.Base(fp)
		}
	case "Glob", "list_files":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Grep", "grep_search":
		if p, ok := input["pattern"].(string); ok {
			return p
		}
	case "Bash":
		if cmd, ok := input["command"].(string); ok {
			// Keep more of long commands so task-thread markers preserve the
			// meaningful path/flag context before truncating.
			if len(cmd) > 320 {
				cmd = cmd[:320] + "..."
			}
			return cmd
		}
	}
	return ""
}

// extractMessageText extracts text and tool_use content from a complete
// Claude CLI message event and writes it to the streamingWriter.
// When skipThinking is true, thinking blocks are omitted (used in chat mode).
func extractMessageText(event map[string]interface{}, writer *Writer, skipThinking bool) {
	content, ok := event["content"].([]interface{})
	if !ok {
		return
	}
	for _, block := range content {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := blockMap["type"].(string)
		switch blockType {
		case "text":
			if text, ok := blockMap["text"].(string); ok && text != "" {
				WriteEvent(writer, Event{Type: EventTextDelta, Text: text}, skipThinking)
			}
		case "tool_use":
			if name, ok := blockMap["name"].(string); ok && name != "" {
				detail := ""
				if inputMap, ok := blockMap["input"].(map[string]interface{}); ok {
					if inputBytes, err := json.Marshal(inputMap); err == nil {
						detail = extractToolDetail(name, string(inputBytes))
						detail = strings.ReplaceAll(detail, "]", ")")
					}
				}
				if detail != "" {
					WriteEvent(writer, Event{Type: EventToolUse, ToolName: name, Secondary: detail}, skipThinking)
				} else {
					WriteEvent(writer, Event{Type: EventToolUse, ToolName: name}, skipThinking)
				}
			}
		case "thinking":
			if !skipThinking {
				if thinking, ok := blockMap["thinking"].(string); ok && thinking != "" {
					WriteEvent(writer, Event{Type: EventThinkingOpen}, skipThinking)
					WriteEvent(writer, Event{Type: EventThinkingText, Text: thinking}, skipThinking)
					WriteEvent(writer, Event{Type: EventThinkingEnd}, skipThinking)
				}
			}
		}
	}
}

// collectMessageText extracts all text and tool_use content from a message
// and returns it as a string. Used for deduplicating partial messages where
// each event contains the full content accumulated so far.
// When skipThinking is true, thinking blocks are omitted (used in chat mode).
func collectMessageText(event map[string]interface{}, skipThinking bool) string {
	content, ok := event["content"].([]interface{})
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, block := range content {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		blockType, _ := blockMap["type"].(string)
		switch blockType {
		case "text":
			if text, ok := blockMap["text"].(string); ok && text != "" {
				sb.WriteString(text)
			}
		case "tool_use":
			if name, ok := blockMap["name"].(string); ok && name != "" {
				detail := ""
				if inputMap, ok := blockMap["input"].(map[string]interface{}); ok {
					if inputBytes, err := json.Marshal(inputMap); err == nil {
						detail = extractToolDetail(name, string(inputBytes))
						detail = strings.ReplaceAll(detail, "]", ")")
					}
				}
				if detail != "" {
					sb.WriteString(fmt.Sprintf("\n[Using tool: %s | %s]\n", name, detail))
				} else {
					sb.WriteString(fmt.Sprintf("\n[Using tool: %s]\n", name))
				}
			}
		case "thinking":
			if !skipThinking {
				if thinking, ok := blockMap["thinking"].(string); ok && thinking != "" {
					sb.WriteString("\n[Thinking]\n")
					sb.WriteString(thinking)
					sb.WriteString("\n[/Thinking]\n")
				}
			}
		}
	}
	return sb.String()
}

// collectMessageTextOnly extracts only text content (no thinking, no tool markers)
// from a message. Used for the text-only buffer in task chaining.
func collectMessageTextOnly(event map[string]interface{}) string {
	content, ok := event["content"].([]interface{})
	if !ok {
		return ""
	}
	var sb strings.Builder
	for _, block := range content {
		blockMap, ok := block.(map[string]interface{})
		if !ok {
			continue
		}
		if blockType, _ := blockMap["type"].(string); blockType == "text" {
			if text, ok := blockMap["text"].(string); ok && text != "" {
				sb.WriteString(text)
			}
		}
	}
	return sb.String()
}
