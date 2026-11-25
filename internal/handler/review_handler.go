package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service/authz"
	"github.com/openvibely/openvibely/web/templates/components"
)

// ListReviewComments returns all review comments for a task as an HTML fragment.
func (h *Handler) ListReviewComments(c echo.Context) error {
	taskID := c.Param("taskId")
	task, lookupErr := h.requireTaskInRequestProject(c.Request().Context(), taskID, h.mutationProjectID(c))
	if lookupErr != nil {
		return lookupErr
	}
	if h.reviewCommentRepo == nil {
		return render(c, http.StatusOK, components.ReviewCommentList(nil, taskID, task.ProjectID))
	}

	comments, err := h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] ListReviewComments error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load review comments")
	}

	return render(c, http.StatusOK, components.ReviewCommentList(comments, taskID, task.ProjectID))
}

// AddReviewComment adds a new inline review comment.
func (h *Handler) AddReviewComment(c echo.Context) error {
	args := addReviewArgs{
		TaskRef:     TaskRef{TaskID: c.Param("taskId"), ProjectID: h.mutationProjectID(c)},
		FilePath:    c.FormValue("file_path"),
		LineNumber:  c.FormValue("line_number"),
		LineType:    c.FormValue("line_type"),
		CommentText: c.FormValue("comment_text"),
	}
	comments, err := actAddReview(h, c.Request().Context(), args)
	if err != nil {
		return err
	}
	task, err := h.taskSvc.GetByID(c.Request().Context(), args.TaskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	return render(c, http.StatusOK, components.ReviewCommentList(comments, args.TaskID, task.ProjectID))
}

type addReviewArgs struct {
	TaskRef
	FilePath    string `remote:"raw"`
	LineNumber  string
	LineType    string
	CommentText string `remote:"raw"`
}

func (a addReviewArgs) ref() TaskRef { return a.TaskRef }

var actAddReview = defineAction("review.add", ownTaskRef[addReviewArgs], func(h *Handler, ctx context.Context, a addReviewArgs) ([]models.ReviewComment, error) {
	if h.reviewCommentRepo == nil {
		return nil, statusErr(http.StatusInternalServerError, "review comments not configured")
	}
	if a.FilePath == "" || a.LineNumber == "" || a.CommentText == "" {
		return nil, statusErr(http.StatusBadRequest, "file_path, line_number, and comment_text are required")
	}
	lineNum, err := strconv.Atoi(a.LineNumber)
	if err != nil {
		return nil, statusErr(http.StatusBadRequest, "invalid line_number")
	}
	lineType := a.LineType
	if lineType == "" {
		lineType = "new"
	}
	comment := &models.ReviewComment{
		TaskID:      a.TaskID,
		FilePath:    a.FilePath,
		LineNumber:  lineNum,
		LineType:    lineType,
		CommentText: strings.TrimSpace(a.CommentText),
		ReviewedBy:  "user",
	}
	if err := h.reviewCommentRepo.Create(ctx, comment); err != nil {
		applog.Infof("[handler] AddReviewComment error: %v", err)
		return nil, statusErr(http.StatusInternalServerError, "failed to add review comment")
	}
	comments, _ := h.reviewCommentRepo.ListByTask(ctx, a.TaskID)
	return comments, nil
})

type reviewCommentArgs struct {
	ProjectID string
	CommentID string
	Text      string `remote:"raw"`
	Delete    bool
}

var actReviewComment = defineAction("review.comment", func(h *Handler, ctx context.Context, a reviewCommentArgs) (string, authz.Permission, error) {
	return editProject(h, ctx, a.ProjectID)
}, func(h *Handler, ctx context.Context, a reviewCommentArgs) (struct{}, error) {
	if h.reviewCommentRepo == nil {
		return struct{}{}, statusErr(http.StatusInternalServerError, "review comments not configured")
	}
	var err error
	if a.Delete {
		err = h.reviewCommentRepo.DeleteForProject(ctx, a.ProjectID, a.CommentID)
	} else {
		err = h.reviewCommentRepo.UpdateTextForProject(ctx, a.ProjectID, a.CommentID, a.Text)
	}
	switch {
	case err == nil:
		return struct{}{}, nil
	case errors.Is(err, persistence.ErrProjectDenied):
		return struct{}{}, projectAccessHTTPError(err)
	case errors.Is(err, persistence.ErrNotFound):
		return struct{}{}, statusErr(http.StatusNotFound, "review comment not found")
	}
	applog.Infof("[handler] review comment %s delete=%v error: %v", a.CommentID, a.Delete, err)
	if a.Delete {
		return struct{}{}, statusErr(http.StatusInternalServerError, "failed to delete review comment")
	}
	return struct{}{}, statusErr(http.StatusInternalServerError, "failed to update review comment")
})

func (h *Handler) reviewCommentAction(c echo.Context, args reviewCommentArgs) error {
	projectID, err := h.getCurrentProjectID(c)
	if err != nil {
		return err
	}
	args.ProjectID = projectID
	args.CommentID = c.Param("id")
	if _, err := actReviewComment(h, c.Request().Context(), args); err != nil {
		return err
	}
	return c.NoContent(http.StatusOK)
}

// UpdateReviewComment updates the text of an existing review comment.
func (h *Handler) UpdateReviewComment(c echo.Context) error {
	commentText := strings.TrimSpace(c.FormValue("comment_text"))
	if commentText == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "comment_text is required")
	}
	return h.reviewCommentAction(c, reviewCommentArgs{Text: commentText})
}

// DeleteReviewComment removes a review comment.
func (h *Handler) DeleteReviewComment(c echo.Context) error {
	return h.reviewCommentAction(c, reviewCommentArgs{Delete: true})
}

// SubmitReview collects all review comments, sends them to the task chat, and clears them.
func (h *Handler) SubmitReview(c echo.Context) error {
	taskID := c.Param("taskId")
	toast, err := actSubmitReview(h, c.Request().Context(), TaskRef{TaskID: taskID, ProjectID: h.mutationProjectID(c)})
	if err != nil {
		var httpErr *echo.HTTPError
		if errors.As(err, &httpErr) && httpErr.Message == "task does not belong to the active project" {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
		if errors.As(err, &httpErr) && httpErr.Code == http.StatusServiceUnavailable {
			c.Response().Header().Set("Retry-After", "30")
		}
		return err
	}
	if toast != "" {
		setHTMXToast(c, toast, "failed")
	}
	c.Response().Header().Set("HX-Redirect", fmt.Sprintf("/tasks/%s?tab=chat", taskID))
	return c.NoContent(http.StatusOK)
}

// actSubmitReview returns a failure toast when the review turn could not start.
var actSubmitReview = defineAction("review.submit", ownTaskRef[TaskRef], func(h *Handler, ctx context.Context, a TaskRef) (string, error) {
	taskID := a.TaskID
	if h.reviewCommentRepo == nil {
		return "", statusErr(http.StatusInternalServerError, "review comments not configured")
	}
	task, err := h.loadTask(ctx, taskID)
	if err != nil {
		return "", err
	}
	comments, err := h.reviewCommentRepo.ListByTask(ctx, taskID)
	if err != nil || len(comments) == 0 {
		return "", statusErr(http.StatusBadRequest, "no review comments to submit")
	}

	var sb strings.Builder
	sb.WriteString("Code review feedback:\n\n")
	for _, comment := range comments {
		sb.WriteString(fmt.Sprintf("**File: %s, line %d**: %s\n\n", comment.FilePath, comment.LineNumber, comment.CommentText))
	}
	sb.WriteString("Please address the above review comments and make the necessary changes.")
	reviewMessage := sb.String()

	agent, err := h.selectAgent(ctx, "", reviewMessage, false)
	if err != nil {
		if task.AgentID != nil {
			agent, _ = h.llmConfigRepo.GetByID(ctx, *task.AgentID)
		}
		if agent == nil {
			return "", statusErr(http.StatusBadRequest, "no agent available")
		}
	}

	admission, err := h.admitTaskFollowup(ctx, taskFollowupAdmissionRequest{
		Task:      task,
		Agent:     agent,
		Message:   reviewMessage,
		Source:    models.TaskOriginWeb,
		LogPrefix: "SubmitReview",
	})
	if err != nil {
		if admissionErr, ok := err.(*taskFollowupAdmissionError); ok {
			switch admissionErr.Op {
			case taskFollowupAdmissionOpActiveCheck:
				applog.Infof("[handler] SubmitReview active execution check failed task=%s: %v", taskID, admissionErr.Err)
				return "", statusErr(http.StatusInternalServerError, "failed to check active task turn")
			case taskFollowupAdmissionOpFirstTurnCheck:
				applog.Infof("[handler] SubmitReview first-turn state check failed task=%s: %v", taskID, admissionErr.Err)
				return "", statusErr(http.StatusInternalServerError, "failed to check task queue")
			case taskFollowupAdmissionOpQueueUnavailable:
				return "", statusErr(http.StatusInternalServerError, "thread input queue is unavailable")
			case taskFollowupAdmissionOpQueueCreate:
				applog.Infof("[handler] SubmitReview error creating queued input: %v", admissionErr.Err)
				return "", statusErr(http.StatusInternalServerError, "failed to queue review follow-up")
			case taskFollowupAdmissionOpDirectAdmission:
				applog.Infof("[handler] SubmitReview error admitting execution: %v", admissionErr.Err)
				return "", statusErr(http.StatusInternalServerError, "failed to admit execution")
			case taskFollowupAdmissionOpSwarmChildRouting:
				applog.Infof("[handler] SubmitReview swarm child follow-up routing failed task=%s: %v", taskID, admissionErr.Err)
				return "", statusErr(http.StatusInternalServerError, "failed to route swarm follow-up")
			}
		}
		return "", statusErr(http.StatusInternalServerError, "failed to admit execution")
	}
	if admission.Queued != nil {
		if err := h.reviewCommentRepo.DeleteSubmitted(ctx, taskID, comments); err != nil {
			applog.Infof("[handler] SubmitReview error clearing comments: %v", err)
		}
		return "", nil
	}

	exec := admission.Execution
	task = admission.Task
	applog.Infof("[handler] SubmitReview created review exec=%s for task=%s with %d comments", exec.ID, taskID, len(comments))

	h.resumeUserStoppedGoalForManualStart(ctx, taskID, models.TaskOriginWeb, "")
	h.reactivateAchievedGoalForManualFollowup(ctx, taskID, models.TaskOriginWeb, "")
	priorExecs, _ := h.execRepo.ListByTaskChronologicalLimit(persistence.WithModelHistory(ctx, agent.Provider), taskID, taskThreadHistoryLimit)
	priorHistory := filterChatHistory(priorExecs, exec.ID)
	agentDef := h.resolveTaskAgentDefinitionForTask(ctx, taskID, nil)
	systemContext := combineContexts(buildThreadSystemContext(task.Title, len(priorHistory) > 0, ""), h.taskGoalContext(ctx, task.ID, agentDef))
	workDir, worktreeContext, republishOpenPR, workDirErr := h.resolveWorktreeWorkDir(ctx, task)
	if workDirErr != nil {
		h.completeWithFailure(ctx, exec.ID, taskID, workDirErr.Error(), 0)
		go h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{ProjectID: task.ProjectID, TaskID: taskID, IsTaskFollowup: true}, exec.ID)
		return workDirErr.Error(), nil
	}

	if err := h.startStreamingResponse(streamingResponseParams{
		ExecID:                          exec.ID,
		TaskID:                          taskID,
		Message:                         reviewMessage,
		Agent:                           *agent,
		AgentDefinition:                 agentDef,
		ChatHistory:                     priorHistory,
		ProjectID:                       task.ProjectID,
		SystemContext:                   combineContexts(systemContext, worktreeContext),
		WorkDir:                         workDir,
		IsTaskFollowup:                  true,
		RepublishOpenPRAfterStartupSync: republishOpenPR,
	}); err != nil {
		return "", statusErr(http.StatusServiceUnavailable, err.Error())
	}

	// Clear only the submitted comment versions; comments saved or edited while submission was in progress stay as drafts
	if err := h.reviewCommentRepo.DeleteSubmitted(ctx, taskID, comments); err != nil {
		applog.Infof("[handler] SubmitReview error clearing comments: %v", err)
	}
	return "", nil
})
