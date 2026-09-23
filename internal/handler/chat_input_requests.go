package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/chatcontrol"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
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
	QuestionID   string `json:"question_id"`
	Label        string `json:"label,omitempty"`
	CustomAnswer string `json:"custom_answer,omitempty"`
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
	Answers   []chatInputRequestAnswer
	CreatedAt time.Time
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
	now := b.now()
	req := &chatInputRequest{
		ID:        id,
		ProjectID: strings.TrimSpace(projectID),
		ExecID:    strings.TrimSpace(execID),
		Questions: cloneChatInputQuestions(questions),
		CreatedAt: now,
		ExpiresAt: now.Add(chatInputRequestTTL),
		answerCh:  make(chan []chatInputRequestAnswer, 1),
	}
	b.mu.Lock()
	b.cleanupLocked(now)
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

func (b *chatInputRequestBroker) resolve(id, projectID string, answers []chatInputRequestAnswer) (*chatInputRequest, error) {
	if b == nil {
		return nil, echo.NewHTTPError(http.StatusGone, "input request broker unavailable")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked(b.now())
	req := b.requests[strings.TrimSpace(id)]
	if req == nil {
		return nil, echo.NewHTTPError(http.StatusGone, "input request is missing, expired, or already resolved")
	}
	if req.resolved {
		return nil, echo.NewHTTPError(http.StatusConflict, "input request already resolved")
	}
	if strings.TrimSpace(projectID) == "" || strings.TrimSpace(projectID) != req.ProjectID {
		return nil, echo.NewHTTPError(http.StatusForbidden, "input request does not belong to this project")
	}
	if err := validateChatInputAnswers(req.Questions, answers); err != nil {
		return nil, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	req.resolved = true
	req.Answers = cloneChatInputAnswers(answers)
	req.answerCh <- cloneChatInputAnswers(req.Answers)
	close(req.answerCh)
	return cloneChatInputRequest(req), nil
}

func (b *chatInputRequestBroker) listProject(projectID string) []*chatInputRequest {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.cleanupLocked(b.now())
	requests := make([]*chatInputRequest, 0)
	for _, req := range b.requests {
		if req != nil && req.ProjectID == strings.TrimSpace(projectID) {
			requests = append(requests, cloneChatInputRequest(req))
		}
	}
	sort.Slice(requests, func(i, j int) bool { return requests[i].CreatedAt.Before(requests[j].CreatedAt) })
	return requests
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
		if req == nil || !now.Before(req.ExpiresAt) {
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
	if err := h.persistChatInputRequest(ctx, pending); err != nil {
		h.chatInputRequests.expire(pending.ID)
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
			ExecID:    pending.ExecID,
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

func (h *Handler) requireChatInputProject(ctx context.Context, projectID string) error {
	if h.projectRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "project repository unavailable")
	}
	project, err := h.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return err
	}
	if project == nil {
		return echo.NewHTTPError(http.StatusForbidden, "project not found")
	}
	return nil
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
		payload.Answers = []chatInputRequestAnswer{{QuestionID: c.FormValue("question_id"), Label: c.FormValue("label"), CustomAnswer: c.FormValue("custom_answer")}}
	}
	projectID := strings.TrimSpace(payload.ProjectID)
	if err := h.requireChatInputProject(c.Request().Context(), projectID); err != nil {
		return err
	}
	resolved, err := h.chatInputRequests.resolve(id, projectID, payload.Answers)
	recovered := false
	if err != nil {
		if httpErr, ok := err.(*echo.HTTPError); !ok || httpErr.Code != http.StatusGone {
			return err
		}
		var recoveredErr error
		resolved, recovered, recoveredErr = h.resolvePersistedChatInputRequest(c.Request().Context(), id, projectID, payload.Answers)
		if recoveredErr != nil {
			return recoveredErr
		}
	} else if err := h.persistChatInputRequestResolution(c.Request().Context(), id, projectID, resolved.Answers); err != nil {
		return err
	}
	if recovered {
		h.completeRecoveredChatInputExecution(c.Request().Context(), resolved)
	}
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{
			Type:         events.ChatUserInputResolved,
			ProjectID:    projectID,
			ExecID:       resolved.ExecID,
			InputRequest: chatInputRequestForEvent(resolved),
		})
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) ChatInputRequests(c echo.Context) error {
	projectID := strings.TrimSpace(c.QueryParam("project_id"))
	if projectID == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "project_id is required")
	}
	if err := h.requireChatInputProject(c.Request().Context(), projectID); err != nil {
		return err
	}
	requests, err := h.listChatInputRequests(c.Request().Context(), projectID)
	if err != nil {
		return err
	}
	items := make([]*events.ChatInputRequestEvent, 0, len(requests))
	for _, req := range requests {
		items = append(items, chatInputRequestForEvent(req))
	}
	return c.JSON(http.StatusOK, map[string]any{"input_requests": items})
}

func (h *Handler) persistChatInputRequest(ctx context.Context, req *chatInputRequest) error {
	if h == nil || h.chatInputRequestRepo == nil || req == nil {
		return nil
	}
	questionsJSON, err := json.Marshal(req.Questions)
	if err != nil {
		return fmt.Errorf("encoding chat input request questions: %w", err)
	}
	return h.chatInputRequestRepo.Create(ctx, repository.ChatInputRequestRecord{
		ID:            req.ID,
		ProjectID:     req.ProjectID,
		ExecutionID:   req.ExecID,
		QuestionsJSON: string(questionsJSON),
		ExpiresAt:     req.ExpiresAt,
	})
}

func (h *Handler) persistChatInputRequestResolution(ctx context.Context, id, projectID string, answers []chatInputRequestAnswer) error {
	if h == nil || h.chatInputRequestRepo == nil {
		return nil
	}
	answersJSON, err := json.Marshal(cloneChatInputAnswers(answers))
	if err != nil {
		return fmt.Errorf("encoding chat input request answers: %w", err)
	}
	_, err = h.chatInputRequestRepo.Resolve(ctx, id, projectID, string(answersJSON))
	return err
}

func (h *Handler) listChatInputRequests(ctx context.Context, projectID string) ([]*chatInputRequest, error) {
	live := h.chatInputRequests.listProject(projectID)
	byID := make(map[string]*chatInputRequest, len(live))
	for _, req := range live {
		byID[req.ID] = req
	}
	if h == nil || h.chatInputRequestRepo == nil {
		return live, nil
	}
	records, err := h.chatInputRequestRepo.ListActiveByProject(ctx, projectID, time.Now())
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		if _, exists := byID[record.ID]; exists {
			continue
		}
		req, err := chatInputRequestFromRecord(record)
		if err != nil {
			return nil, err
		}
		byID[req.ID] = req
		live = append(live, req)
	}
	sort.Slice(live, func(i, j int) bool { return live[i].CreatedAt.Before(live[j].CreatedAt) })
	return live, nil
}

func (h *Handler) resolvePersistedChatInputRequest(ctx context.Context, id, projectID string, answers []chatInputRequestAnswer) (*chatInputRequest, bool, error) {
	if h == nil || h.chatInputRequestRepo == nil {
		return nil, false, echo.NewHTTPError(http.StatusGone, "input request is missing, expired, or already resolved")
	}
	record, err := h.chatInputRequestRepo.Get(ctx, id)
	if err != nil {
		return nil, false, err
	}
	if record == nil || record.ProjectID != projectID || record.Status != repository.ChatInputRequestPending || !time.Now().Before(record.ExpiresAt) {
		return nil, false, echo.NewHTTPError(http.StatusGone, "input request is missing, expired, or already resolved")
	}
	req, err := chatInputRequestFromRecord(*record)
	if err != nil {
		return nil, false, err
	}
	if err := validateChatInputAnswers(req.Questions, answers); err != nil {
		return nil, false, echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	req.resolved = true
	req.Answers = cloneChatInputAnswers(answers)
	if err := h.persistChatInputRequestResolution(ctx, id, projectID, req.Answers); err != nil {
		return nil, false, err
	}
	return req, true, nil
}

func (h *Handler) completeRecoveredChatInputExecution(ctx context.Context, req *chatInputRequest) {
	if h == nil || h.execRepo == nil || req == nil || strings.TrimSpace(req.ExecID) == "" {
		return
	}
	exec, err := h.execRepo.GetByID(ctx, req.ExecID)
	if err != nil || exec == nil || (exec.Status != models.ExecRunning && exec.Status != models.ExecQueued) {
		return
	}
	_ = h.execRepo.Complete(ctx, req.ExecID, models.ExecFailed, "", "The app restarted while waiting for this input request, so the original response cannot resume. Send a new Chat message to continue.", 0, 0)
	h.PromoteQueuedChatInput(req.ProjectID)
}

func chatInputRequestFromRecord(record repository.ChatInputRequestRecord) (*chatInputRequest, error) {
	var questions []chatInputRequestQuestion
	if err := json.Unmarshal([]byte(record.QuestionsJSON), &questions); err != nil {
		return nil, fmt.Errorf("decoding chat input request questions: %w", err)
	}
	var answers []chatInputRequestAnswer
	if strings.TrimSpace(record.AnswersJSON) != "" {
		if err := json.Unmarshal([]byte(record.AnswersJSON), &answers); err != nil {
			return nil, fmt.Errorf("decoding chat input request answers: %w", err)
		}
	}
	return &chatInputRequest{
		ID:        record.ID,
		ProjectID: record.ProjectID,
		ExecID:    record.ExecutionID,
		Questions: cloneChatInputQuestions(questions),
		Answers:   cloneChatInputAnswers(answers),
		CreatedAt: record.CreatedAt,
		ExpiresAt: record.ExpiresAt,
		answerCh:  make(chan []chatInputRequestAnswer, 1),
		resolved:  record.Status == repository.ChatInputRequestResolved,
	}, nil
}

func chatInputRequestForEvent(req *chatInputRequest) *events.ChatInputRequestEvent {
	if req == nil {
		return nil
	}
	answers := make([]events.ChatInputRequestAnswer, 0, len(req.Answers))
	for _, answer := range req.Answers {
		answers = append(answers, events.ChatInputRequestAnswer{QuestionID: answer.QuestionID, Label: answer.Label})
	}
	return &events.ChatInputRequestEvent{
		ID: req.ID, ExecID: req.ExecID, ExpiresAt: req.ExpiresAt.UTC().Format(time.RFC3339),
		Questions: chatInputQuestionsForEvent(req.Questions), Answers: answers, Completed: req.resolved,
	}
}

func cloneChatInputRequest(req *chatInputRequest) *chatInputRequest {
	if req == nil {
		return nil
	}
	clone := *req
	clone.Questions = cloneChatInputQuestions(req.Questions)
	clone.Answers = cloneChatInputAnswers(req.Answers)
	return &clone
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
		customAnswer := strings.TrimSpace(ans.CustomAnswer)
		if qid == "" || (label == "" && customAnswer == "") {
			return fmt.Errorf("answer question_id and either label or custom_answer are required")
		}
		if label != "" && customAnswer != "" {
			return fmt.Errorf("answer for question %q must use either label or custom_answer, not both", qid)
		}
		if len(customAnswer) > 2000 {
			return fmt.Errorf("custom answer for question %q is too long", qid)
		}
		labels := allowed[qid]
		if labels == nil {
			return fmt.Errorf("unknown question id %q", qid)
		}
		if seen[qid] {
			return fmt.Errorf("duplicate answer for question %q", qid)
		}
		seen[qid] = true
		if customAnswer == "" && !labels[label] {
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
		label := strings.TrimSpace(ans.Label)
		if customAnswer := strings.TrimSpace(ans.CustomAnswer); customAnswer != "" {
			label = customAnswer
		}
		out[i] = chatInputRequestAnswer{QuestionID: strings.TrimSpace(ans.QuestionID), Label: label}
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
