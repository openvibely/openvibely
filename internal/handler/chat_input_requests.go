package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/repository"
)

const chatInputRequestTTL = 10 * time.Minute

type chatInputRequestOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type chatInputRequestQuestion struct {
	ID       string                   `json:"id"`
	Question string                   `json:"question"`
	Options  []chatInputRequestOption `json:"options"`
}

type requestUserInputToolInput struct {
	Questions []chatInputRequestQuestion `json:"questions"`
}

type chatInputRequestAnswer struct {
	QuestionID string `json:"question_id"`
	Label      string `json:"label"`
}

type chatInputRequestAnswerPayload struct {
	ProjectID string                   `json:"project_id"`
	Answers   []chatInputRequestAnswer `json:"answers"`
}

type chatInputRequestBroker struct {
	mu       sync.Mutex
	requests map[string]*chatInputRequest
	now      func() time.Time
}

type chatInputRequest struct {
	ID        string
	ProjectID string
	ExecID    string
	Questions []chatInputRequestQuestion
	ExpiresAt time.Time
	answerCh  chan []chatInputRequestAnswer
	resolved  bool
}

func newChatInputRequestBroker() *chatInputRequestBroker {
	return &chatInputRequestBroker{requests: map[string]*chatInputRequest{}, now: time.Now}
}

func (b *chatInputRequestBroker) create(projectID, execID string, questions []chatInputRequestQuestion) (*chatInputRequest, error) {
	if b == nil {
		return nil, fmt.Errorf("input request broker unavailable")
	}
	id := repository.NewID()
	if id == "" {
		return nil, fmt.Errorf("failed to generate input request id")
	}
	req := &chatInputRequest{
		ID:        id,
		ProjectID: strings.TrimSpace(projectID),
		ExecID:    strings.TrimSpace(execID),
		Questions: cloneChatInputQuestions(questions),
		ExpiresAt: b.now().Add(chatInputRequestTTL),
		answerCh:  make(chan []chatInputRequestAnswer, 1),
	}
	b.mu.Lock()
	b.cleanupLocked(b.now())
	b.requests[id] = req
	b.mu.Unlock()
	return req, nil
}

func (b *chatInputRequestBroker) wait(ctx context.Context, id string) ([]chatInputRequestAnswer, error) {
	if b == nil {
		return nil, fmt.Errorf("input request broker unavailable")
	}
	b.mu.Lock()
	req := b.requests[id]
	b.mu.Unlock()
	if req == nil {
		return nil, fmt.Errorf("input request missing")
	}
	select {
	case answers := <-req.answerCh:
		return answers, nil
	case <-time.After(time.Until(req.ExpiresAt)):
		b.expire(id)
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		b.expire(id)
		return nil, ctx.Err()
	}
}

func (b *chatInputRequestBroker) resolve(id, projectID string, answers []chatInputRequestAnswer) error {
	if b == nil {
		return echo.NewHTTPError(http.StatusGone, "input request broker unavailable")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked(b.now())
	req := b.requests[strings.TrimSpace(id)]
	if req == nil {
		return echo.NewHTTPError(http.StatusGone, "input request is missing, expired, or already resolved")
	}
	if req.resolved {
		return echo.NewHTTPError(http.StatusConflict, "input request already resolved")
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(projectID) != req.ProjectID {
		return echo.NewHTTPError(http.StatusForbidden, "input request does not belong to this project")
	}
	if err := validateChatInputAnswers(req.Questions, answers); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	req.resolved = true
	delete(b.requests, req.ID)
	req.answerCh <- cloneChatInputAnswers(answers)
	close(req.answerCh)
	return nil
}

func (b *chatInputRequestBroker) expire(id string) {
	if b == nil {
		return
	}
	b.mu.Lock()
	delete(b.requests, strings.TrimSpace(id))
	b.cleanupLocked(b.now())
	b.mu.Unlock()
}

func (b *chatInputRequestBroker) cleanupLocked(now time.Time) {
	for id, req := range b.requests {
		if req == nil || req.resolved || !now.Before(req.ExpiresAt) {
			delete(b.requests, id)
		}
	}
}

func (h *Handler) executeRequestUserInputTool(ctx context.Context, params streamingResponseParams, input json.RawMessage) (string, error) {
	if params.Surface != "" && params.Surface != chatcontrol.SurfaceWeb {
		return "", fmt.Errorf("request_user_input is available only on web Chat")
	}
	if params.IsTaskFollowup {
		return "", fmt.Errorf("request_user_input is available only on web Chat, not task thread follow-ups")
	}
	projectID := strings.TrimSpace(params.ProjectID)
	if projectID == "" {
		return "", fmt.Errorf("request_user_input: no current project")
	}
	var req requestUserInputToolInput
	if err := chatcontrol.DecodeRuntimeToolInput(input, &req); err != nil {
		return "", err
	}
	if err := validateChatInputQuestions(req.Questions); err != nil {
		return "", err
	}
	if h.chatInputRequests == nil {
		h.chatInputRequests = newChatInputRequestBroker()
	}
	pending, err := h.chatInputRequests.create(projectID, params.ExecID, req.Questions)
	if err != nil {
		return "", err
	}
	if h.chatBroadcaster == nil {
		h.chatInputRequests.expire(pending.ID)
		return "", fmt.Errorf("chat live updates unavailable")
	}
	h.chatBroadcaster.Publish(events.ChatEvent{
		Type:      events.ChatUserInputRequested,
		ProjectID: projectID,
		ExecID:    params.ExecID,
		InputRequest: &events.ChatInputRequestEvent{
			ID:        pending.ID,
			ExpiresAt: pending.ExpiresAt.UTC().Format(time.RFC3339),
			Questions: chatInputQuestionsForEvent(pending.Questions),
		},
	})
	answers, err := h.chatInputRequests.wait(ctx, pending.ID)
	if err != nil {
		return "", err
	}
	out, err := json.Marshal(map[string]any{"ok": true, "request_id": pending.ID, "answers": answers})
	return string(out), err
}

func (h *Handler) ChatInputRequestAnswer(c echo.Context) error {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "input request id is required")
	}
	var payload chatInputRequestAnswerPayload
	if strings.Contains(strings.ToLower(c.Request().Header.Get("Content-Type")), "application/json") {
		if err := json.NewDecoder(c.Request().Body).Decode(&payload); err != nil {
			return echo.NewHTTPError(http.StatusBadRequest, "invalid JSON answer payload")
		}
	} else {
		payload.ProjectID = c.FormValue("project_id")
		payload.Answers = []chatInputRequestAnswer{{QuestionID: c.FormValue("question_id"), Label: c.FormValue("label")}}
	}
	if h.projectRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "project repository unavailable")
	}
	projectID := strings.TrimSpace(payload.ProjectID)
	project, err := h.projectRepo.GetByID(c.Request().Context(), projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return echo.NewHTTPError(http.StatusForbidden, "project not found")
	}
	if err := h.chatInputRequests.resolve(id, projectID, payload.Answers); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

func validateChatInputQuestions(questions []chatInputRequestQuestion) error {
	if len(questions) < 1 || len(questions) > 3 {
		return fmt.Errorf("request_user_input requires 1 to 3 questions")
	}
	seen := map[string]bool{}
	for i, q := range questions {
		qid := strings.TrimSpace(q.ID)
		if qid == "" {
			return fmt.Errorf("question %d id is required", i+1)
		}
		if seen[qid] {
			return fmt.Errorf("duplicate question id %q", qid)
		}
		seen[qid] = true
		if strings.TrimSpace(q.Question) == "" {
			return fmt.Errorf("question %q text is required", qid)
		}
		if len(q.Options) < 2 || len(q.Options) > 3 {
			return fmt.Errorf("question %q requires 2 to 3 options", qid)
		}
		labels := map[string]bool{}
		for _, opt := range q.Options {
			label := strings.TrimSpace(opt.Label)
			if label == "" {
				return fmt.Errorf("question %q option label is required", qid)
			}
			if labels[label] {
				return fmt.Errorf("question %q has duplicate option label %q", qid, label)
			}
			labels[label] = true
			if strings.TrimSpace(opt.Description) == "" {
				return fmt.Errorf("question %q option %q description is required", qid, label)
			}
		}
	}
	return nil
}

func validateChatInputAnswers(questions []chatInputRequestQuestion, answers []chatInputRequestAnswer) error {
	if len(answers) != len(questions) {
		return fmt.Errorf("answer must include exactly one option for each question")
	}
	allowed := map[string]map[string]bool{}
	for _, q := range questions {
		labels := map[string]bool{}
		for _, opt := range q.Options {
			labels[strings.TrimSpace(opt.Label)] = true
		}
		allowed[strings.TrimSpace(q.ID)] = labels
	}
	seen := map[string]bool{}
	for _, ans := range answers {
		qid := strings.TrimSpace(ans.QuestionID)
		label := strings.TrimSpace(ans.Label)
		if qid == "" || label == "" {
			return fmt.Errorf("answer question_id and label are required")
		}
		labels := allowed[qid]
		if labels == nil {
			return fmt.Errorf("unknown question id %q", qid)
		}
		if seen[qid] {
			return fmt.Errorf("duplicate answer for question %q", qid)
		}
		seen[qid] = true
		if !labels[label] {
			return fmt.Errorf("invalid option %q for question %q", label, qid)
		}
	}
	return nil
}

func cloneChatInputQuestions(in []chatInputRequestQuestion) []chatInputRequestQuestion {
	out := make([]chatInputRequestQuestion, len(in))
	for i, q := range in {
		out[i] = chatInputRequestQuestion{ID: strings.TrimSpace(q.ID), Question: strings.TrimSpace(q.Question)}
		out[i].Options = make([]chatInputRequestOption, len(q.Options))
		for j, opt := range q.Options {
			out[i].Options[j] = chatInputRequestOption{Label: strings.TrimSpace(opt.Label), Description: strings.TrimSpace(opt.Description)}
		}
	}
	return out
}

func cloneChatInputAnswers(in []chatInputRequestAnswer) []chatInputRequestAnswer {
	out := make([]chatInputRequestAnswer, len(in))
	for i, ans := range in {
		out[i] = chatInputRequestAnswer{QuestionID: strings.TrimSpace(ans.QuestionID), Label: strings.TrimSpace(ans.Label)}
	}
	return out
}

func chatInputQuestionsForEvent(in []chatInputRequestQuestion) []events.ChatInputRequestQuestion {
	out := make([]events.ChatInputRequestQuestion, len(in))
	for i, q := range in {
		out[i] = events.ChatInputRequestQuestion{ID: q.ID, Question: q.Question}
		out[i].Options = make([]events.ChatInputRequestOption, len(q.Options))
		for j, opt := range q.Options {
			out[i].Options[j] = events.ChatInputRequestOption{Label: opt.Label, Description: opt.Description}
		}
	}
	return out
}
