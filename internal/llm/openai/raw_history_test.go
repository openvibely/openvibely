package openai

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/jsonview"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"testing"
)

func TestPreparedResponsesHistoryBorrowsToolText(t *testing.T) {
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows("responses_input", []json.RawMessage{
		json.RawMessage(`{"type":"function_call","call_id":"c","name":"read_file","arguments":"{}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"c","output":"tool text <>&世"}`),
	})}}}
	decoded := buildClientHistory(history)[0].ResponsesInputItems
	prepared := buildPreparedHistory(history)[0].ResponsesInputItems
	if _, ok := prepared[1].(map[string]any)["output"].(jsonview.Value); !ok {
		t.Fatal("output decoded")
	}
	a, _ := json.Marshal(decoded)
	b, _ := json.Marshal(prepared)
	if string(a) != string(b) {
		t.Fatalf("%s != %s", a, b)
	}
	if got, want := openaiclient.EstimateResponsesInputTokens(prepared), openaiclient.EstimateResponsesInputTokens(decoded); got != want {
		t.Fatal(got, want)
	}
}

func TestPreparedResponsesPreservesEscapedTextBudgets(t *testing.T) {
	for _, text := range []string{`"\u0061"`, `"\ud83d\ude00"`, `"\u0026"`, `"literal <>&"`, `"\n\t\\"`} {
		raw := json.RawMessage(`{"type":"function_call_output","call_id":"c","output":` + text + `}`)
		history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows("responses_input", []json.RawMessage{raw})}}}
		decoded := buildClientHistory(history)[0].ResponsesInputItems
		prepared := buildPreparedHistory(history)[0].ResponsesInputItems
		if got, want := openaiclient.EstimateResponsesInputTokens(prepared), openaiclient.EstimateResponsesInputTokens(decoded); got != want {
			t.Fatalf("%s: %d != %d", text, got, want)
		}
	}
}

func TestBorrowedUserTextRetainedDuringCompaction(t *testing.T) {
	history := []models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{models.ReplayFromRows("responses_input", []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":[{"type":"input_text","text":"keep this request"},{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}`),
	})}}}
	users := UserMessageHistory(history)
	if len(users) != 1 || users[0].PromptSent != "keep this request" {
		t.Fatalf("user text lost: %#v", users)
	}
	var retained map[string]any
	if err := json.Unmarshal(users[0].RetainedUserMessage, &retained); err != nil {
		t.Fatal(err)
	}
	if len(retained["content"].([]any)) != 1 {
		t.Fatal("image retained in text-only compaction context")
	}
}
