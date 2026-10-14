package openai

import (
	"testing"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
)

func TestProcessToolUsesShellPermission(t *testing.T) {
	for _, grant := range []string{"Read", "Bash", "Shell"} {
		allowed := llmcontracts.AllowsBuiltInTool("interact_process", llmcontracts.BuiltInToolPolicyOptions{ConfiguredTools: []string{grant}, MapToolName: mapBuiltInToolName})
		if allowed != (grant != "Read") {
			t.Fatalf("grant %s: allowed=%v", grant, allowed)
		}
	}
	filter := llmcontracts.ComposeRuntimeToolFilter(nil, nil, llmcontracts.RuntimeToolPolicyOptions{ChatMode: models.ChatModePlan})
	if filter("interact_process") {
		t.Fatal("plan mode must not send stdin or terminate processes")
	}
}
