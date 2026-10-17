package ollama

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/openvibely/openvibely/pkg/managedcontext"
)

// ConversationError retains the failed tool conversation for local compaction.
type ConversationError struct {
	Err          error
	MessagesJSON string
}

func (e *ConversationError) Error() string { return e.Err.Error() }
func (e *ConversationError) Unwrap() error { return e.Err }

type runtimeReplay struct {
	Messages []chatMessage `json:"ollama_messages"`
}

// callRuntime executes the supplied runtime tools and retains structured exchanges
// for the next turn. Local models must support Ollama's tool-calling protocol.
func (a *Adapter) callRuntime(ctx context.Context, req llmcontracts.AgentRequest, workDir string) (result llmcontracts.AgentResult, callErr error) {
	rt := llmcontracts.RuntimeToolsFromContext(ctx)
	system := llmprompt.BuildAgentSystemPrompt(req.ProjectInstructions, workDir)
	if req.Followup || req.ChatHistory != nil {
		system = llmprompt.BuildChatSystemPrompt(req.Followup, req.ChatMode, req.ChatSystemContext, false)
	}
	messages := buildChatHistory(system, req.ChatHistory)
	user := chatMessage{Role: "user", Content: req.Message}
	if err := prepareUserAttachments(ctx, &user, req.Attachments); err != nil {
		return result, err
	}
	if !llmcontracts.HistoryContinuationFromContext(ctx) {
		messages = append(messages, user)
	}
	messages = ensureExecutionEnvironment(messages, workDir)
	read := func(m chatMessage) (string, string) {
		if len(m.ToolCalls) > 0 {
			return "", ""
		}
		return m.Role, m.Content
	}
	makeMessage := func(text string) chatMessage { return chatMessage{Role: "user", Content: text} }
	replay := []chatMessage{{Role: "user", Content: user.Content}} // Never pin uploaded bytes.
	replay = managedcontext.SeedReplay(ctx, messages, replay, read, makeMessage)
	defer func() {
		if callErr != nil {
			data, _ := json.Marshal(messages)
			callErr = &ConversationError{Err: callErr, MessagesJSON: string(data)}
		}
		if a.execRepo == nil || req.ExecID == "" {
			return
		}
		data, err := json.Marshal(runtimeReplay{replay})
		if err == nil {
			err = a.execRepo.ReplaceReasoningReplay(context.WithoutCancel(ctx), req.ExecID, "", []models.ExecutionReplayMessage{{UserContent: req.Message, AssistantContent: result.TextOnlyOutput, TranscriptJSON: string(data)}})
		}
		if err != nil {
			callErr = errors.Join(callErr, err)
		}
	}()
	filter := llmcontracts.ComposeRuntimeToolFilter(nil, rt, llmcontracts.RuntimeToolPolicyOptions{ChatMode: req.ChatMode, IsTaskFollowup: req.Followup || req.Operation == llmcontracts.OperationTask})
	tools := []Tool{}
	for _, def := range rt.Definitions {
		if !filter(def.Name) {
			continue
		}
		tools = append(tools, Tool{Type: "function", Function: Function{Name: def.Name, Description: def.Description, Parameters: def.ParameterSchema()}})
	}
	sw := llmstream.NewWriterWithPublisher(req.ExecID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()
	for step := 0; step < 128; step++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		messages = managedcontext.Ensure(ctx, messages, read, makeMessage)
		messages = attachmentcontext.Ensure(ctx, messages, read, makeMessage)
		body, err := json.Marshal(chatRequest{Model: req.Agent.Model, Messages: messages, Options: ollamaRequestOptions(req.Agent), Tools: tools})
		if err != nil {
			return result, err
		}
		if err = ensureOllamaRequestFits(body, req.Agent); err != nil {
			return result, err
		}
		response, err := a.bufferedWithRetry(ctx, strings.TrimRight(req.Agent.GetOllamaBaseURL(), "/")+"/api/chat", body)
		if err != nil {
			return result, err
		}
		if response.statusCode != http.StatusOK {
			return result, ollamaResponseError(response.statusCode, response.body)
		}
		var turn chatResponse
		if err = json.Unmarshal(response.body, &turn); err != nil {
			return result, err
		}
		messages = append(messages, turn.Message)
		replay = append(replay, turn.Message)
		result.Usage.InputTokens += turn.PromptEvalCount
		result.Usage.OutputTokens += turn.EvalCount
		result.Usage.TotalTokens += turn.PromptEvalCount + turn.EvalCount
		if turn.Message.Content != "" {
			result.Output += turn.Message.Content
			result.TextOnlyOutput += turn.Message.Content
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: turn.Message.Content}, false)
		}
		if len(turn.Message.ToolCalls) == 0 {
			return result, nil
		}
		for _, call := range turn.Message.ToolCalls {
			name := call.Function.Name
			output, handled, isErr, err := "Tool not permitted", true, true, error(nil)
			allowed := rt.HasDefinition(name) && filter(name)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolUse, ToolName: name}, false)
			if allowed && rt.Executor != nil {
				output, handled, isErr, err = rt.Executor(ctx, name, call.Function.Arguments)
			}
			if err != nil {
				output = err.Error()
				isErr = true
			}
			if !handled {
				output = "Unknown tool"
				isErr = true
			}
			message := chatMessage{Role: "tool", Content: output, ToolName: name}
			messages = append(messages, message)
			replay = append(replay, message)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventToolResult, ToolName: name, Output: output, IsError: isErr}, false)
		}
		if steering := llmcontracts.SteeringCallbackFromContext(ctx); steering != nil {
			text, err := steering(ctx)
			if err != nil {
				return result, err
			}
			if text != "" {
				m := chatMessage{Role: "user", Content: text}
				messages = append(messages, m)
				replay = append(replay, m)
			}
		}
	}
	return result, fmt.Errorf("Ollama runtime tool turn limit reached")
}
