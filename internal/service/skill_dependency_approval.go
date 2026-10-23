package service

import (
	"context"
	"encoding/json"
	"fmt"
	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"reflect"
	"strings"
)

// SetSkillInstallationPrompt connects the loader to the shared user-input broker.
func (s *LLMService) SetSkillInstallationPrompt(prompt func(context.Context, contracts.AgentRequest, skilldeps.InstallationRequest) error, result ...func(context.Context, contracts.AgentRequest) (string, error)) {
	s.skillInstallationPrompt = prompt
	if len(result) > 0 {
		s.skillInstallationResult = result[0]
	}
}

func (s *LLMService) withSkillDependencyApproval(req contracts.AgentRequest) context.Context {
	if req.ExecID == "" || req.LifecycleHookCall || s.skillInstallationPrompt == nil {
		return req.Ctx
	}
	ctx, cancel := context.WithCancel(req.Ctx)
	return skilldeps.WithApproval(ctx, req.ProjectID, func(ctx context.Context, manifest skilldeps.InstallationRequest) error {
		err := s.skillInstallationPrompt(ctx, req, manifest)
		if err != nil && strings.HasPrefix(err.Error(), contracts.PendingSkillInputPrefix) {
			// End this attempt after the tool boundary. Provider transcript saving
			// uses WithoutCancel; no request is held open for a late answer.
			cancel()
		}
		return err
	})
}

// completeInstallationTranscript completes the original provider exchange. The
// delivery envelope lives in the ordinary queue for admission/retries, but is
// never sent as a user message.
func (s *LLMService) completeInstallationTranscript(req contracts.AgentRequest) (contracts.AgentRequest, error) {
	if !strings.HasPrefix(req.Message, contracts.SkillInputResultPrefix) {
		return req, nil
	}
	var answer struct {
		SourceExecutionID string          `json:"source_execution_id"`
		QuestionID        string          `json:"question_id"`
		ToolCallID        string          `json:"tool_call_id,omitempty"`
		Arguments         json.RawMessage `json:"arguments"`
		Output            string          `json:"output"`
		IsError           bool            `json:"is_error"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(req.Message, contracts.SkillInputResultPrefix)), &answer); err != nil {
		return req, err
	}
	if answer.SourceExecutionID == "" || answer.QuestionID == "" {
		return req, fmt.Errorf("installation result has no original tool exchange")
	}
	source, err := s.execRepo.GetByID(req.Ctx, answer.SourceExecutionID)
	if err != nil {
		return req, err
	}
	current, err := s.execRepo.GetByID(req.Ctx, req.ExecID)
	if err != nil {
		return req, err
	}
	if source == nil || current == nil {
		return req, fmt.Errorf("installation execution is unavailable")
	}
	if source.TaskID != current.TaskID {
		originalTask, e := s.taskRepo.GetByID(req.Ctx, source.TaskID)
		if e != nil {
			return req, e
		}
		currentTask, e := s.taskRepo.GetByID(req.Ctx, current.TaskID)
		if e != nil {
			return req, e
		}
		if originalTask == nil || currentTask == nil || originalTask.Category != models.CategoryChat || currentTask.Category != models.CategoryChat || originalTask.ProjectID != req.ProjectID || currentTask.ProjectID != req.ProjectID {
			return req, fmt.Errorf("installation result belongs to another thread")
		}
	}
	replay, err := s.execRepo.ReplayMessagesByExecutionIDs(req.Ctx, []string{source.ID})
	if err != nil {
		return req, err
	}
	source.ReplayMessages = append([]models.ExecutionReplayMessage(nil), replay[source.ID]...)
	matched := false
	var pendingAnswer error
	for i := range source.ReplayMessages {
		raw, found, e := completeSkillToolTranscript(source.ReplayMessages[i].TranscriptJSON, answer.QuestionID, answer.Arguments, answer.Output, answer.IsError, answer.ToolCallID)
		if e != nil && !found {
			return req, e
		}
		if found {
			pendingAnswer = e
			source.ReplayMessages[i].TranscriptJSON = raw
			matched = true
			break
		}
	}
	if !matched {
		return req, fmt.Errorf("original skill_view tool call is not available in saved history")
	}
	if err := s.execRepo.ReplaceReasoningReplay(context.WithoutCancel(req.Ctx), source.ID, source.ReasoningContent, source.ReplayMessages); err != nil {
		return req, err
	}
	if pendingAnswer != nil {
		return req, pendingAnswer
	}
	// Avoid replaying an interrupted attempt's partial answer after the tool result.
	source.Output, source.ErrorMessage = "", ""
	source.Status = models.ExecCompleted
	history := append([]models.Execution(nil), req.ChatHistory...)
	replacedSource := false
	for i := range history {
		if history[i].ID == source.ID {
			history[i] = *source
			replacedSource = true
			break
		}
	}
	if !replacedSource {
		history = append(history, *source)
	}
	req.ChatHistory = history
	req.Message = ""
	req.Attachments = nil
	req.Ctx = contracts.WithHistoryContinuation(req.Ctx)
	return req, nil
}

// Work on provider-native JSON so reasoning, images and provider-specific fields
// survive unchanged. Only the requested tool result is replaced or inserted;
// other messages, images, and tool exchanges are retained.
func completeSkillToolTranscript(raw, question string, args json.RawMessage, output string, isError bool, callIDs ...string) (string, bool, error) {
	var root any
	if decodeSkillTranscriptJSON(raw, &root) != nil {
		return raw, false, nil
	}
	key := ""
	var items []any
	switch v := root.(type) {
	case []any:
		items = v
	case map[string]any:
		for _, k := range []string{"responses_input", "messages"} {
			if a, ok := v[k].([]any); ok {
				key, items = k, a
				break
			}
		}
	}
	if len(items) == 0 {
		return raw, false, nil
	}
	type call struct {
		id        string
		index     int
		arguments any
	}
	var calls []call
	markerID := ""
	asMap := func(v any) map[string]any { m, _ := v.(map[string]any); return m }
	str := func(v any) string { s, _ := v.(string); return s }
	blocks := func(m map[string]any) []any { a, _ := m["content_blocks"].([]any); return a }
	for i, item := range items {
		m := asMap(item)
		if m["type"] == "function_call" && m["name"] == "skill_view" {
			calls = append(calls, call{str(m["call_id"]), i, m["arguments"]})
		}
		if m["type"] == "function_call_output" && str(m["output"]) == contracts.PendingSkillInputPrefix+question {
			markerID = str(m["call_id"])
		}
		if m["role"] == "tool" && str(m["content"]) == contracts.PendingSkillInputPrefix+question {
			markerID = str(m["tool_call_id"])
		}
		tools, _ := m["tool_calls"].([]any)
		for _, t := range tools {
			t := asMap(t)
			f := asMap(t["function"])
			if f["name"] == "skill_view" {
				calls = append(calls, call{str(t["id"]), i, f["arguments"]})
			}
		}
		for _, b := range blocks(m) {
			b := asMap(b)
			if b["type"] == "tool_use" && b["name"] == "skill_view" {
				calls = append(calls, call{str(b["id"]), i, b["input"]})
			}
			if b["type"] == "tool_result" && str(b["content"]) == contracts.PendingSkillInputPrefix+question {
				markerID = str(b["tool_use_id"])
			}
		}
	}
	// New questions carry an exact provider ID. Marker lookup remains only for
	// questions saved before IDs were recorded.
	if len(callIDs) > 0 && callIDs[0] != "" {
		markerID = callIDs[0]
	}
	var expected any
	if err := decodeSkillTranscriptJSON(string(args), &expected); err != nil {
		return raw, false, err
	}
	var matches []call
	for _, c := range calls {
		actual := c.arguments
		if text, ok := actual.(string); ok {
			if decodeSkillTranscriptJSON(text, &actual) != nil {
				continue
			}
		}
		if reflect.DeepEqual(actual, expected) && (markerID == "" || markerID == c.id) {
			matches = append(matches, c)
		}
	}
	if len(matches) == 0 {
		return raw, false, nil
	}
	if len(matches) != 1 || matches[0].id == "" {
		return raw, false, fmt.Errorf("ambiguous original skill_view tool call")
	}
	target := matches[0]
	insertAt := len(items)
	pending := map[string]bool{}
	replaced := false
	awaitingSibling := false
	batchStart := target.index
	if key == "responses_input" {
		for batchStart > 0 {
			prev := asMap(items[batchStart-1])
			if prev["type"] != "function_call" && prev["type"] != "reasoning" {
				break
			}
			batchStart--
		}
	}
	for i := batchStart; i < len(items); i++ {
		m := asMap(items[i])
		// Chat Completions requires the tool replies before the next user
		// message, including images buffered from another tool in the batch.
		boundary := m["role"] == "assistant" || m["type"] == "reasoning" || (key == "" && m["role"] == "user")
		if insertAt == len(items) && i > target.index && boundary {
			insertAt = i
		}
		if m["type"] == "function_call" {
			pending[str(m["call_id"])] = true
		}
		tools, _ := m["tool_calls"].([]any)
		for _, t := range tools {
			pending[str(asMap(t)["id"])] = true
		}
		for _, b := range blocks(m) {
			b := asMap(b)
			if b["type"] == "tool_use" {
				pending[str(b["id"])] = true
			}
		}
		if m["type"] == "function_call_output" {
			id := str(m["call_id"])
			if id != target.id && strings.HasPrefix(str(m["output"]), contracts.PendingSkillInputPrefix) {
				awaitingSibling = true
			}
			delete(pending, id)
			if id == target.id {
				m["output"] = output
				replaced = true
			}
		}
		if m["role"] == "tool" {
			id := str(m["tool_call_id"])
			if id != target.id && strings.HasPrefix(str(m["content"]), contracts.PendingSkillInputPrefix) {
				awaitingSibling = true
			}
			delete(pending, id)
			if id == target.id {
				m["content"] = output
				replaced = true
			}
		}
		for _, b := range blocks(m) {
			b := asMap(b)
			if b["type"] == "tool_result" {
				id := str(b["tool_use_id"])
				if id != target.id && strings.HasPrefix(str(b["content"]), contracts.PendingSkillInputPrefix) {
					awaitingSibling = true
				}
				delete(pending, id)
				if id == target.id {
					b["content"] = output
					b["is_error"] = isError
					replaced = true
				}
			}
		}
	}
	// Do not guess the outcome of other interrupted side-effecting tool calls.
	delete(pending, target.id)
	if len(pending) > 0 {
		return raw, false, fmt.Errorf("original tool exchange has unfinished sibling calls")
	}
	if !replaced {
		var result any
		switch key {
		case "responses_input":
			result = map[string]any{"type": "function_call_output", "call_id": target.id, "output": output}
		case "messages":
			result = map[string]any{"role": "user", "content_blocks": []any{map[string]any{"type": "tool_result", "tool_use_id": target.id, "content": output, "is_error": isError}}}
		default:
			result = map[string]any{"role": "tool", "tool_call_id": target.id, "content": output}
		}
		items = append(items, nil)
		copy(items[insertAt+1:], items[insertAt:])
		items[insertAt] = result
	}
	if key != "" {
		root.(map[string]any)[key] = items
	} else {
		root = items
	}
	encoded, err := json.Marshal(root)
	if err == nil && awaitingSibling {
		err = fmt.Errorf("another tool call is still awaiting an answer")
	}
	return string(encoded), true, err
}

// Preserve provider-native numeric arguments without float64 rounding.
func decodeSkillTranscriptJSON(raw string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(target)
}
