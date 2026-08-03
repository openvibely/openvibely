package handler

import (
	"os"
	"strings"
	"testing"
)

func TestAllStreamingExecutionIngressUsesSynchronousUpdateAdmission(t *testing.T) {
	files := []string{"api_chat_handler.go", "channel_chat_runner.go", "chat_handler.go", "chat_processing.go", "review_handler.go", "swarm_handler.go", "task_handler.go"}
	for _, name := range files {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if name != "chat_processing.go" && strings.Contains(text, "go h.processStreamingResponse(") {
			t.Errorf("%s bypasses synchronous admission", name)
		}
		if name != "chat_processing.go" && !strings.Contains(text, "h.startStreamingResponse(") {
			t.Errorf("%s does not use shared admission launcher", name)
		}
	}
}
