package anthropicclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestParseAgenticStream_TextOnly(t *testing.T) {
	// Simulate a streaming response with only text content
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" world"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
		`{"type":"message_stop"}`,
	})

	var collected strings.Builder
	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		collected.WriteString(text)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.model != "claude-sonnet-4-20250514" {
		t.Errorf("model = %q, want claude-sonnet-4-20250514", result.model)
	}
	if result.stopReason != "end_turn" {
		t.Errorf("stopReason = %q, want end_turn", result.stopReason)
	}
	if result.inputTokens != 10 {
		t.Errorf("inputTokens = %d, want 10", result.inputTokens)
	}
	if result.outputTokens != 5 {
		t.Errorf("outputTokens = %d, want 5", result.outputTokens)
	}
	if collected.String() != "Hello world" {
		t.Errorf("collected text = %q, want %q", collected.String(), "Hello world")
	}
	if len(result.contentBlocks) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.contentBlocks))
	}
	if result.contentBlocks[0].Text != "Hello world" {
		t.Errorf("block text = %q, want %q", result.contentBlocks[0].Text, "Hello world")
	}
}

func TestParseAgenticStream_WithToolUse(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me read that file."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_abc","name":"read_file"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"path\": \"main.go\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":15}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.stopReason != "tool_use" {
		t.Errorf("stopReason = %q, want tool_use", result.stopReason)
	}
	if len(result.contentBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.contentBlocks))
	}

	// First block: text
	if result.contentBlocks[0].Type != "text" {
		t.Errorf("block 0 type = %q, want text", result.contentBlocks[0].Type)
	}
	if result.contentBlocks[0].Text != "Let me read that file." {
		t.Errorf("block 0 text = %q", result.contentBlocks[0].Text)
	}

	// Second block: tool_use
	block := result.contentBlocks[1]
	if block.Type != "tool_use" {
		t.Errorf("block 1 type = %q, want tool_use", block.Type)
	}
	if block.ID != "toolu_abc" {
		t.Errorf("block 1 id = %q, want toolu_abc", block.ID)
	}
	if block.Name != "read_file" {
		t.Errorf("block 1 name = %q, want read_file", block.Name)
	}

	// Parse the accumulated input JSON
	var input struct {
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(block.Input, &input); err != nil {
		t.Fatalf("parse tool input: %v", err)
	}
	if input.FilePath != "main.go" {
		t.Errorf("tool input file_path = %q, want main.go", input.FilePath)
	}
}

func TestParseAgenticStream_EmptyStream(t *testing.T) {
	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.contentBlocks) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(result.contentBlocks))
	}
}

func TestParseAgenticStream_MultipleToolUse(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_3","model":"claude-sonnet-4-20250514","usage":{"input_tokens":30}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\": \"a.go\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"read_file"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\": \"b.go\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":10}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.contentBlocks) != 2 {
		t.Fatalf("expected 2 tool_use blocks, got %d", len(result.contentBlocks))
	}
	if result.contentBlocks[0].ID != "toolu_1" || result.contentBlocks[1].ID != "toolu_2" {
		t.Error("tool use IDs mismatch")
	}
}

// TestAgenticBlockMarshal verifies that agenticBlock marshals correctly
// for tool_result messages sent back to the API.
func TestAgenticBlockMarshal(t *testing.T) {
	block := agenticBlock{
		Type:      "tool_result",
		ToolUseID: "toolu_abc",
		Content:   "file contents here",
	}

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	json.Unmarshal(data, &m)

	if m["type"] != "tool_result" {
		t.Errorf("type = %v", m["type"])
	}
	if m["tool_use_id"] != "toolu_abc" {
		t.Errorf("tool_use_id = %v", m["tool_use_id"])
	}
	if m["content"] != "file contents here" {
		t.Errorf("content = %v", m["content"])
	}
	// Omitted fields should not be present
	if _, ok := m["text"]; ok {
		t.Error("text field should be omitted")
	}
	if _, ok := m["id"]; ok {
		t.Error("id field should be omitted")
	}
}

// TestAgenticBlockMarshal_ToolUse verifies tool_use blocks marshal correctly
// when echoed back as assistant content.
func TestAgenticBlockMarshal_ToolUse(t *testing.T) {
	block := agenticBlock{
		Type:  "tool_use",
		ID:    "toolu_xyz",
		Name:  "bash",
		Input: json.RawMessage(`{"command":"ls -la"}`),
	}

	data, err := json.Marshal(block)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	json.Unmarshal(data, &m)

	if m["type"] != "tool_use" {
		t.Errorf("type = %v", m["type"])
	}
	if m["id"] != "toolu_xyz" {
		t.Errorf("id = %v", m["id"])
	}
	if m["name"] != "bash" {
		t.Errorf("name = %v", m["name"])
	}
}

// TestAgenticMessageMarshal verifies that messages with content block arrays
// marshal differently from simple string content messages.
func TestAgenticMessageMarshal(t *testing.T) {
	t.Run("string content", func(t *testing.T) {
		msg := agenticMessage{Role: "user", Content: "hello"}
		data, _ := json.Marshal(msg)
		if !strings.Contains(string(data), `"content":"hello"`) {
			t.Errorf("expected string content: %s", data)
		}
	})

	t.Run("block array content", func(t *testing.T) {
		msg := agenticMessage{
			Role: "user",
			Content: []agenticBlock{
				{Type: "tool_result", ToolUseID: "toolu_1", Content: "result"},
			},
		}
		data, err := json.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), `"tool_use_id"`) {
			t.Errorf("expected tool_result content block: %s", data)
		}
	})
}

func TestStripToolCallTags(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no tags",
			input:    "Hello world",
			expected: "Hello world",
		},
		{
			name:     "single tool_call block",
			input:    "Before\n<tool_call>\n{\"name\": \"read_file\"}\n</tool_call>\nAfter",
			expected: "Before\nAfter",
		},
		{
			name:     "multiple tool_call blocks",
			input:    "Start\n<tool_call>first</tool_call>\nMiddle\n<tool_call>second</tool_call>\nEnd",
			expected: "Start\nMiddle\nEnd",
		},
		{
			name:     "only tool_call block",
			input:    "<tool_call>\n{\"name\": \"bash\"}\n</tool_call>",
			expected: "",
		},
		{
			name:     "tool_call with multiline content",
			input:    "Text\n<tool_call>\n{\n  \"name\": \"edit_file\",\n  \"arguments\": {}\n}\n</tool_call>\nMore text",
			expected: "Text\nMore text",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripToolCallTags(tt.input)
			if got != tt.expected {
				t.Errorf("stripToolCallTags()\ngot:  %q\nwant: %q", got, tt.expected)
			}
		})
	}
}

func TestParseAgenticStream_StripsToolCallTags(t *testing.T) {
	// Simulate a streaming response where the model emits <tool_call> XML in text
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me read that file."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"\n<tool_call>\n{\"name\": \"read_file\"}\n</tool_call>\n"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done reading."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
	})

	var collected strings.Builder
	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		collected.WriteString(text)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// The completed text block should have tags stripped
	if len(result.contentBlocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.contentBlocks))
	}
	blockText := result.contentBlocks[0].Text
	if strings.Contains(blockText, "<tool_call>") {
		t.Errorf("text block still contains <tool_call> tag: %q", blockText)
	}
	if !strings.Contains(blockText, "Let me read that file.") {
		t.Errorf("text block missing expected content: %q", blockText)
	}

	// The streamed text should also not contain <tool_call> tags
	streamed := collected.String()
	if strings.Contains(streamed, "<tool_call>") {
		t.Errorf("streamed text contains <tool_call> tag: %q", streamed)
	}
	if !strings.Contains(streamed, "Let me read that file.") {
		t.Errorf("streamed text missing expected content: %q", streamed)
	}
}

func TestParseAgenticStream_ToolCallTagAcrossDeltas(t *testing.T) {
	// Tag split across multiple deltas
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Before "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"<tool_call>"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"{\"name\":\"bash\"}"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"</tool_call>"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" After"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
	})

	var collected strings.Builder
	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		collected.WriteString(text)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Completed text block should be clean
	blockText := result.contentBlocks[0].Text
	if strings.Contains(blockText, "<tool_call>") {
		t.Errorf("text block contains <tool_call>: %q", blockText)
	}

	// Streamed output should not contain tag content
	streamed := collected.String()
	if strings.Contains(streamed, "<tool_call>") {
		t.Errorf("streamed text contains <tool_call>: %q", streamed)
	}
	if !strings.Contains(streamed, "Before") {
		t.Errorf("streamed text missing 'Before': %q", streamed)
	}
}

func TestParseAgenticStream_ThinkingBlock(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-6","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" about this."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here is my answer."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
		`{"type":"message_stop"}`,
	})

	var collected strings.Builder
	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), func(text string) {
		collected.WriteString(text)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.contentBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.contentBlocks))
	}

	// First block: thinking
	if result.contentBlocks[0].Type != "thinking" {
		t.Errorf("block 0 type = %q, want thinking", result.contentBlocks[0].Type)
	}
	if result.contentBlocks[0].Thinking != "Let me think about this." {
		t.Errorf("block 0 thinking = %q, want %q", result.contentBlocks[0].Thinking, "Let me think about this.")
	}

	// Second block: text
	if result.contentBlocks[1].Type != "text" {
		t.Errorf("block 1 type = %q, want text", result.contentBlocks[1].Type)
	}
	if result.contentBlocks[1].Text != "Here is my answer." {
		t.Errorf("block 1 text = %q", result.contentBlocks[1].Text)
	}

	// Streamed text should only contain the text block, not thinking
	if collected.String() != "Here is my answer." {
		t.Errorf("streamed text = %q, want %q", collected.String(), "Here is my answer.")
	}
}

func TestParseAgenticStream_MaxTokensTruncation(t *testing.T) {
	// When the model hits max_tokens, stop_reason is "max_tokens"
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Partial output..."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":4096}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.stopReason != "max_tokens" {
		t.Errorf("stopReason = %q, want max_tokens", result.stopReason)
	}
	if len(result.contentBlocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(result.contentBlocks))
	}
	if result.contentBlocks[0].Text != "Partial output..." {
		t.Errorf("text = %q", result.contentBlocks[0].Text)
	}
}

func TestParseAgenticStream_CompactionBlock(t *testing.T) {
	// Simulate a streaming response that includes a compaction block
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":160000}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"compaction"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"Previous context summary: "}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"The user asked about Go programming."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here is my response."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":25}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Compaction block should be tracked separately, not in content blocks
	if result.compaction == nil {
		t.Fatal("expected compaction result, got nil")
	}
	if result.compaction.content == nil {
		t.Fatal("expected compaction content, got nil")
	}
	expected := "Previous context summary: The user asked about Go programming."
	if *result.compaction.content != expected {
		t.Errorf("compaction content = %q, want %q", *result.compaction.content, expected)
	}

	// Content blocks should only contain the text block (no compaction)
	if len(result.contentBlocks) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.contentBlocks))
	}
	if result.contentBlocks[0].Type != "text" {
		t.Errorf("block 0 type = %q, want text", result.contentBlocks[0].Type)
	}
	if result.contentBlocks[0].Text != "Here is my response." {
		t.Errorf("block 0 text = %q", result.contentBlocks[0].Text)
	}
}

func TestParseAgenticStream_CompactionBlockFailed(t *testing.T) {
	// Simulate a compaction block with no content (failed compaction)
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":160000}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"compaction"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Response text."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Failed compaction: result.compaction should be non-nil but content should be nil
	if result.compaction == nil {
		t.Fatal("expected compaction result for failed compaction")
	}
	if result.compaction.content != nil {
		t.Errorf("expected nil content for failed compaction, got %q", *result.compaction.content)
	}

	// Text block should still be present
	if len(result.contentBlocks) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.contentBlocks))
	}
}

func TestParseAgenticStream_NoCompaction(t *testing.T) {
	// Normal response without compaction — verify compaction is nil
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":5000}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Normal response."}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if result.compaction != nil {
		t.Error("expected no compaction result")
	}
}

func TestCompactionBlockJSON_Marshal(t *testing.T) {
	t.Run("with content", func(t *testing.T) {
		summary := "Summary of context"
		block := compactionBlockJSON{
			Type:    "compaction",
			Content: &summary,
		}
		data, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		expected := `{"type":"compaction","content":"Summary of context"}`
		if string(data) != expected {
			t.Errorf("got %s, want %s", data, expected)
		}
	})

	t.Run("with null content", func(t *testing.T) {
		block := compactionBlockJSON{
			Type:    "compaction",
			Content: nil,
		}
		data, err := json.Marshal(block)
		if err != nil {
			t.Fatal(err)
		}
		expected := `{"type":"compaction","content":null}`
		if string(data) != expected {
			t.Errorf("got %s, want %s", data, expected)
		}
	})
}

func TestContextManagementConfig_Marshal(t *testing.T) {
	cfg := contextManagementConfig{
		Edits: []contextManagementEdit{
			{
				Type: "clear_tool_uses_20250919",
				Trigger: &inputTokensTrigger{
					Type:  "input_tokens",
					Value: 150000,
				},
			},
			{
				Type: "clear_thinking_20251015",
				Trigger: &inputTokensTrigger{
					Type:  "input_tokens",
					Value: 150000,
				},
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	edits, ok := m["edits"].([]interface{})
	if !ok || len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %v", m["edits"])
	}

	edit := edits[0].(map[string]interface{})
	if edit["type"] != "clear_tool_uses_20250919" {
		t.Errorf("type = %v", edit["type"])
	}

	trigger := edit["trigger"].(map[string]interface{})
	if trigger["type"] != "input_tokens" {
		t.Errorf("trigger type = %v", trigger["type"])
	}
	if trigger["value"] != float64(150000) {
		t.Errorf("trigger value = %v", trigger["value"])
	}
}

func TestAgenticRequest_WithCompaction(t *testing.T) {
	req := agenticRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 8192,
		Messages: []agenticMessage{
			{Role: "user", Content: "Hello"},
		},
		Stream: true,
		ContextManagement: &contextManagementConfig{
			Edits: []contextManagementEdit{
				{
					Type: "clear_tool_uses_20250919",
					Trigger: &inputTokensTrigger{
						Type:  "input_tokens",
						Value: 100000,
					},
				},
				{
					Type: "clear_thinking_20251015",
					Trigger: &inputTokensTrigger{
						Type:  "input_tokens",
						Value: 100000,
					},
				},
			},
		},
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	cm, ok := m["context_management"].(map[string]interface{})
	if !ok {
		t.Fatal("context_management not found in request")
	}

	edits, ok := cm["edits"].([]interface{})
	if !ok || len(edits) != 2 {
		t.Fatalf("expected 2 edits, got %v", cm["edits"])
	}
}

func TestAgenticRequest_WithoutCompaction(t *testing.T) {
	req := agenticRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 8192,
		Messages: []agenticMessage{
			{Role: "user", Content: "Hello"},
		},
		Stream: true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	if _, ok := m["context_management"]; ok {
		t.Error("context_management should not be present when not configured")
	}
}

func TestSendAgentic_CompactionRoundTrip(t *testing.T) {
	// Simulate a multi-turn agentic call where compaction occurs.
	// Turn 1: tool_use (triggers compaction)
	// Turn 2: end_turn (final response)
	turnCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turnCount++

		// Verify beta header includes context management
		betaHeader := r.Header.Get("anthropic-beta")
		if !strings.Contains(betaHeader, "context-management-2025-06-27") {
			t.Errorf("missing context management beta header, got: %s", betaHeader)
		}

		// Verify context_management is in the request body
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		json.Unmarshal(body, &reqBody)
		if _, ok := reqBody["context_management"]; !ok {
			t.Error("context_management not found in request body")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		if turnCount == 1 {
			// Turn 1: Return compaction + tool_use
			events := []string{
				`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":160000}}}`,
				// Compaction block
				`{"type":"content_block_start","index":0,"content_block":{"type":"compaction"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"compaction_delta","content":"Summary of prior context."}}`,
				`{"type":"content_block_stop","index":0}`,
				// Text block
				`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Let me check."}}`,
				`{"type":"content_block_stop","index":1}`,
				// Tool use
				`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash"}}`,
				`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command\": \"echo hello\"}"}}`,
				`{"type":"content_block_stop","index":2}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":50}}`,
				`{"type":"message_stop"}`,
			}
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
		} else {
			// Turn 2: Verify compaction block was round-tripped in messages
			var msgs []interface{}
			if msgsRaw, ok := reqBody["messages"].([]interface{}); ok {
				msgs = msgsRaw
			}
			// First message should be the compaction block
			if len(msgs) > 0 {
				firstMsg := msgs[0].(map[string]interface{})
				if firstMsg["role"] == "user" {
					content, ok := firstMsg["content"].([]interface{})
					if ok && len(content) > 0 {
						block := content[0].(map[string]interface{})
						if block["type"] != "compaction" {
							t.Errorf("first message content type = %v, want compaction", block["type"])
						}
						if block["content"] != "Summary of prior context." {
							t.Errorf("compaction content = %v", block["content"])
						}
					}
				}
			}

			// Return final response
			events := []string{
				`{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-20250514","usage":{"input_tokens":5000}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done!"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`,
				`{"type":"message_stop"}`,
			}
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
		}
	}))
	defer server.Close()

	// Override the API host for testing
	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	client := NewWithAPIKey("test-key")

	var compactionCalled bool
	resp, err := client.SendAgentic(context.Background(), "test prompt", &AgenticOptions{
		Model:          "claude-sonnet-4-20250514",
		MaxTokens:      8192,
		MaxTurns:       5,
		DisableTools:   true, // We handle tool results manually in the mock
		AutoCompaction: true,
		OnCompaction: func(summary string) {
			compactionCalled = true
			if summary != "Summary of prior context." {
				t.Errorf("OnCompaction summary = %q", summary)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !compactionCalled {
		t.Error("OnCompaction callback was not called")
	}
	if !resp.Compacted {
		t.Error("expected Compacted=true in response")
	}
	if turnCount != 2 {
		t.Errorf("expected 2 turns, got %d", turnCount)
	}
	if !strings.Contains(resp.Text, "Done!") {
		t.Errorf("response text = %q, want to contain 'Done!'", resp.Text)
	}
}

func TestSendAgentic_NoCompactionWhenDisabled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify context management beta header is NOT present
		betaHeader := r.Header.Get("anthropic-beta")
		if strings.Contains(betaHeader, "context-management") {
			t.Error("context management beta header should not be present when disabled")
		}

		// Verify context_management is NOT in request body
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		json.Unmarshal(body, &reqBody)
		if _, ok := reqBody["context_management"]; ok {
			t.Error("context_management should not be in request when disabled")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Response"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	client := NewWithAPIKey("test-key")

	resp, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:          "claude-sonnet-4-20250514",
		MaxTokens:      8192,
		DisableTools:   true,
		AutoCompaction: false,
	})
	if err != nil {
		t.Fatal(err)
	}

	if resp.Compacted {
		t.Error("expected Compacted=false when auto compaction is disabled")
	}
}

func TestSendAgentic_ReadOnlyToolUsesExecuteInParallel(t *testing.T) {
	turnCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turnCount++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		if turnCount == 1 {
			events := []string{
				`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":50}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"grep_search"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
				`{"type":"message_stop"}`,
			}
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
			return
		}

		events := []string{
			`{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	var inFlight int32
	var maxInFlight int32

	client := NewWithAPIKey("test-key")
	_, err := client.SendAgentic(context.Background(), "parallel tools", &AgenticOptions{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 8192,
		ToolExecutor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			curr := atomic.AddInt32(&inFlight, 1)
			for {
				prev := atomic.LoadInt32(&maxInFlight)
				if curr <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, curr) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return "ok", false, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxInFlight < 2 {
		t.Fatalf("expected parallel execution for read-only tools, max in-flight=%d", maxInFlight)
	}
}

func TestSendAgentic_MutatingToolUsesStaySerial(t *testing.T) {
	turnCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turnCount++
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		if turnCount == 1 {
			events := []string{
				`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":50}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read_file"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_2","name":"bash"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":20}}`,
				`{"type":"message_stop"}`,
			}
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
			return
		}

		events := []string{
			`{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"done"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	var inFlight int32
	var maxInFlight int32

	client := NewWithAPIKey("test-key")
	_, err := client.SendAgentic(context.Background(), "serial tools", &AgenticOptions{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 8192,
		ToolExecutor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, error) {
			curr := atomic.AddInt32(&inFlight, 1)
			for {
				prev := atomic.LoadInt32(&maxInFlight)
				if curr <= prev || atomic.CompareAndSwapInt32(&maxInFlight, prev, curr) {
					break
				}
			}
			time.Sleep(40 * time.Millisecond)
			atomic.AddInt32(&inFlight, -1)
			return "ok", false, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if maxInFlight != 1 {
		t.Fatalf("expected serial execution when mutating tool is present, max in-flight=%d", maxInFlight)
	}
}

func TestCompactionThreshold_CustomValue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		json.Unmarshal(body, &reqBody)

		cm := reqBody["context_management"].(map[string]interface{})
		edits := cm["edits"].([]interface{})
		found := false
		for _, rawEdit := range edits {
			edit := rawEdit.(map[string]interface{})
			if edit["type"] != "clear_tool_uses_20250919" {
				continue
			}
			trigger := edit["trigger"].(map[string]interface{})
			if trigger["value"] != float64(50000) {
				t.Errorf("threshold = %v, want 50000", trigger["value"])
			}
			found = true
			break
		}
		if !found {
			t.Error("clear_tool_uses_20250919 edit not found")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	client := NewWithAPIKey("test-key")

	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:                    "claude-sonnet-4-20250514",
		MaxTokens:                8192,
		DisableTools:             true,
		AutoCompaction:           true,
		CompactionTokenThreshold: 50000,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContextManagementEdits_BothTypes(t *testing.T) {
	// Verify that both clear_tool_uses and clear_thinking edits are sent when thinking is enabled
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		json.Unmarshal(body, &reqBody)

		cm := reqBody["context_management"].(map[string]interface{})
		edits := cm["edits"].([]interface{})

		if len(edits) != 2 {
			t.Errorf("expected 2 edits, got %d", len(edits))
		}
		edit0 := edits[0].(map[string]interface{})
		edit1 := edits[1].(map[string]interface{})
		if edit0["type"] != "clear_thinking_20251015" {
			t.Errorf("first edit type = %v, want clear_thinking_20251015", edit0["type"])
		}
		if edit1["type"] != "clear_tool_uses_20250919" {
			t.Errorf("second edit type = %v, want clear_tool_uses_20250919", edit1["type"])
		}
		if _, ok := edit1["trigger"]; !ok {
			t.Error("clear_tool_uses_20250919 must include trigger")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	client := NewWithAPIKey("test-key")

	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:          "claude-sonnet-4-20250514",
		MaxTokens:      8192,
		EnableThinking: true,
		DisableTools:   true,
		AutoCompaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestContextManagementEdits_NoClearThinkingWithoutThinking(t *testing.T) {
	// Verify that clear_thinking is NOT included when thinking is disabled.
	// The Anthropic API returns 400 "clear_thinking strategy requires thinking
	// to be enabled or adaptive" if clear_thinking is sent without thinking.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		json.Unmarshal(body, &reqBody)

		cm := reqBody["context_management"].(map[string]interface{})
		edits := cm["edits"].([]interface{})

		if len(edits) != 1 {
			t.Errorf("expected 1 edit (clear_tool_uses only), got %d", len(edits))
		}
		edit0 := edits[0].(map[string]interface{})
		if edit0["type"] != "clear_tool_uses_20250919" {
			t.Errorf("first edit type = %v, want clear_tool_uses_20250919", edit0["type"])
		}
		// Verify clear_thinking is NOT present
		for _, edit := range edits {
			e := edit.(map[string]interface{})
			if e["type"] == "clear_thinking_20251015" {
				t.Error("clear_thinking must NOT be included when thinking is disabled")
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":100}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`,
			`{"type":"message_stop"}`,
		}
		for _, evt := range events {
			fmt.Fprintf(w, "data: %s\n\n", evt)
		}
	}))
	defer server.Close()

	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	client := NewWithAPIKey("test-key")

	_, err := client.SendAgentic(context.Background(), "test", &AgenticOptions{
		Model:          "claude-sonnet-4-20250514",
		MaxTokens:      8192,
		DisableTools:   true,
		AutoCompaction: true,
		// EnableThinking is false — clear_thinking must be omitted
	})
	if err != nil {
		t.Fatal(err)
	}
}

// buildSSE constructs a server-sent events stream from JSON data lines.
func buildSSE(events []string) string {
	var sb strings.Builder
	for _, evt := range events {
		fmt.Fprintf(&sb, "data: %s\n\n", evt)
	}
	return sb.String()
}

// devNull is an io.Reader that returns EOF immediately.
var _ io.Reader = strings.NewReader("")

func TestIsRetryable(t *testing.T) {
	retryable := []int{429, 500, 502, 503, 529}
	for _, code := range retryable {
		if !isRetryable(code) {
			t.Errorf("expected %d to be retryable", code)
		}
	}
	nonRetryable := []int{200, 400, 401, 403, 404, 422}
	for _, code := range nonRetryable {
		if isRetryable(code) {
			t.Errorf("expected %d to not be retryable", code)
		}
	}
}

func TestRetryBackoff(t *testing.T) {
	// Without a response, should use exponential backoff
	if d := retryBackoff(0, nil); d != 1*time.Second {
		t.Errorf("attempt 0: got %v, want 1s", d)
	}
	if d := retryBackoff(1, nil); d != 2*time.Second {
		t.Errorf("attempt 1: got %v, want 2s", d)
	}
	if d := retryBackoff(2, nil); d != 4*time.Second {
		t.Errorf("attempt 2: got %v, want 4s", d)
	}
}

func TestRetryBackoff_RetryAfterHeader(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{"Retry-After": []string{"7"}},
	}
	if d := retryBackoff(0, resp); d != 7*time.Second {
		t.Errorf("got %v, want 7s", d)
	}
}

func TestRetryBackoff_429WithoutRetryAfter(t *testing.T) {
	resp := &http.Response{
		StatusCode: 429,
		Header:     http.Header{},
	}
	if d := retryBackoff(0, resp); d != 1*time.Second {
		t.Errorf("got %v, want 1s (default backoff)", d)
	}
}

func TestDoWithRetry_SuccessOnFirstTry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := &http.Client{}
	resp, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
}

func TestDoWithRetry_RetriesThenSucceeds(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := &http.Client{}
	resp, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestDoWithRetry_ExhaustsRetries(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"unavailable"}`))
	}))
	defer server.Close()

	client := &http.Client{}
	resp, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	// After exhausting retries, the last response is returned (non-200)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("got status %d, want 503", resp.StatusCode)
	}
	// 1 initial + 3 retries = 4 attempts
	if attempts != 4 {
		t.Errorf("expected 4 attempts, got %d", attempts)
	}
}

func TestDoWithRetry_NonRetryableError(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer server.Close()

	client := &http.Client{}
	resp, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("got status %d, want 400", resp.StatusCode)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt (no retry for 400), got %d", attempts)
	}
}

func TestDoWithRetry_ContextCancelled(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel immediately so the backoff select picks up cancellation
	cancel()

	client := &http.Client{}
	_, err := doWithRetry(ctx, client, func() (*http.Request, error) {
		return http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	})
	if err == nil {
		t.Fatal("expected error from cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

func TestDoWithRetry_RetryAfterHeader(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := &http.Client{}
	resp, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("got status %d, want 200", resp.StatusCode)
	}
	if attempts != 2 {
		t.Errorf("expected 2 attempts, got %d", attempts)
	}
}

func TestDoWithRetry_SkipsRetryOnLongRetryAfter(t *testing.T) {
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.Header().Set("Retry-After", "7200")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":"rate limited"}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()

	client := &http.Client{}
	resp, err := doWithRetry(context.Background(), client, func() (*http.Request, error) {
		return http.NewRequest("POST", server.URL, strings.NewReader("{}"))
	})
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("got status %d, want 429", resp.StatusCode)
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt (skip retry on long Retry-After), got %d", attempts)
	}
}
