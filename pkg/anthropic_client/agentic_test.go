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
		Content:   anthropicStringContentRaw("file contents here"),
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
				{Type: "tool_result", ToolUseID: "toolu_1", Content: anthropicStringContentRaw("result")},
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

func TestParseAgenticStream_DropsIncompleteThinkingBlock(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-7","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Done."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":20}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.contentBlocks) != 1 {
		t.Fatalf("expected only complete text block, got %#v", result.contentBlocks)
	}
	if result.contentBlocks[0].Type != "text" || result.contentBlocks[0].Text != "Done." {
		t.Fatalf("unexpected remaining block: %#v", result.contentBlocks[0])
	}
}

func TestParseAgenticStream_ThinkingBlock(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-opus-4-6","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":" about this."}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"sig_123"}}`,
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
	if result.contentBlocks[0].Signature != "sig_123" {
		t.Errorf("block 0 signature = %q, want sig_123", result.contentBlocks[0].Signature)
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

func TestSendAgentic_OAuthSetsXAppHeader(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "beta=true" {
			t.Fatalf("raw query = %q, want beta=true", r.URL.RawQuery)
		}
		if got := r.Header.Get("x-app"); got != "cli" {
			t.Fatalf("x-app header = %q, want cli", got)
		}
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Fatalf("authorization header = %q, want Bearer token", got)
		}
		betaHeader := r.Header.Get("anthropic-beta")
		if !strings.Contains(betaHeader, "claude-code-20250219") || !strings.Contains(betaHeader, OAuthBetaHeader) {
			t.Fatalf("anthropic-beta header missing oauth betas: %q", betaHeader)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
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

	client := NewWithOAuthToken("oauth-token", "refresh-token", time.Now().Add(24*time.Hour).UnixMilli())
	resp, err := client.SendAgentic(context.Background(), "hello", &AgenticOptions{
		Model:          "claude-sonnet-4-20250514",
		MaxTokens:      1024,
		MaxTurns:       1,
		AutoCompaction: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "ok") {
		t.Fatalf("response text = %q, want %q", resp.Text, "ok")
	}
}

func TestSendAgentic_PauseTurnContinuesServerToolLoop(t *testing.T) {
	var turnCount int32
	var toolUseCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := int(atomic.AddInt32(&turnCount, 1))
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}

		// On the second turn, the assistant pause_turn response must be present in
		// conversation history so the provider can resume server-side tool work.
		if turn == 2 {
			msgs, ok := reqBody["messages"].([]interface{})
			if !ok || len(msgs) < 2 {
				t.Fatalf("turn 2 expected >=2 messages, got %#v", reqBody["messages"])
			}
			last, ok := msgs[len(msgs)-1].(map[string]interface{})
			if !ok {
				t.Fatalf("turn 2 last message type mismatch: %#v", msgs[len(msgs)-1])
			}
			if role, _ := last["role"].(string); role != "assistant" {
				t.Fatalf("turn 2 last role = %q, want assistant", role)
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		if turn == 1 {
			events := []string{
				`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Searching the web..."}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"stu_1","name":"web_search"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"n8n readme\"}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":8}}`,
				`{"type":"message_stop"}`,
			}
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
			return
		}

		events := []string{
			`{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-20250514","usage":{"input_tokens":12}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"n8n is a workflow automation tool."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
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
	resp, err := client.SendAgentic(context.Background(), "summarize n8n readme", &AgenticOptions{
		Model:            "claude-sonnet-4-20250514",
		MaxTokens:        2048,
		MaxTurns:         5,
		DisableTools:     true,
		WebSearchEnabled: true,
		OnToolUse: func(name string, _ json.RawMessage) {
			if name == "web_search" {
				atomic.AddInt32(&toolUseCount, 1)
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&turnCount); got != 2 {
		t.Fatalf("turnCount = %d, want 2", got)
	}
	if !strings.Contains(resp.Text, "n8n is a workflow automation tool.") {
		t.Fatalf("response text = %q, expected final resumed response text", resp.Text)
	}
	if got := atomic.LoadInt32(&toolUseCount); got == 0 {
		t.Fatal("expected server web_search tool-use callback during pause_turn flow")
	}
}

func TestSendAgentic_PauseTurnPreservesCodeExecutionToolResult(t *testing.T) {
	var turnCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := int(atomic.AddInt32(&turnCount, 1))
		body, _ := io.ReadAll(r.Body)
		var reqBody map[string]interface{}
		if err := json.Unmarshal(body, &reqBody); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}

		// On turn 3, prior assistant messages must include the provider-side
		// code_execution_tool_result block; otherwise Anthropic rejects with:
		// "...code_execution tool use ... without a corresponding
		// code_execution_tool_result block".
		if turn == 3 {
			msgs, ok := reqBody["messages"].([]interface{})
			if !ok || len(msgs) < 3 {
				t.Fatalf("turn 3 expected >=3 messages, got %#v", reqBody["messages"])
			}

			foundCodeExecutionResult := false
			for _, msgAny := range msgs {
				msg, ok := msgAny.(map[string]interface{})
				if !ok {
					continue
				}
				role, _ := msg["role"].(string)
				if role != "assistant" {
					continue
				}
				blocks, ok := msg["content"].([]interface{})
				if !ok {
					continue
				}
				for _, blockAny := range blocks {
					block, ok := blockAny.(map[string]interface{})
					if !ok {
						continue
					}
					if typ, _ := block["type"].(string); typ == "code_execution_tool_result" {
						if gotID, _ := block["tool_use_id"].(string); gotID != "srvtoolu_1" {
							t.Fatalf("code_execution_tool_result tool_use_id = %q, want srvtoolu_1", gotID)
						}
						if _, hasName := block["name"]; hasName {
							t.Fatal("code_execution_tool_result must not include name when round-tripped")
						}
						contentObj, ok := block["content"].(map[string]interface{})
						if !ok {
							t.Fatalf("code_execution_tool_result content type = %T, want object", block["content"])
						}
						if gotStdout, _ := contentObj["stdout"].(string); gotStdout != "2\n" {
							t.Fatalf("code_execution_tool_result content.stdout = %q, want %q", gotStdout, "2\n")
						}
						foundCodeExecutionResult = true
					}
				}
			}
			if !foundCodeExecutionResult {
				t.Fatal("turn 3 request missing code_execution_tool_result block in assistant history")
			}
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		switch turn {
		case 1:
			events := []string{
				`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":30}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"code_execution"}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"code\":\"print(1+1)\"}"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":7}}`,
				`{"type":"message_stop"}`,
			}
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
		case 2:
			events := []string{
				`{"type":"message_start","message":{"id":"msg_2","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","content":{"stdout":"2\n","stderr":""}}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"server_tool_use","id":"srvtoolu_2","name":"web_search"}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"n8n\"}"}}`,
				`{"type":"content_block_stop","index":1}`,
				`{"type":"message_delta","delta":{"stop_reason":"pause_turn"},"usage":{"output_tokens":9}}`,
				`{"type":"message_stop"}`,
			}
			for _, evt := range events {
				fmt.Fprintf(w, "data: %s\n\n", evt)
			}
		default:
			events := []string{
				`{"type":"message_start","message":{"id":"msg_3","model":"claude-sonnet-4-20250514","usage":{"input_tokens":15}}}`,
				`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
				`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Here is the README summary."}}`,
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

	origHost := AnthropicAPIHost
	AnthropicAPIHost = server.URL
	defer func() { AnthropicAPIHost = origHost }()

	client := NewWithAPIKey("test-key")
	resp, err := client.SendAgentic(context.Background(), "summarize n8n readme", &AgenticOptions{
		Model:            "claude-sonnet-4-20250514",
		MaxTokens:        2048,
		MaxTurns:         5,
		DisableTools:     true,
		WebSearchEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&turnCount); got != 3 {
		t.Fatalf("turnCount = %d, want 3", got)
	}
	if !strings.Contains(resp.Text, "Here is the README summary.") {
		t.Fatalf("response text = %q, expected resumed final response text", resp.Text)
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

func TestAgenticRequestMarshalJSON_WithRawTools(t *testing.T) {
	// Verify that rawTools overrides Tools in JSON output.
	clientTools := []ToolDefinition{
		{Name: "read_file", Description: "Read a file", InputSchema: json.RawMessage(`{"type":"object"}`)},
	}
	serverTools := []serverToolDef{
		{Type: anthropicWebSearchToolType, Name: "web_search"},
		{Type: anthropicWebFetchToolType, Name: "web_fetch"},
	}
	mixed := make([]any, 0, len(clientTools)+len(serverTools))
	for _, t2 := range clientTools {
		mixed = append(mixed, t2)
	}
	for _, st := range serverTools {
		mixed = append(mixed, st)
	}
	rawBytes, err := json.Marshal(mixed)
	if err != nil {
		t.Fatal(err)
	}

	req := agenticRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 4096,
		Messages:  []agenticMessage{{Role: "user", Content: "hello"}},
		rawTools:  rawBytes,
		Stream:    true,
	}

	data, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}

	// Check tools array
	var tools []map[string]interface{}
	if err := json.Unmarshal(m["tools"], &tools); err != nil {
		t.Fatalf("unmarshal tools: %v", err)
	}

	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	// First should be client tool
	if tools[0]["name"] != "read_file" {
		t.Errorf("tools[0].name = %v, want read_file", tools[0]["name"])
	}
	// Second should be provider-native web_search tool
	if tools[1]["type"] != anthropicWebSearchToolType {
		t.Errorf("tools[1].type = %v, want %s", tools[1]["type"], anthropicWebSearchToolType)
	}
	if tools[1]["name"] != "web_search" {
		t.Errorf("tools[1].name = %v, want web_search", tools[1]["name"])
	}
	// Third should be provider-native web_fetch tool
	if tools[2]["type"] != anthropicWebFetchToolType {
		t.Errorf("tools[2].type = %v, want %s", tools[2]["type"], anthropicWebFetchToolType)
	}
	if tools[2]["name"] != "web_fetch" {
		t.Errorf("tools[2].name = %v, want web_fetch", tools[2]["name"])
	}
}

func TestAgenticRequestMarshalJSON_WithoutRawTools(t *testing.T) {
	// Verify that normal serialization works when rawTools is nil.
	req := agenticRequest{
		Model:     "claude-sonnet-4-20250514",
		MaxTokens: 4096,
		Messages:  []agenticMessage{{Role: "user", Content: "hello"}},
		Tools: []ToolDefinition{
			{Name: "bash", Description: "Run bash", InputSchema: json.RawMessage(`{"type":"object"}`)},
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

	tools, ok := m["tools"].([]interface{})
	if !ok {
		t.Fatal("expected tools array")
	}
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	tool := tools[0].(map[string]interface{})
	if tool["name"] != "bash" {
		t.Errorf("tools[0].name = %v, want bash", tool["name"])
	}
}

func TestSendAgentic_SkipDefaultToolsUsesOnlyExtraTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req map[string]interface{}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}

		toolsVal, ok := req["tools"].([]interface{})
		if !ok {
			t.Fatalf("expected tools array, got %#v", req["tools"])
		}
		if len(toolsVal) != 1 {
			t.Fatalf("expected 1 tool (runtime only), got %d", len(toolsVal))
		}
		tool0, ok := toolsVal[0].(map[string]interface{})
		if !ok {
			t.Fatalf("tools[0] type mismatch: %#v", toolsVal[0])
		}
		if got, _ := tool0["name"].(string); got != "create_task" {
			t.Fatalf("tools[0].name = %q, want create_task", got)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":12}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ok"}}`,
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
	resp, err := client.SendAgentic(context.Background(), "run action", &AgenticOptions{
		Model:            "claude-sonnet-4-20250514",
		MaxTokens:        2048,
		DisableTools:     false,
		SkipDefaultTools: true,
		ExtraTools: []ToolDefinition{
			{
				Name:        "create_task",
				Description: "Create a task",
				InputSchema: json.RawMessage(`{"type":"object"}`),
			},
		},
		WebSearchEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Text, "ok") {
		t.Fatalf("response text = %q, want %q", resp.Text, "ok")
	}
}

func TestSendAgentic_ProviderToolResultWithoutToolUseStillEmitsToolUseCallback(t *testing.T) {
	var toolUseCount int32
	var toolResultCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":20}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"web_fetch_tool_result","tool_use_id":"stu_1","content":{"type":"web_fetch_result","url":"https://example.com","title":"Example"}}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Summary text."}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":9}}`,
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
	resp, err := client.SendAgentic(context.Background(), "summarize", &AgenticOptions{
		Model:            "claude-sonnet-4-20250514",
		MaxTokens:        2048,
		DisableTools:     true,
		WebSearchEnabled: true,
		OnToolUse: func(name string, input json.RawMessage) {
			if name != "web_fetch" {
				t.Fatalf("tool use name = %q, want web_fetch", name)
			}
			var payload map[string]string
			if err := json.Unmarshal(input, &payload); err != nil {
				t.Fatalf("tool use input unmarshal: %v", err)
			}
			if payload["url"] != "https://example.com" {
				t.Fatalf("tool use input url = %q, want https://example.com", payload["url"])
			}
			atomic.AddInt32(&toolUseCount, 1)
		},
		OnToolResult: func(name string, output string, isError bool) {
			if isError {
				t.Fatalf("unexpected tool result error for %s", name)
			}
			if name != "web_fetch" {
				t.Fatalf("tool result name = %q, want web_fetch", name)
			}
			if !strings.Contains(output, "web_fetch_result") {
				t.Fatalf("tool result output = %q, expected web_fetch_result payload", output)
			}
			atomic.AddInt32(&toolResultCount, 1)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&toolUseCount); got != 1 {
		t.Fatalf("toolUseCount = %d, want 1", got)
	}
	if got := atomic.LoadInt32(&toolResultCount); got != 1 {
		t.Fatalf("toolResultCount = %d, want 1", got)
	}
	if !strings.Contains(resp.Text, "Summary text.") {
		t.Fatalf("response text = %q, want summary text", resp.Text)
	}
}

func TestSendAgentic_ProviderToolCallbacksStreamBeforeText(t *testing.T) {
	var callbackOrder []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":24}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"stu_1","name":"web_fetch"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"url\":\"https://example.com\"}"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"web_fetch_tool_result","tool_use_id":"stu_1","name":"web_fetch","content":{"type":"web_fetch_result","url":"https://example.com","title":"Example"}}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":2,"delta":{"type":"text_delta","text":"Summary text."}}`,
			`{"type":"content_block_stop","index":2}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`,
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
	resp, err := client.SendAgentic(context.Background(), "summarize", &AgenticOptions{
		Model:            "claude-sonnet-4-20250514",
		MaxTokens:        2048,
		DisableTools:     true,
		WebSearchEnabled: true,
		OnToolUse: func(name string, _ json.RawMessage) {
			callbackOrder = append(callbackOrder, "tool_use:"+name)
		},
		OnToolResult: func(name string, _ string, isError bool) {
			if isError {
				t.Fatalf("unexpected tool error for %s", name)
			}
			callbackOrder = append(callbackOrder, "tool_result:"+name)
		},
		OnText: func(text string) {
			if strings.TrimSpace(text) == "" {
				return
			}
			callbackOrder = append(callbackOrder, "text")
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(resp.Text, "Summary text.") {
		t.Fatalf("response text = %q, want summary text", resp.Text)
	}

	toolUseIndex := -1
	toolResultIndex := -1
	textIndex := -1
	for i, item := range callbackOrder {
		if toolUseIndex == -1 && strings.HasPrefix(item, "tool_use:") {
			toolUseIndex = i
			continue
		}
		if toolResultIndex == -1 && strings.HasPrefix(item, "tool_result:") {
			toolResultIndex = i
			continue
		}
		if textIndex == -1 && item == "text" {
			textIndex = i
		}
	}

	if toolUseIndex == -1 || toolResultIndex == -1 || textIndex == -1 {
		t.Fatalf("missing callbacks, order=%v", callbackOrder)
	}
	if !(toolUseIndex < toolResultIndex && toolResultIndex < textIndex) {
		t.Fatalf("callback order=%v, want tool_use -> tool_result -> text", callbackOrder)
	}
}

func TestSendAgentic_URLPromptCodeExecRateLimitDoesNotForceProviderWebRetry(t *testing.T) {
	var turnCount int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := int(atomic.AddInt32(&turnCount, 1))
		if turn > 1 {
			t.Fatalf("unexpected extra turn %d; no forced provider-web retry should be injected", turn)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		events := []string{
			`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":22}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"code_execution_tool_result","tool_use_id":"srvtoolu_1","name":"code_execution","content":{"type":"code_execution_tool_result_error","error_code":"too_many_requests"}}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"I cannot fetch this right now."}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":8}}`,
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
	resp, err := client.SendAgentic(context.Background(), "summarize this blog https://www.crunchydata.com/blog/postgres-is-out-of-disk-and-how-to-recover-the-dos-and-donts", &AgenticOptions{
		Model:            "claude-sonnet-4-20250514",
		MaxTokens:        2048,
		MaxTurns:         5,
		DisableTools:     true,
		WebSearchEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := atomic.LoadInt32(&turnCount); got != 1 {
		t.Fatalf("turnCount = %d, want 1", got)
	}
	if !strings.Contains(resp.Text, "I cannot fetch this right now.") {
		t.Fatalf("response text = %q, want first-turn text without forced retry", resp.Text)
	}
}

func TestParseAgenticStream_ServerToolUse(t *testing.T) {
	// Simulate a stream with server_tool_use (web search) content blocks.
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":15}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"stu_1","name":"web_search_20250305"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"Go documentation\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Here are the results."}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":10}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.contentBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.contentBlocks))
	}

	// First block should be server_tool_use
	block := result.contentBlocks[0]
	if block.Type != "server_tool_use" {
		t.Errorf("block[0].type = %q, want server_tool_use", block.Type)
	}
	if block.Name != "web_search_20250305" {
		t.Errorf("block[0].name = %q, want web_search_20250305", block.Name)
	}
	if block.ID != "stu_1" {
		t.Errorf("block[0].id = %q, want stu_1", block.ID)
	}
	// Verify input was accumulated
	var input map[string]string
	if err := json.Unmarshal(block.Input, &input); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if input["query"] != "Go documentation" {
		t.Errorf("input.query = %q, want %q", input["query"], "Go documentation")
	}

	// Second block should be text
	if result.contentBlocks[1].Type != "text" {
		t.Errorf("block[1].type = %q, want text", result.contentBlocks[1].Type)
	}
	if result.contentBlocks[1].Text != "Here are the results." {
		t.Errorf("block[1].text = %q", result.contentBlocks[1].Text)
	}
}

func TestParseAgenticStream_ServerToolUseNotInToolCalls(t *testing.T) {
	// Verify that server_tool_use blocks are NOT treated as local tool calls.
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"stu_1","name":"web_search_20250305"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"test\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"read_file"}}`,
		`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"main.go\"}"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":8}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.contentBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.contentBlocks))
	}

	// First should be server_tool_use
	if result.contentBlocks[0].Type != "server_tool_use" {
		t.Errorf("block[0].type = %q, want server_tool_use", result.contentBlocks[0].Type)
	}
	// Second should be regular tool_use
	if result.contentBlocks[1].Type != "tool_use" {
		t.Errorf("block[1].type = %q, want tool_use", result.contentBlocks[1].Type)
	}
	if result.contentBlocks[1].Name != "read_file" {
		t.Errorf("block[1].name = %q, want read_file", result.contentBlocks[1].Name)
	}
}

func TestParseAgenticStream_WebFetchToolResultBlock(t *testing.T) {
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":12}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"stu_1","name":"web_fetch"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"url\":\"https://example.com\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"web_fetch_tool_result","tool_use_id":"stu_1","name":"web_fetch","content":{"type":"web_fetch_result","url":"https://example.com"}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":6}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.contentBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.contentBlocks))
	}
	if result.contentBlocks[1].Type != "web_fetch_tool_result" {
		t.Errorf("block[1].type = %q, want web_fetch_tool_result", result.contentBlocks[1].Type)
	}
	if result.contentBlocks[1].ToolUseID != "stu_1" {
		t.Errorf("block[1].tool_use_id = %q, want stu_1", result.contentBlocks[1].ToolUseID)
	}
	if !strings.Contains(string(result.contentBlocks[1].Content), "web_fetch_result") {
		t.Errorf("block[1].content = %q, want serialized web_fetch_result payload", string(result.contentBlocks[1].Content))
	}
}

func TestIsAnthropicProviderNativeToolName(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"web_search", true},
		{"web_search_20250305", true},
		{"web_search_20260209", true},
		{"web_fetch", true},
		{"web_fetch_20250910", true},
		{"web_fetch_20260209", true},
		{"web_fetch_20260309", true},
		{"read_file", false},
		{"bash", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isAnthropicProviderNativeToolName(tc.name); got != tc.want {
			t.Errorf("isAnthropicProviderNativeToolName(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestIsAnthropicProviderToolResultBlockType(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"web_search_tool_result", true},
		{"web_fetch_tool_result", true},
		{"code_execution_tool_result", true},
		{"server_tool_result", true},
		{"tool_result", false},
		{"tool_use", false},
		{"", false},
	}
	for _, tc := range tests {
		if got := isAnthropicProviderToolResultBlockType(tc.name); got != tc.want {
			t.Errorf("isAnthropicProviderToolResultBlockType(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestLegacyAnthropicToolCallsUnaffected(t *testing.T) {
	// Regression test: verify normal tool_use behavior works without web search.
	stream := buildSSE([]string{
		`{"type":"message_start","message":{"id":"msg_1","model":"claude-sonnet-4-20250514","usage":{"input_tokens":10}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"tu_1","name":"bash"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"command\":"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"echo hello\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":6}}`,
		`{"type":"message_stop"}`,
	})

	client := &Client{}
	result, err := client.parseAgenticStream(strings.NewReader(stream), nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(result.contentBlocks) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.contentBlocks))
	}
	block := result.contentBlocks[0]
	if block.Type != "tool_use" {
		t.Errorf("type = %q, want tool_use", block.Type)
	}
	if block.Name != "bash" {
		t.Errorf("name = %q, want bash", block.Name)
	}
	var input map[string]string
	if err := json.Unmarshal(block.Input, &input); err != nil {
		t.Fatalf("unmarshal input: %v", err)
	}
	if input["command"] != "echo hello" {
		t.Errorf("command = %q, want %q", input["command"], "echo hello")
	}
	if result.stopReason != "tool_use" {
		t.Errorf("stopReason = %q, want tool_use", result.stopReason)
	}
}
