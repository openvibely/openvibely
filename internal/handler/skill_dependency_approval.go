package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/events"
	contracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/skilldeps"
	"github.com/openvibely/openvibely/internal/update"
)

func (h *Handler) requireDependencyHuman(c echo.Context) error {
	if h.authEnabled() {
		session := h.recognizeSession(c.Request(), time.Now())
		if session.localUser == nil && session.hostedClaims == nil {
			return echo.NewHTTPError(http.StatusUnauthorized, "Sign in to answer installation questions")
		}
	}
	r := c.Request()
	originText := r.Header.Get("Origin")
	origin, err := url.Parse(originText)
	sameOrigin := err == nil && origin.Host == r.Host && (origin.Scheme == "http" || origin.Scheme == "https")
	// Wails uses a custom origin on macOS/Linux, sometimes serialized as null
	// or omitted. Match the existing native asset proxy convention, only in desktop mode.
	nativeOrigin := h.desktopMode && r.Header.Get("X-OpenVibely-Native-Window") == "1" && r.Host == "localhost" && (originText == "wails://localhost" || originText == "null" || originText == "")
	if (!sameOrigin && !nativeOrigin) || r.Header.Get("X-OpenVibely-UI") != "chat-input-answer" {
		return echo.NewHTTPError(http.StatusForbidden, "same-origin UI decision required")
	}
	return nil
}

func (h *Handler) requestSkillInstallation(ctx context.Context, req contracts.AgentRequest, manifest skilldeps.InstallationRequest) error {
	if h.chatInputRequestRepo == nil || h.chatInputRequests == nil {
		return errors.New("question broker unavailable")
	}
	execution, err := h.execRepo.GetByID(ctx, req.ExecID)
	if err != nil {
		return err
	}
	if execution == nil || execution.Status != models.ExecRunning {
		return errors.New("execution is no longer active")
	}
	task, err := h.taskRepo.GetByID(ctx, execution.TaskID)
	if err != nil {
		return err
	}
	if task == nil || task.Status == models.StatusCancelled || (req.ProjectID != "" && req.ProjectID != task.ProjectID) {
		return errors.New("task no longer available")
	}
	manifest.Identity.ProjectID = task.ProjectID
	raw, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	agentID := ""
	if req.AgentDefinition != nil {
		agentID = req.AgentDefinition.ID
	}
	names := make([]string, 0, len(manifest.Dependencies))
	for _, dependency := range manifest.Dependencies {
		name := dependency.Name
		if name != "" {
			name = strings.ToUpper(name[:1]) + name[1:]
		}
		names = append(names, name)
	}
	tools := strings.Join(names, ", ")
	if tools == "" {
		tools = "tools"
	}
	questions := []chatInputRequestQuestion{{ID: "install", Question: fmt.Sprintf("Install %s to use the %s skill?", tools, strings.ReplaceAll(manifest.Identity.Skill, "_", " ")), Options: []chatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}
	pending, err := h.chatInputRequests.create(task.ProjectID, req.ExecID, questions, &persistence.ChatInstallationRequest{ToolCallID: contracts.ToolCallIDFromContext(ctx), ToolArguments: skilldeps.SkillViewArguments(ctx), Manifest: raw, AgentID: agentID, ChatMode: string(req.ChatMode)})
	if err != nil {
		return err
	}
	if err = h.persistChatInputRequest(ctx, pending); err != nil {
		h.chatInputRequests.expire(pending.ID)
		return err
	}
	if h.chatBroadcaster != nil && task.Category == models.CategoryChat {
		h.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatUserInputRequested, ProjectID: task.ProjectID, ExecID: req.ExecID, InputRequest: chatInputRequestForEvent(pending)})
	}
	if h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{Type: events.SkillDependencyApproval, ProjectID: task.ProjectID, TaskID: task.ID, ExecID: req.ExecID, PendingInputID: pending.ID, Status: "pending", Message: "Install missing skill dependencies?"})
	}
	if h.alertSvc != nil {
		_ = h.alertSvc.Create(ctx, &models.Alert{ProjectID: task.ProjectID, TaskID: &task.ID, ExecutionID: &req.ExecID, Title: "Skill needs dependency installation", Message: "Answer the question in the requesting thread.", Type: models.AlertCustom, Source: "skill_dependency_approval", IdempotencyKey: "skill-dependency:" + pending.ID, DecisionState: models.AlertDecisionNotRequired})
	}
	_, waitErr := h.chatInputRequests.wait(ctx, pending.ID)
	if waitErr != nil || ctx.Err() != nil {
		return fmt.Errorf("%s%s", contracts.PendingSkillInputPrefix, pending.ID)
	}
	answer, err := h.chatInputRequestRepo.ClaimInstallationAnswer(ctx, pending.ID, task.ProjectID, false)
	if err != nil {
		return err
	}
	if answer == nil {
		return errors.New("installation answer already handled")
	}
	// The tool's initial permission check preceded the human wait. Read current
	// project permissions again before allowing dependency preparation to resume.
	const unavailable = "skill_view could not read this skill because access is no longer available. Continue without this skill."
	if h.projectRepo == nil {
		return errors.New(unavailable)
	}
	project, err := h.projectRepo.GetByID(ctx, task.ProjectID)
	if err != nil || project == nil || !project.ManagedPermissions().ReadSkills {
		return errors.New(unavailable)
	}
	if agentID != "" {
		if h.agentRepo == nil {
			return errors.New(unavailable)
		}
		fresh, e := h.agentRepo.GetByID(ctx, agentID)
		if e != nil || fresh == nil || !fresh.Enabled || fresh.ProjectID != req.AgentDefinition.ProjectID || !reflect.DeepEqual(fresh.PermissionDefaults, req.AgentDefinition.PermissionDefaults) || !reflect.DeepEqual(fresh.Tools, req.AgentDefinition.Tools) || !reflect.DeepEqual(fresh.Skills, req.AgentDefinition.Skills) {
			return errors.New(unavailable)
		}
	}
	return installationAnswerResult(answer.AnswersJSON)
}

func installationAnswerResult(raw string) error {
	var answers []chatInputRequestAnswer
	if json.Unmarshal([]byte(raw), &answers) != nil || len(answers) != 1 || answers[0].QuestionID != "install" || answers[0].CustomAnswer != "" {
		return errors.New("invalid installation answer")
	}
	if answers[0].Label != "Install dependencies" {
		return errors.New("human declined dependency installation; choose another approach")
	}
	return nil
}

func (h *Handler) answerSkillInstallation(c echo.Context, record *persistence.ChatInputRequestRecord, payload chatInputRequestAnswerPayload) error {
	if err := h.requireDependencyHuman(c); err != nil {
		return err
	}
	if record.ProjectID != payload.ProjectID {
		return echo.NewHTTPError(http.StatusForbidden, "question belongs to another project")
	}
	req, err := chatInputRequestFromRecord(*record)
	if err != nil {
		return err
	}
	if len(payload.Answers) != 1 || payload.Answers[0].CustomAnswer != "" {
		return echo.NewHTTPError(http.StatusBadRequest, "select an installation option")
	}
	if err = validateChatInputAnswers(req.Questions, payload.Answers); err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	raw, err := json.Marshal(payload.Answers)
	if err != nil {
		return err
	}
	record, err = h.chatInputRequestRepo.Resolve(c.Request().Context(), record.ID, record.ProjectID, string(raw))
	if err != nil {
		return err
	}
	req, err = chatInputRequestFromRecord(*record)
	if err != nil {
		return err
	}
	_, err = h.chatInputRequests.resolve(req.ID, req.ProjectID, req.Answers, true)
	if err != nil {
		httpErr, ok := err.(*echo.HTTPError)
		if !ok || (httpErr.Code != http.StatusGone && httpErr.Code != http.StatusConflict) {
			return err
		}

	}
	if h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatUserInputResolved, ProjectID: req.ProjectID, ExecID: req.ExecID, InputRequest: chatInputRequestForEvent(req)})
	}
	execution, e := h.execRepo.GetByID(c.Request().Context(), req.ExecID)
	if e == nil && execution != nil {
		if task, e := h.taskRepo.GetByID(c.Request().Context(), execution.TaskID); e == nil && task != nil {
			if h.broadcaster != nil {
				h.broadcaster.Publish(events.TaskEvent{Type: events.SkillDependencyApproval, ProjectID: task.ProjectID, TaskID: task.ID, ExecID: req.ExecID, PendingInputID: req.ID, Status: "resolved"})
			}
			if task.Category == models.CategoryChat {
				// Reuse Chat's recovered-question handling only for a question
				// created before this broker. An expired wait in this process
				// must not terminate a model loop that is still running.
				if req.ExpiresAt.Add(-chatInputRequestTTL).Before(h.chatInputRequests.startedAt) {
					h.completeRecoveredChatInputExecution(c.Request().Context(), req)
				}
				h.startInstallationContinuations(context.WithoutCancel(c.Request().Context()), task.ProjectID, "")
			} else {
				h.startInstallationContinuations(context.WithoutCancel(c.Request().Context()), task.ProjectID, task.ID)
			}
		}
	}
	return c.JSON(http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) OpenChatInputRequest(c echo.Context) error {
	record, err := h.chatInputRequestRepo.Get(c.Request().Context(), c.Param("id"))
	if err != nil || record == nil || record.ProjectID != h.mutationProjectID(c) {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	execution, err := h.execRepo.GetByID(c.Request().Context(), record.ExecutionID)
	if err != nil || execution == nil {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	task, err := h.taskRepo.GetByID(c.Request().Context(), execution.TaskID)
	if err != nil || task == nil || task.ProjectID != record.ProjectID {
		return echo.NewHTTPError(http.StatusNotFound)
	}
	destination := "/tasks/" + url.PathEscape(task.ID) + "?tab=history&project_id=" + url.QueryEscape(record.ProjectID)
	if task.Category == models.CategoryChat {
		destination = "/chat?project_id=" + url.QueryEscape(record.ProjectID)
	}
	destination += "&approval_execution_id=" + url.QueryEscape(record.ExecutionID) + "#chat-input-request-" + url.PathEscape(record.ID)
	return c.Redirect(http.StatusSeeOther, destination)
}

func (h *Handler) PreviewSkillDependencies(c echo.Context) error {
	raw, err := io.ReadAll(http.MaxBytesReader(c.Response(), c.Request().Body, 1<<20))
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "skill preview exceeds size limit")
	}
	deps, err := skilldeps.Parse(raw)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	return c.JSON(http.StatusOK, map[string]any{"dependencies": deps, "platform": runtime.GOOS + "/" + runtime.GOARCH})
}

// Complete the original read before the continuation's first model request.
// The result is attached to the original tool call, never a user message.
func (h *Handler) completeSkillInstallationContinuation(ctx context.Context, req contracts.AgentRequest) (string, error) {
	agentID := ""
	if req.AgentDefinition != nil {
		agentID = req.AgentDefinition.ID
	}
	record, err := h.chatInputRequestRepo.LoadInstallationContinuation(ctx, req.ProjectID, req.ExecID, agentID)
	if err != nil || record == nil {
		return "", err
	}
	_, meta, err := record.DecodeQuestions()
	if err != nil {
		return "", err
	}
	if meta.Handled {
		if !strings.HasPrefix(meta.Result, contracts.SkillInputResultPrefix) {
			return "", errors.New("saved installation tool result unavailable")
		}
		return meta.Result, nil
	}

	output := ""
	isError := false
	if denied := installationAnswerResult(record.AnswersJSON); denied != nil {
		output = denied.Error()
		isError = true
	} else {
		tools := contracts.RuntimeToolsFromContext(ctx)
		if !json.Valid(meta.ToolArguments) {
			return "", errors.New("original skill_view arguments missing")
		}
		if tools == nil || tools.Executor == nil || !tools.HasDefinition("skill_view") {
			output = "skill_view is no longer permitted"
			isError = true
		} else {
			var approved skilldeps.InstallationRequest
			if err = json.Unmarshal(meta.Manifest, &approved); err != nil {
				return "", err
			}
			toolCtx := skilldeps.WithApproval(ctx, req.ProjectID, func(_ context.Context, current skilldeps.InstallationRequest) error {
				current.Identity.ProjectID = req.ProjectID
				changed := errors.New("skill dependencies changed since the installation question; installation was not performed")
				if approved.Identity != current.Identity || approved.Platform != current.Platform {
					return changed
				}
				// Another run may have installed some of the offered packages.
				// Every package still missing must match an offered dependency exactly.
				for _, missing := range current.Dependencies {
					matched := false
					for _, offered := range approved.Dependencies {
						if reflect.DeepEqual(offered, missing) {
							matched = true
							break
						}
					}
					if !matched {
						return changed
					}
				}
				return nil
			})
			var handled bool
			output, handled, isError, err = tools.Executor(toolCtx, "skill_view", meta.ToolArguments)
			if err != nil {
				output = err.Error()
				isError = true
			}
			if !handled {
				output = "skill_view is no longer available"
				isError = true
			}
		}
	}
	result, err := json.Marshal(struct {
		SourceExecutionID string          `json:"source_execution_id"`
		QuestionID        string          `json:"question_id"`
		ToolCallID        string          `json:"tool_call_id,omitempty"`
		Tool              string          `json:"tool"`
		Arguments         json.RawMessage `json:"arguments,omitempty"`
		Output            string          `json:"output"`
		IsError           bool            `json:"is_error"`
	}{record.ExecutionID, record.ID, meta.ToolCallID, "skill_view", meta.ToolArguments, output, isError})
	if err != nil {
		return "", err
	}
	message := contracts.SkillInputResultPrefix + string(result)
	if err = h.chatInputRequestRepo.SaveInstallationContinuationResult(ctx, record.ID, req.ExecID, message); err != nil {
		return "", err
	}
	return message, nil
}

// Answered installation questions continue their original tool exchange directly.
// They are not user messages and never enter the follow-up input queue.
func (h *Handler) startInstallationContinuations(ctx context.Context, project, taskID string) {
	if h.chatInputRequestRepo == nil || h.execRepo == nil || h.taskRepo == nil {
		return
	}
	records, err := h.chatInputRequestRepo.ListReadyInstallationAnswers(ctx, project, taskID)
	if err != nil {
		applog.Infof("[handler] installation answers: %v", err)
		return
	}
	for _, record := range records {
		if err := h.startInstallationContinuation(ctx, record); err != nil {
			applog.Infof("[handler] installation continuation %s: %v", record.ID, err)
		}
	}
}
func (h *Handler) startInstallationContinuation(ctx context.Context, record persistence.ChatInputRequestRecord) error {
	original, err := h.execRepo.GetByID(ctx, record.ExecutionID)
	if err != nil {
		return err
	}
	if original == nil {
		return errors.New("original execution unavailable")
	}
	task, err := h.taskRepo.GetByID(ctx, original.TaskID)
	if err != nil {
		return err
	}
	if task == nil {
		return errors.New("original task unavailable")
	}
	// Native tool results must continue with the original provider/model.
	agent, err := h.llmConfigRepo.GetByID(ctx, original.AgentConfigID)
	if err != nil {
		return err
	}
	if agent == nil {
		return errors.New("execution model unavailable")
	}
	_, meta, err := record.DecodeQuestions()
	if err != nil {
		return err
	}
	agentDef := h.resolveTaskAgentDefinitionForTask(ctx, task.ID, nil)
	if meta.AgentID != "" && (agentDef == nil || agentDef.ID != meta.AgentID) {
		return errors.New("Agent assignment changed")
	}
	var updateDone func()
	if h.updateWorkTracker != nil {
		class := update.WorkChat
		if task.Category != models.CategoryChat {
			class = update.WorkTask
		}
		updateDone, err = h.updateWorkTracker.Start(class)
		if err != nil {
			return err
		}
		defer func() {
			if updateDone != nil {
				updateDone()
			}
		}()
	}
	// Claim creation serializes against the live answer and other HTTP requests.
	claimed, err := h.chatInputRequestRepo.ClaimInstallationAnswer(ctx, record.ID, record.ProjectID, true)
	if err != nil || claimed == nil {
		return err
	}
	_, meta, err = claimed.DecodeQuestions()
	if err != nil {
		return err
	}
	exec, err := h.execRepo.GetByID(ctx, meta.ContinuationExecutionID)
	if err != nil {
		return err
	}
	if exec == nil {
		return errors.New("continuation execution unavailable")
	}
	fail := func(err error) error { h.completeWithFailure(ctx, exec.ID, task.ID, err.Error(), 0, nil); return err }
	params := streamingResponseParams{
		ExecID: exec.ID, TaskID: task.ID, ProjectID: task.ProjectID,
		Message: original.PromptSent, Agent: *agent, AgentDefinition: agentDef,
		ChatMode:       models.NormalizeChatMode(meta.ChatMode),
		IsTaskFollowup: task.Category != models.CategoryChat, Task: task,
	}
	if params.IsTaskFollowup {
		// Reuse the direct task runner's context preparation after worker admission.
		params.DeferHistoryLoad = true
	} else {
		history, e := h.execRepo.ListChatHistory(ctx, task.ProjectID, chatHistoryLimit)
		if e != nil {
			return fail(e)
		}
		params.ChatHistory = filterChatHistory(history, exec.ID)
		params.WorkDir = h.resolveWorkDir(ctx, task.ProjectID)
		params.SystemContext = h.getPersonalityContext(ctx, task.ProjectID)
	}
	if h.automationGraphSvc != nil {
		value, e := h.automationGraphSvc.ContextForExecution(ctx, task.ProjectID, original.ID)
		if e != nil {
			return fail(e)
		}
		if len(value.Bindings) > 0 {
			params.AutomationContext = &value
		}
	}
	if h.workerSvc != nil {
		h.workerSvc.ClearCancellationRequested(task.ID)
	}
	if params.IsTaskFollowup && h.broadcaster != nil {
		h.broadcaster.Publish(events.TaskEvent{Type: events.TaskThreadExecutionStarted, ProjectID: task.ProjectID, TaskID: task.ID, TaskName: task.Title, ExecID: exec.ID})
	} else if !params.IsTaskFollowup && h.chatBroadcaster != nil {
		h.chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatNewMessage, ProjectID: task.ProjectID, TaskID: task.ID, ExecID: exec.ID, AgentName: agent.Name})
	}
	params.updateWorkDone = updateDone
	if err = h.startStreamingResponse(params); err != nil {
		return fail(err)
	}
	updateDone = nil
	return nil
}
