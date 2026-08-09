package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/httpretry"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	llmstream "github.com/openvibely/openvibely/internal/llm/stream"
	llmusage "github.com/openvibely/openvibely/internal/llm/usage"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// HTTPDoer is an interface for making HTTP requests.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

const defaultOllamaRequestTimeout = 10 * time.Minute

// DefaultHTTPClient is the default HTTP client for Ollama requests.
var DefaultHTTPClient HTTPDoer = &http.Client{Timeout: defaultOllamaRequestTimeout}

var retryAfter = time.After

// Adapter encapsulates Ollama provider logic.
type Adapter struct {
	execRepo   *repository.ExecutionRepo
	streamHub  llmstream.ExecutionStreamPublisher
	httpClient HTTPDoer
}

// New creates a new Ollama adapter.
func New(execRepo *repository.ExecutionRepo, streamHub llmstream.ExecutionStreamPublisher) *Adapter {
	return &Adapter{
		execRepo:   execRepo,
		streamHub:  streamHub,
		httpClient: DefaultHTTPClient,
	}
}

// SetHTTPClient allows overriding the HTTP client (for tests).
func (a *Adapter) SetHTTPClient(client HTTPDoer) {
	a.httpClient = client
}

// Call handles Ollama LLM requests.
func (a *Adapter) Call(ctx context.Context, req llmcontracts.AgentRequest, workDir string, w *llmstream.Writer) (llmcontracts.AgentResult, error) {
	agent := req.Agent

	switch req.Operation {
	case llmcontracts.OperationTask:
		output, textOnly, tokens, err := a.callStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, req.ProjectInstructions)
		return llmcontracts.AgentResult{
			Output:         output,
			TextOnlyOutput: textOnly,
			Usage:          llmusage.FromTotal(tokens),
		}, err

	case llmcontracts.OperationStreaming:
		if req.Followup || req.ChatHistory != nil || req.ChatMode == models.ChatModeOrchestrate || req.ChatMode == models.ChatModePlan {
			output, tokens, err := a.callChat(ctx, req.Message, req.Attachments, agent, req.ExecID, req.ChatHistory, req.ChatSystemContext, req.Followup, req.ChatMode)
			return llmcontracts.AgentResult{
				Output: output,
				Usage:  llmusage.FromTotal(tokens),
			}, err
		}
		output, textOnly, tokens, err := a.callStreaming(ctx, req.Message, req.Attachments, agent, req.ExecID, req.ProjectInstructions)
		return llmcontracts.AgentResult{
			Output:         output,
			TextOnlyOutput: textOnly,
			Usage:          llmusage.FromTotal(tokens),
		}, err

	case llmcontracts.OperationDirect:
		output, tokens, err := a.callDirect(ctx, req.Message, req.Attachments, agent, req.ProjectInstructions)
		return llmcontracts.AgentResult{
			Output: output,
			Usage:  llmusage.FromTotal(tokens),
		}, err

	default:
		return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
	}
}

// callDirect calls the Ollama API for task execution (non-chat).
// projectInstructions carries the calling agent's own system prompt; the
// provider wrapper folds the agent definition into it.
func (a *Adapter) callDirect(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, projectInstructions string) (string, int, error) {
	baseURL := agent.GetOllamaBaseURL()
	applog.Infof("[ollama] callDirect model=%s base_url=%s prompt_len=%d attachments=%d", agent.Model, baseURL, len(prompt), len(attachments))

	opts := &options{Temperature: &agent.Temperature}

	userMsg := chatMessage{Role: "user", Content: prompt}
	if images := encodeImageAttachments(attachments); len(images) > 0 {
		userMsg.Images = images
	}

	messages := make([]chatMessage, 0, 2)
	if strings.TrimSpace(projectInstructions) != "" {
		messages = append(messages, chatMessage{Role: "system", Content: projectInstructions})
	}
	messages = append(messages, userMsg)

	reqBody := chatRequest{
		Model:    agent.Model,
		Messages: messages,
		Stream:   false,
		Options:  opts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling ollama request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/api/chat"
	buffered, err := a.bufferedWithRetry(ctx, url, body)
	if err != nil {
		return "", 0, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}

	if buffered.statusCode != http.StatusOK {
		var errResp errorResponse
		if json.Unmarshal(buffered.body, &errResp) == nil && errResp.Error != "" {
			return "", 0, fmt.Errorf("ollama API error (%d): %s", buffered.statusCode, errResp.Error)
		}
		return "", 0, fmt.Errorf("ollama API error (%d): %s", buffered.statusCode, string(buffered.body))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(buffered.body, &chatResp); err != nil {
		return "", 0, fmt.Errorf("parsing ollama response: %w", err)
	}

	output := chatResp.Message.Content
	tokens := chatResp.EvalCount
	applog.Infof("[ollama] callDirect success model=%s eval_tokens=%d prompt_tokens=%d output_len=%d",
		agent.Model, tokens, chatResp.PromptEvalCount, len(output))
	return output, tokens, nil
}

// callChat calls the Ollama API for Chat and task follow-up requests.
func (a *Adapter) callChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode) (string, int, error) {
	baseURL := agent.GetOllamaBaseURL()
	applog.Infof("[ollama] callChat model=%s base_url=%s history=%d message_len=%d attachments=%d exec=%s isTaskFollowup=%v",
		agent.Model, baseURL, len(chatHistory), len(message), len(attachments), execID, isTaskFollowup)

	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	if chatMode == models.ChatModeOrchestrate {
		systemPromptStr = llmprompt.ApplyChatActionToolMode(systemPromptStr, nil)
	}
	messages := buildChatHistory(systemPromptStr, chatHistory)

	userMsg := chatMessage{Role: "user", Content: message}
	if images := encodeImageAttachments(attachments); len(images) > 0 {
		userMsg.Images = images
	}
	messages = append(messages, userMsg)

	opts := &options{Temperature: &agent.Temperature}

	reqBody := chatRequest{
		Model:    agent.Model,
		Messages: messages,
		Stream:   true,
		Options:  opts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling ollama chat request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/api/chat"
	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()
	streamResult, err := a.streamWithRetry(ctx, url, body, func(chunk chatStreamChunk) bool {
		if chunk.Message.Content != "" {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: chunk.Message.Content}, true)
			return true
		}
		return false
	})
	if err != nil {
		sw.Flush()
		return "", 0, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}

	sw.Flush()
	output := sw.String()
	applog.Infof("[ollama] callChat success model=%s eval_tokens=%d prompt_tokens=%d output_len=%d",
		agent.Model, streamResult.totalTokens, streamResult.promptTokens, len(output))
	return output, streamResult.totalTokens, nil
}

// callStreaming calls Ollama with streaming for task execution.
func (a *Adapter) callStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, projectInstructions string) (string, string, int, error) {
	baseURL := agent.GetOllamaBaseURL()
	applog.Infof("[ollama] callStreaming model=%s base_url=%s prompt_len=%d attachments=%d exec=%s", agent.Model, baseURL, len(prompt), len(attachments), execID)

	opts := &options{Temperature: &agent.Temperature}

	var messages []chatMessage
	// Inject system prompt with project instructions for task execution
	systemPrompt := llmprompt.BuildAgentSystemPrompt(projectInstructions)
	messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})

	userMsg := chatMessage{Role: "user", Content: llmprompt.ApplyTaskCreationToolMode(prompt, nil)}
	if images := encodeImageAttachments(attachments); len(images) > 0 {
		userMsg.Images = images
	}
	messages = append(messages, userMsg)

	reqBody := chatRequest{
		Model:    agent.Model,
		Messages: messages,
		Stream:   true,
		Options:  opts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", "", 0, fmt.Errorf("marshaling ollama streaming request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/api/chat"
	sw := llmstream.NewWriterWithPublisher(execID, "", a.execRepo, ctx, 500*time.Millisecond, a.streamHub)
	defer sw.Stop()
	var thinkingBuf strings.Builder
	var textBuf strings.Builder
	streamResult, err := a.streamWithRetry(ctx, url, body, func(chunk chatStreamChunk) bool {
		observed := false
		if chunk.Message.Thinking != "" {
			observed = true
			thinkingBuf.WriteString(chunk.Message.Thinking)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingText, Text: chunk.Message.Thinking}, false)
		}
		if chunk.Message.Content != "" {
			observed = true
			textBuf.WriteString(chunk.Message.Content)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: chunk.Message.Content}, false)
		}
		return observed
	})
	if err != nil {
		sw.Flush()
		return "", "", 0, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}

	sw.Flush()
	output := sw.String()
	textOutput := textBuf.String()
	if thinkingBuf.Len() == 0 {
		textOutput = output
	}
	applog.Infof("[ollama] callStreaming success model=%s eval_tokens=%d prompt_tokens=%d output_len=%d",
		agent.Model, streamResult.totalTokens, streamResult.promptTokens, len(output))
	return output, textOutput, streamResult.totalTokens, nil
}

type ollamaStreamResult struct {
	totalTokens  int
	promptTokens int
}

type ollamaBufferedResponse struct {
	statusCode int
	body       []byte
}

func (a *Adapter) bufferedWithRetry(ctx context.Context, url string, body []byte) (ollamaBufferedResponse, error) {
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	policy.After = retryAfter
	policy.OnRetry = func(event httpretry.RetryEvent) {
		applog.Infof("[ollama] response read error, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
	}
	return httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (ollamaBufferedResponse, bool, error) {
		result := ollamaBufferedResponse{}
		resp, err := a.doWithRetry(attemptCtx, func() (*http.Request, error) {
			httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			httpReq.Header.Set("Content-Type", "application/json")
			return httpReq, nil
		})
		if err != nil {
			return result, false, err
		}
		defer resp.Body.Close()
		result.statusCode = resp.StatusCode
		result.body, err = io.ReadAll(resp.Body)
		if err != nil {
			return result, false, httpretry.NewStreamError(fmt.Errorf("reading ollama response: %w", err))
		}
		if httpretry.IsRetryableStatus(resp.StatusCode) {
			return result, false, httpretry.NewResponseError(resp, ollamaResponseError(resp.StatusCode, result.body))
		}
		return result, false, nil
	})
}

func (a *Adapter) streamWithRetry(ctx context.Context, url string, body []byte, onChunk func(chatStreamChunk) bool) (ollamaStreamResult, error) {
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	policy.After = retryAfter
	policy.OnRetry = func(event httpretry.RetryEvent) {
		applog.Infof("[ollama] stream error before output, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
	}
	return httpretry.DoStream(ctx, policy, func(attemptCtx context.Context) (ollamaStreamResult, bool, error) {
		result := ollamaStreamResult{}
		observed := false
		resp, err := a.doWithRetry(attemptCtx, func() (*http.Request, error) {
			httpReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, url, bytes.NewReader(body))
			if err != nil {
				return nil, err
			}
			httpReq.Header.Set("Content-Type", "application/json")
			return httpReq, nil
		})
		if err != nil {
			return result, observed, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			respBody, _ := io.ReadAll(resp.Body)
			err := ollamaResponseError(resp.StatusCode, respBody)
			return result, observed, httpretry.NewResponseError(resp, err)
		}

		decoder := json.NewDecoder(resp.Body)
		for {
			var chunk chatStreamChunk
			if err := decoder.Decode(&chunk); err != nil {
				if ctxErr := ctx.Err(); ctxErr != nil {
					return result, observed, fmt.Errorf("ollama streaming cancelled: %w", ctxErr)
				}
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				return result, observed, httpretry.NewStreamError(fmt.Errorf("decoding ollama stream chunk: %w", err))
			}
			observed = onChunk(chunk) || observed
			if chunk.Done {
				result.totalTokens = chunk.EvalCount
				result.promptTokens = chunk.PromptEvalCount
				return result, observed, nil
			}
		}
	})
}

func ollamaResponseError(statusCode int, body []byte) error {
	var errResp errorResponse
	if json.Unmarshal(body, &errResp) == nil && errResp.Error != "" {
		return fmt.Errorf("ollama API error (%d): %s", statusCode, errResp.Error)
	}
	return fmt.Errorf("ollama API error (%d): %s", statusCode, string(body))
}

func (a *Adapter) doWithRetry(ctx context.Context, buildReq func() (*http.Request, error)) (*http.Response, error) {
	policy := httpretry.DefaultPolicy()
	policy.AllowReplay = true
	policy.After = retryAfter
	policy.OnRetry = func(event httpretry.RetryEvent) {
		if event.Err != nil {
			applog.Infof("[ollama] network error, retry attempt %d/%d in %v: %v", event.Attempt, event.MaxRetries, event.Delay, event.Err)
			return
		}
		applog.Infof("[ollama] received HTTP %d, retry attempt %d/%d in %v", event.StatusCode, event.Attempt, event.MaxRetries, event.Delay)
	}
	return httpretry.Do(ctx, a.httpClient, buildReq, policy)
}

// buildChatHistory converts execution history to Ollama chat messages.
func buildChatHistory(systemPrompt string, history []models.Execution) []chatMessage {
	var messages []chatMessage

	if systemPrompt != "" {
		messages = append(messages, chatMessage{
			Role:    "system",
			Content: systemPrompt,
		})
	}

	for _, exec := range history {
		if exec.PromptSent != "" {
			messages = append(messages, chatMessage{
				Role:    "user",
				Content: exec.PromptSent,
			})
		}
		if replay := llmprompt.ReplayAssistantContent(exec); replay != "" {
			messages = append(messages, chatMessage{
				Role:    "assistant",
				Content: replay,
			})
		}
	}

	return messages
}

// encodeImageAttachments reads image attachments from disk and returns base64-encoded strings.
func encodeImageAttachments(attachments []models.Attachment) []string {
	var images []string
	for _, att := range attachments {
		if !llmoutput.IsImageMediaType(att.MediaType) {
			continue
		}
		filePath := att.FilePath
		if !filepath.IsAbs(filePath) {
			if abs, err := filepath.Abs(filePath); err == nil {
				filePath = abs
			}
		}
		data, err := os.ReadFile(filePath)
		if err != nil {
			applog.Infof("[ollama] error reading image %s: %v", filePath, err)
			continue
		}
		images = append(images, base64.StdEncoding.EncodeToString(data))
		applog.Infof("[ollama] added image attachment %s (%s, %d bytes)", att.FileName, att.MediaType, len(data))
	}
	return images
}
