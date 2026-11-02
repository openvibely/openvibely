package openai_compatible

import (
	"context"
	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/llm/requestjson"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
)

// EstimateHistoryTokens uses the same replay representation as the adapter,
// avoiding duplicate execution fields and counting images as images, not base64 text.
func EstimateHistoryTokens(history []models.Execution) int {
	return EstimateHistoryTokensContext(context.Background(), history)
}

func EstimateHistoryTokensContext(ctx context.Context, history []models.Execution) int {
	messages := preparedhistory.Read(ctx, history, func(h []models.Execution) []openaiclient.PreparedCompletionsHistory {
		return buildPreparedClientHistoryContext(ctx, h)
	})
	return messages[0].EstimatedTokens()
}

// Match encoding/json's default string escaping, including HTML, invalid UTF-8,
// and JavaScript line separators. Count bytes without allocating escaped text.
func jsonStringBytes(s string) int { return requestjson.StringSize(s) }
