package ollama

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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

// DefaultHTTPClient is the default HTTP client for Ollama requests.
var DefaultHTTPClient HTTPDoer = &http.Client{Timeout: 5 * time.Minute}

// Adapter encapsulates Ollama provider logic.
type Adapter struct {
	execRepo   *repository.ExecutionRepo
	httpClient HTTPDoer
}

// New creates a new Ollama adapter.
func New(execRepo *repository.ExecutionRepo) *Adapter {
	return &Adapter{
		execRepo:   execRepo,
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
		if req.ChatHistory != nil {
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
		output, tokens, err := a.callDirect(ctx, req.Message, req.Attachments, agent)
		return llmcontracts.AgentResult{
			Output: output,
			Usage:  llmusage.FromTotal(tokens),
		}, err

	default:
		return llmcontracts.AgentResult{}, fmt.Errorf("unsupported operation: %s", req.Operation)
	}
}

// callDirect calls the Ollama API for task execution (non-chat).
func (a *Adapter) callDirect(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig) (string, int, error) {
	baseURL := agent.GetOllamaBaseURL()
	log.Printf("[ollama] callDirect model=%s base_url=%s prompt_len=%d attachments=%d", agent.Model, baseURL, len(prompt), len(attachments))

	opts := &options{}
	if agent.Temperature > 0 {
		opts.Temperature = agent.Temperature
	}
	if agent.MaxTokens > 0 {
		opts.NumPredict = agent.MaxTokens
	}

	userMsg := chatMessage{Role: "user", Content: prompt}
	if images := encodeImageAttachments(attachments); len(images) > 0 {
		userMsg.Images = images
	}

	reqBody := chatRequest{
		Model:    agent.Model,
		Messages: []chatMessage{userMsg},
		Stream:   false,
		Options:  opts,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return "", 0, fmt.Errorf("marshaling ollama request: %w", err)
	}

	url := strings.TrimRight(baseURL, "/") + "/api/chat"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("creating ollama request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, fmt.Errorf("reading ollama response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return "", 0, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return "", 0, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, string(respBody))
	}

	var chatResp chatResponse
	if err := json.Unmarshal(respBody, &chatResp); err != nil {
		return "", 0, fmt.Errorf("parsing ollama response: %w", err)
	}

	output := chatResp.Message.Content
	tokens := chatResp.EvalCount
	log.Printf("[ollama] callDirect success model=%s eval_tokens=%d prompt_tokens=%d output_len=%d",
		agent.Model, tokens, chatResp.PromptEvalCount, len(output))
	return output, tokens, nil
}

// callChat calls the Ollama API with chat history for interactive chat.
func (a *Adapter) callChat(ctx context.Context, message string, attachments []models.Attachment, agent models.LLMConfig, execID string, chatHistory []models.Execution, chatSystemContext string, isTaskFollowup bool, chatMode models.ChatMode) (string, int, error) {
	baseURL := agent.GetOllamaBaseURL()
	log.Printf("[ollama] callChat model=%s base_url=%s history=%d message_len=%d attachments=%d exec=%s isTaskFollowup=%v",
		agent.Model, baseURL, len(chatHistory), len(message), len(attachments), execID, isTaskFollowup)

	systemPromptStr := llmprompt.BuildChatSystemPrompt(isTaskFollowup, chatMode, chatSystemContext, false)
	messages := buildChatHistory(systemPromptStr, chatHistory)

	userMsg := chatMessage{Role: "user", Content: message}
	if images := encodeImageAttachments(attachments); len(images) > 0 {
		userMsg.Images = images
	}
	messages = append(messages, userMsg)

	opts := &options{}
	if agent.Temperature > 0 {
		opts.Temperature = agent.Temperature
	}
	if agent.MaxTokens > 0 {
		opts.NumPredict = agent.MaxTokens
	}

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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", 0, fmt.Errorf("creating ollama chat request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return "", 0, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return "", 0, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return "", 0, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, string(respBody))
	}

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	decoder := json.NewDecoder(resp.Body)
	totalTokens := 0
	promptTokens := 0

	for {
		var chunk chatStreamChunk
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			if ctx.Err() != nil {
				sw.Flush()
				return "", 0, fmt.Errorf("ollama streaming cancelled: %w", ctx.Err())
			}
			sw.Flush()
			return "", 0, fmt.Errorf("decoding ollama stream chunk: %w", err)
		}

		if chunk.Message.Content != "" {
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: chunk.Message.Content}, true)
		}

		if chunk.Done {
			totalTokens = chunk.EvalCount
			promptTokens = chunk.PromptEvalCount
			break
		}
	}

	sw.Flush()
	output := sw.String()
	log.Printf("[ollama] callChat success model=%s eval_tokens=%d prompt_tokens=%d output_len=%d",
		agent.Model, totalTokens, promptTokens, len(output))
	return output, totalTokens, nil
}

// callStreaming calls Ollama with streaming for task execution.
func (a *Adapter) callStreaming(ctx context.Context, prompt string, attachments []models.Attachment, agent models.LLMConfig, execID string, projectInstructions string) (string, string, int, error) {
	baseURL := agent.GetOllamaBaseURL()
	log.Printf("[ollama] callStreaming model=%s base_url=%s prompt_len=%d attachments=%d exec=%s", agent.Model, baseURL, len(prompt), len(attachments), execID)

	opts := &options{}
	if agent.Temperature > 0 {
		opts.Temperature = agent.Temperature
	}
	if agent.MaxTokens > 0 {
		opts.NumPredict = agent.MaxTokens
	}

	var messages []chatMessage
	// Inject system prompt with project instructions for task execution
	systemPrompt := llmprompt.BuildAgentSystemPrompt(projectInstructions)
	messages = append(messages, chatMessage{Role: "system", Content: systemPrompt})

	userMsg := chatMessage{Role: "user", Content: prompt}
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
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", "", 0, fmt.Errorf("creating ollama streaming request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(httpReq)
	if err != nil {
		return "", "", 0, fmt.Errorf("ollama API call failed (is Ollama running at %s?): %w", baseURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		var errResp errorResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != "" {
			return "", "", 0, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, errResp.Error)
		}
		return "", "", 0, fmt.Errorf("ollama API error (%d): %s", resp.StatusCode, string(respBody))
	}

	sw := llmstream.NewWriter(execID, "", a.execRepo, ctx, 500*time.Millisecond)
	defer sw.Stop()

	decoder := json.NewDecoder(resp.Body)
	totalTokens := 0
	promptTokens := 0
	var thinkingBuf strings.Builder
	var textBuf strings.Builder

	for {
		var chunk chatStreamChunk
		if err := decoder.Decode(&chunk); err != nil {
			if err == io.EOF {
				break
			}
			if ctx.Err() != nil {
				sw.Flush()
				return "", "", 0, fmt.Errorf("ollama streaming cancelled: %w", ctx.Err())
			}
			sw.Flush()
			return "", "", 0, fmt.Errorf("decoding ollama stream chunk: %w", err)
		}

		if chunk.Message.Thinking != "" {
			thinkingBuf.WriteString(chunk.Message.Thinking)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventThinkingText, Text: chunk.Message.Thinking}, false)
		}

		if chunk.Message.Content != "" {
			textBuf.WriteString(chunk.Message.Content)
			llmstream.WriteEvent(sw, llmstream.Event{Type: llmstream.EventTextDelta, Text: chunk.Message.Content}, false)
		}

		if chunk.Done {
			totalTokens = chunk.EvalCount
			promptTokens = chunk.PromptEvalCount
			break
		}
	}

	sw.Flush()
	output := sw.String()
	textOutput := textBuf.String()
	if thinkingBuf.Len() == 0 {
		textOutput = output
	}
	log.Printf("[ollama] callStreaming success model=%s eval_tokens=%d prompt_tokens=%d output_len=%d",
		agent.Model, totalTokens, promptTokens, len(output))
	return output, textOutput, totalTokens, nil
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
		if exec.Output != "" {
			messages = append(messages, chatMessage{
				Role:    "assistant",
				Content: llmoutput.CleanChatOutput(exec.Output),
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
			log.Printf("[ollama] error reading image %s: %v", filePath, err)
			continue
		}
		images = append(images, base64.StdEncoding.EncodeToString(data))
		log.Printf("[ollama] added image attachment %s (%s, %d bytes)", att.FileName, att.MediaType, len(data))
	}
	return images
}
