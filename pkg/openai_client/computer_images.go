package openaiclient

import "github.com/openvibely/openvibely/pkg/agenttools"

// Attach screenshots after all tool results in a batch to preserve tool-call pairing.
func completionsComputerImages(messages []completionsMessage) []completionsMessage {
	out := make([]completionsMessage, 0, len(messages))
	var images []map[string]any
	flush := func() {
		if len(images) > 0 {
			out = append(out, completionsMessage{Role: "user", Content: images})
			images = nil
		}
	}
	for _, msg := range messages {
		if msg.Role != "tool" {
			flush()
		}
		if raw, ok := msg.Content.(string); ok && msg.Role == "tool" {
			if result, ok := agenttools.DecodeImageResult(raw); ok {
				msg.Content = agenttools.ImageResultSummary(raw)
				images = append(images, map[string]any{"type": "text", "text": "Screenshot from tool call " + msg.ToolCallID})
				for _, img := range result.Content {
					images = append(images, map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + img.MIME + ";base64," + img.Data}})
				}
			}
		}
		out = append(out, msg)
	}
	flush()
	return out
}
