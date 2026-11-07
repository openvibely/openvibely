package service

import (
	"context"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestWorkspaceChatCompactionIsPersonal(t *testing.T) {
	s := &LLMService{}
	for _, transport := range []string{"", "chat:project:" + models.AllProjectsID} {
		var previous string
		for _, user := range []string{"alice", "bob"} {
			ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: user})
			req := llmcontracts.AgentRequest{Ctx: ctx, ProjectID: models.AllProjectsID, Operation: llmcontracts.OperationStreaming, TransportScope: transport}
			scope, ok := s.compactionScopeForRequest(req)
			require.True(t, ok)
			require.Equal(t, "chat_project", scope.Type)
			require.Equal(t, models.AllProjectsID+":user:"+user, scope.ID)
			require.NotEqual(t, previous, scope.ID)
			previous = scope.ID
			req.TransportScope = "chat:project:" + scope.ID
			again, ok := s.compactionScopeForRequest(req)
			require.True(t, ok)
			require.Equal(t, scope, again)
		}
	}
}

// Provider switches use a personal checkpoint key but still query the physical
// workspace project, with the caller's principal retained for row filtering.
type workspaceHistoryStore struct {
	persistence.ExecutionStore
	project string
	user    string
}

func (s *workspaceHistoryStore) ListChatHistoryBefore(ctx context.Context, project, before string, limit int) ([]models.Execution, error) {
	s.project = project
	p, _ := models.PrincipalFromContext(ctx)
	s.user = p.UserID
	return []models.Execution{{ID: "readable", Status: models.ExecCompleted, PromptSent: "Own question", Output: "Own answer"}}, nil
}
func TestWorkspaceChatProviderRecoveryRetainsUser(t *testing.T) {
	store := &workspaceHistoryStore{}
	svc := &LLMService{execRepo: store}
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: "alice"})
	_, err := svc.recoverPortableProviderHistory(llmcontracts.AgentRequest{
		Ctx: ctx, ProjectID: models.AllProjectsID,
		TransportScope: "chat:project:" + models.WorkspaceChatScope(ctx, models.AllProjectsID),
		Agent:          models.LLMConfig{Provider: models.ProviderAnthropic},
		ChatHistory:    []models.Execution{{ID: "previous", ReplayMessages: []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"compaction","encrypted_content":"opaque"}]}`}}}},
	})
	require.NoError(t, err)
	require.Equal(t, models.AllProjectsID, store.project)
	require.Equal(t, "alice", store.user)
}
