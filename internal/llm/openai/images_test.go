package openai

import (
	"context"
	"encoding/json"
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestImagePublisherUsesRuntimeArtifactService(t *testing.T) {
	called := false
	runtime := &llmcontracts.RuntimeTools{
		Definitions: []llmcontracts.RuntimeToolDefinition{{Name: "publish_file", Access: llmcontracts.RuntimeToolAccessWrite}},
		Executor: func(ctx context.Context, name string, input json.RawMessage) (string, bool, bool, error) {
			called = true
			var args map[string]string
			if err := json.Unmarshal(input, &args); err != nil {
				t.Fatal(err)
			}
			if name != "publish_file" || args["file_path"] != "/tmp/generated.png" {
				t.Fatalf("publisher call: %s %s", name, input)
			}
			return `{"id":"artifact","url":"/files/artifact?project_id=project"}`, true, false, nil
		},
	}
	publish := openAIImagePublisher(runtime)
	if publish == nil {
		t.Fatal("publisher missing")
	}
	out, err := publish(context.Background(), "/tmp/generated.png")
	if err != nil || !called || out != `{"id":"artifact","url":"/files/artifact?project_id=project"}` {
		t.Fatalf("publication: %q %v", out, err)
	}

	called = false
	blocked := *runtime
	blocked.Filter = func(string) (bool, bool) { return false, true }
	ctx := llmcontracts.WithRuntimeTools(context.Background(), &blocked)
	if _, err := publish(ctx, "/tmp/generated.png"); err == nil || called {
		t.Fatal("revoked publication was executed")
	}
	if openAIImagePublisher(&blocked) != nil {
		t.Fatal("publisher exposed for denied tool")
	}
	blocked.Filter = nil
	blocked.SkipDefaultTools = true
	if openAIImagePublisher(&blocked) != nil {
		t.Fatal("publisher exposed in restricted runtime")
	}
	if openAIImagePublisher(nil) != nil {
		t.Fatal("publisher exposed without runtime")
	}

	filter := llmcontracts.ComposeRuntimeToolFilter(nil, runtime, runtimeToolPolicyOptions(false, models.ChatModePlan))
	if filter("publish_file") {
		t.Fatal("image publication permission allowed in Plan mode")
	}
}
