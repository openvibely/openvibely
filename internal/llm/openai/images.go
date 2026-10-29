package openai

import (
	"context"
	"encoding/json"
	"fmt"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
)

// Reuse the existing generated-file service for ownership, durable storage,
// downloads, and file-change events. Nothing is published through a provider URL.
func openAIImagePublisher(initial *llmcontracts.RuntimeTools) func(context.Context, string) (string, error) {
	if initial == nil || initial.Executor == nil || initial.SkipDefaultTools || !initial.HasDefinition("publish_file") {
		return nil
	}
	return func(ctx context.Context, path string) (string, error) {
		runtime := llmcontracts.RuntimeToolsFromContext(ctx)
		if runtime == nil {
			runtime = initial
		}
		if runtime.Executor == nil || runtime.SkipDefaultTools || !runtime.HasDefinition("publish_file") {
			return "", fmt.Errorf("image publication is not available for this request")
		}
		input, err := json.Marshal(map[string]string{"file_path": path})
		if err != nil {
			return "", err
		}
		output, handled, failed, err := runtime.Executor(ctx, "publish_file", input)
		if err != nil {
			return "", err
		}
		if !handled || failed {
			return "", fmt.Errorf("image publication failed: %s", output)
		}
		return output, nil
	}
}
