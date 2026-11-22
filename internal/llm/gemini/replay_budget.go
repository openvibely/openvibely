package gemini

import (
	"context"

	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/llm/tokenestimate"
	"github.com/openvibely/openvibely/internal/models"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
)

// EstimateHistoryTokensContext budgets the same contents consumed by the adapter.
func EstimateHistoryTokensContext(ctx context.Context, history []models.Execution) int {
	prepared := preparedhistory.Read(ctx, history, func(h []models.Execution) [][]geminiclient.Content {
		return [][]geminiclient.Content{BuildHistory(h)}
	})
	total := 0
	for _, contents := range prepared {
		tokens, err := geminiclient.EstimateContentsTokens(contents)
		if err != nil {
			// Preserve a conservative estimate if an invalid replay cannot be encoded.
			total = 0
			for _, exec := range history {
				total += tokenestimate.FromText(exec.PromptSent + exec.Output + exec.ErrorMessage)
				for _, replay := range exec.ReplayMessages {
					total += tokenestimate.FromByteCount(replay.TranscriptBytes())
				}
			}
			return total
		}
		total += tokens
	}
	return total
}
