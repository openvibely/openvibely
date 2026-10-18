package openai_compatible

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
)

// EstimateHistoryTokens uses the same replay representation as the adapter,
// avoiding duplicate execution fields and counting images as images, not base64 text.
func EstimateHistoryTokens(history []models.Execution) int {
	messages := buildClientHistory(history)
	if len(messages) == 0 {
		return 0
	}
	raw, err := json.Marshal(map[string]any{"messages": messages})
	if err != nil {
		return 0
	}
	tokens, _ := tokenestimate.FromMessagesJSON(raw, "image_url", 2000)
	return tokens
}
