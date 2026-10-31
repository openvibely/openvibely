package openai

import (
	"context"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/preparedhistory"
	"github.com/openvibely/openvibely/internal/models"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
	"github.com/stretchr/testify/require"
)

func TestRecoveryTakesPreparedHistory(t *testing.T) {
	ctx := preparedhistory.WithScope(context.Background())
	history := []models.Execution{{PromptSent: "question", Output: "answer"}}
	calls := 0
	build := func(h []models.Execution) []openaiclient.Message {
		calls++
		return buildClientHistory(h)
	}
	preparedhistory.Read(ctx, history, build)
	require.Equal(t, HistoryInputItems(history), TakeHistoryInputItems(ctx, history))
	preparedhistory.Read(ctx, history, build)
	require.Equal(t, 2, calls, "recovery must release the estimator cache")
}
