package openaiclient

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestRestoredCheckpointFirstSave(t *testing.T) {
	for _, tc := range []struct {
		name                                                string
		continuation, environment, textContent, replacement bool
	}{
		{name: "new-exchange"},
		{name: "appended-environment", environment: true},
		{name: "inserted-environment", environment: true, textContent: true, replacement: true},
		{name: "explicit-replacement", continuation: true, replacement: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stop := errors.New("stop after first durable save")
			calls := 0
			var content any = []any{map[string]any{"type": "input_text", "text": "SAVED_CHECKPOINT_MARKER"}}
			if tc.textContent {
				content = "SAVED_CHECKPOINT_MARKER"
			}
			opts := &AgenticOptions{
				Model: "gpt-5.3-codex", MaxTurns: 1, SkipDefaultTools: true, OmitExecutionEnvironment: !tc.environment,
				InitialInputItems:   []any{map[string]any{"type": "message", "role": "user", "content": content}},
				ContinueFromHistory: tc.continuation,
				OnReplayChanged: func(ctx context.Context, update ResponsesReplayUpdate) error {
					calls++
					raw, err := json.Marshal(update.Items)
					if err != nil {
						t.Fatal(err)
					}
					if update.ReplacesHistory != tc.replacement {
						t.Fatalf("replacement=%v, want %v", update.ReplacesHistory, tc.replacement)
					}
					if strings.Contains(string(raw), "SAVED_CHECKPOINT_MARKER") != tc.replacement {
						t.Fatalf("incorrect saved checkpoint inclusion: %s", raw)
					}
					if !tc.continuation && !strings.Contains(string(raw), "NEW_FOLLOWUP_MARKER") {
						t.Fatalf("new message missing: %s", raw)
					}
					if tc.environment && !strings.Contains(string(raw), "openvibely_execution_environment") {
						t.Fatalf("environment missing: %s", raw)
					}
					return stop
				},
			}
			_, err := NewWithAPIKey("local-test-only").SendAgentic(context.Background(), "NEW_FOLLOWUP_MARKER", opts)
			if !errors.Is(err, stop) {
				t.Fatalf("save failure must stop before sending: %v", err)
			}
			if calls != 1 {
				t.Fatalf("save calls=%d, want 1", calls)
			}
		})
	}
}
