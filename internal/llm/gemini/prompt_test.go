package gemini

import (
	"context"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
	"testing"

	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestGooglePromptModeAndInternalRequests(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation contracts.Operation
		mode      models.ChatMode
		followup  bool
		raw       bool
	}{
		{name: "task", operation: contracts.OperationTask},
		{name: "chat", operation: contracts.OperationStreaming, mode: models.ChatModeOrchestrate},
		{name: "followup", operation: contracts.OperationStreaming, followup: true},
		{name: "raw", operation: contracts.OperationDirect, raw: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rs := newRecordingServer(t, func(int) []string { return []string{doneChunk} })
			req := contracts.AgentRequest{Ctx: context.Background(), Agent: geminiAgent(), Operation: tc.operation, ChatMode: tc.mode, Followup: tc.followup, RawDirectPrompt: tc.raw, Message: "hello", ProjectInstructions: "PROJECT_RULE"}
			_, err := rs.adapter().Call(req.Ctx, req, ".")
			require.NoError(t, err)
			text := systemText(rs.bodies[0])
			if tc.raw {
				require.NotContains(t, text, "OpenVibely runtime rules")
				return
			}
			require.Contains(t, text, "OpenVibely runtime rules")
			if tc.name == "chat" {
				require.Contains(t, text, "working with")
			} else {
				require.Contains(t, text, "working for")
			}
		})
	}
}

func TestGoogleInternalPromptSkipsConfiguration(t *testing.T) {
	a := New(nil, nil, nil)
	a.SetClientConfigurer(func(*geminiclient.Client) { t.Fatal("internal request attempted prompt configuration") })
	for _, req := range []contracts.AgentRequest{
		{Ctx: context.Background(), RawDirectPrompt: true},
		{Ctx: context.Background(), LifecycleHookCall: true},
		{Ctx: contracts.WithSummarySystemPrompt(context.Background(), "SUMMARY")},
	} {
		ctx := a.PreparePrompt(req)
		require.Nil(t, ctx.Value(basePromptKey{}))
	}
}

func TestPrepareBundledPromptDoesNotCreateClient(t *testing.T) {
	a := New(nil, nil, nil)
	a.SetClientConfigurer(func(*geminiclient.Client) {
		t.Fatal("prompt preparation must not create a client or fetch configuration")
	})
	for _, auth := range []models.AuthMethod{models.AuthMethodAPIKey, models.AuthMethodOAuth} {
		req := contracts.AgentRequest{Ctx: context.Background(), Operation: contracts.OperationTask, Agent: geminiAgent()}
		req.Agent.AuthMethod = auth
		req.Agent.Model = "gemini-4-hydrogen"
		req.Ctx = a.PreparePrompt(req)
		require.Contains(t, BaseSystemPrompt(req), "<code_quality>")
	}
}
