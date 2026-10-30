package transcript

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestReplayCodecPreservesProviderShapes(t *testing.T) {
	for _, raw := range []string{
		``, `[]`, `null`,
		`[{"role":"assistant","reasoning_content":"reason","tool_calls":[{"id":"c","function":{"name":"read","arguments":"{}"}}]}]`,
		`{"responses_input":[{"type":"reasoning","encrypted_content":"secret"},{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA=="}]}],"future_field":{"keep":true}}`,
		`{"anthropic_execution_environment":"env","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"result"}]}]}`,
		`{"unknown_provider":{"preserve":true}}`,
		`{"responses_input":null,"future_field":"keep"}`,
		`{"messages":[],"future_field":"keep"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			original := models.ExecutionReplayMessage{TranscriptJSON: raw, UserContent: "user", AssistantContent: "assistant", ReasoningContent: "reason"}
			header, items, err := SplitReplay(original)
			if err != nil {
				t.Fatal(err)
			}
			restored, err := JoinReplay(header, items)
			if err != nil {
				t.Fatal(err)
			}
			builder, err := NewReplayBuilder(header, len(raw)+128)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range items {
				if err := builder.AppendValidatedJSON(item); err != nil {
					t.Fatal(err)
				}
			}
			built, err := builder.Finish()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(restored, built) {
				// JSON object key order is not part of the provider contract.
				var joinedJSON, builtJSON any
				if err := json.Unmarshal([]byte(restored.TranscriptJSON), &joinedJSON); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(built.TranscriptJSON), &builtJSON); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(joinedJSON, builtJSON) {
					t.Fatalf("builder changed JSON: %s", built.TranscriptJSON)
				}
				built.TranscriptJSON = restored.TranscriptJSON
				if !reflect.DeepEqual(restored, built) {
					t.Fatal("builder lost metadata")
				}
			}
			var a, b any
			if raw != "" {
				if err := json.Unmarshal([]byte(raw), &a); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(restored.TranscriptJSON), &b); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("changed JSON: %s", restored.TranscriptJSON)
			}
			restored.TranscriptJSON = raw
			if !reflect.DeepEqual(original, restored) {
				t.Fatal("lost replay metadata")
			}
		})
	}
}
