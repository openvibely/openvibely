package handler

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

// ListReviewComments returns all review comments for a task as an HTML fragment.
func (h *Handler) ListReviewComments(c echo.Context) error {
	taskID := c.Param("taskId")
	if h.reviewCommentRepo == nil {
		return render(c, http.StatusOK, components.ReviewCommentList(nil, taskID))
	}

	comments, err := h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	if err != nil {
		applog.Infof("[handler] ListReviewComments error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to load review comments")
	}

	return render(c, http.StatusOK, components.ReviewCommentList(comments, taskID))
}

// AddReviewComment adds a new inline review comment.
func (h *Handler) AddReviewComment(c echo.Context) error {
	taskID := c.Param("taskId")
	if h.reviewCommentRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "review comments not configured")
	}

	filePath := c.FormValue("file_path")
	lineNumStr := c.FormValue("line_number")
	lineType := c.FormValue("line_type")
	commentText := c.FormValue("comment_text")

	if filePath == "" || lineNumStr == "" || commentText == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "file_path, line_number, and comment_text are required")
	}

	lineNum, err := strconv.Atoi(lineNumStr)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, "invalid line_number")
	}

	if lineType == "" {
		lineType = "new"
	}

	comment := &models.ReviewComment{
		TaskID:      taskID,
		FilePath:    filePath,
		LineNumber:  lineNum,
		LineType:    lineType,
		CommentText: strings.TrimSpace(commentText),
		ReviewedBy:  "user",
	}

	if err := h.reviewCommentRepo.Create(c.Request().Context(), comment); err != nil {
		applog.Infof("[handler] AddReviewComment error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to add review comment")
	}

	// Return the updated comment list
	comments, _ := h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
	return render(c, http.StatusOK, components.ReviewCommentList(comments, taskID))
}

// UpdateReviewComment updates the text of an existing review comment.
func (h *Handler) UpdateReviewComment(c echo.Context) error {
	id := c.Param("id")
	if h.reviewCommentRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "review comments not configured")
	}

	commentText := strings.TrimSpace(c.FormValue("comment_text"))
	if commentText == "" {
		return echo.NewHTTPError(http.StatusBadRequest, "comment_text is required")
	}

	if err := h.reviewCommentRepo.UpdateText(c.Request().Context(), id, commentText); err != nil {
		applog.Infof("[handler] UpdateReviewComment error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to update review comment")
	}

	return c.NoContent(http.StatusOK)
}

// DeleteReviewComment removes a review comment.
func (h *Handler) DeleteReviewComment(c echo.Context) error {
	id := c.Param("id")
	if h.reviewCommentRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "review comments not configured")
	}

	if err := h.reviewCommentRepo.Delete(c.Request().Context(), id); err != nil {
		applog.Infof("[handler] DeleteReviewComment error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete review comment")
	}

	// Return empty response; HTMX will remove the element
	return c.NoContent(http.StatusOK)
}

// SubmitReview collects all review comments, sends them to the task chat, and clears them.
func (h *Handler) SubmitReview(c echo.Context) error {
	ctx := c.Request().Context()
	taskID := c.Param("taskId")
	if h.reviewCommentRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "review comments not configured")
	}

	projectID := h.mutationProjectID(c)
	task, err := h.requireTaskInRequestProject(ctx, taskID, projectID)
	if err != nil {
		if httpErr, ok := err.(*echo.HTTPError); ok && httpErr.Code == http.StatusBadRequest {
			return echo.NewHTTPError(http.StatusNotFound, "task not found")
		}
		return err
	}

	comments, err := h.reviewCommentRepo.ListByTask(ctx, taskID)
	if err != nil || len(comments) == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no review comments to submit")
	}

	// Build the review message
	var sb strings.Builder
	sb.WriteString("Code review feedback:\n\n")
	for _, comment := range comments {
		sb.WriteString(fmt.Sprintf("**File: %s, line %d**: %s\n\n", comment.FilePath, comment.LineNumber, comment.CommentText))
	}
	sb.WriteString("Please address the above review comments and make the necessary changes.")

	reviewMessage := sb.String()

	// Select agent for processing the review
	agent, err := h.selectAgent(c.Request().Context(), "", reviewMessage, false)
	if err != nil {
		// Fall back to task's assigned agent
		if task.AgentID != nil {
			agent, _ = h.llmConfigRepo.GetByID(c.Request().Context(), *task.AgentID)
		}
		if agent == nil {
			return echo.NewHTTPError(http.StatusBadRequest, "no agent available")
		}
	}

	admission, err := h.admitTaskFollowup(c.Request().Context(), taskFollowupAdmissionRequest{
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
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to check active task turn")
			case taskFollowupAdmissionOpFirstTurnCheck:
				applog.Infof("[handler] SubmitReview first-turn state check failed task=%s: %v", taskID, admissionErr.Err)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to check task queue")
			case taskFollowupAdmissionOpQueueUnavailable:
				return echo.NewHTTPError(http.StatusInternalServerError, "thread input queue is unavailable")
			case taskFollowupAdmissionOpQueueCreate:
				applog.Infof("[handler] SubmitReview error creating queued input: %v", admissionErr.Err)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to queue review follow-up")
			case taskFollowupAdmissionOpDirectAdmission:
				applog.Infof("[handler] SubmitReview error admitting execution: %v", admissionErr.Err)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to admit execution")
			case taskFollowupAdmissionOpSwarmChildRouting:
				applog.Infof("[handler] SubmitReview swarm child follow-up routing failed task=%s: %v", taskID, admissionErr.Err)
				return echo.NewHTTPError(http.StatusInternalServerError, "failed to route swarm follow-up")
			}
		}
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to admit execution")
	}
	if admission.Queued != nil {
		if err := h.reviewCommentRepo.DeleteByTask(c.Request().Context(), taskID); err != nil {
			applog.Infof("[handler] SubmitReview error clearing comments: %v", err)
		}
		c.Response().Header().Set("HX-Redirect", fmt.Sprintf("/tasks/%s?tab=chat", taskID))
		return c.NoContent(http.StatusOK)
	}

	exec := admission.Execution
	task = admission.Task
	applog.Infof("[handler] SubmitReview created review exec=%s for task=%s with %d comments", exec.ID, taskID, len(comments))

	// Build system context and spawn LLM processing
	h.resumeUserStoppedGoalForManualStart(c.Request().Context(), taskID, models.TaskOriginWeb, "")
	h.reactivateAchievedGoalForManualFollowup(c.Request().Context(), taskID, models.TaskOriginWeb, "")
	priorExecs, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	priorHistory := filterChatHistory(priorExecs, exec.ID)
	agentDef := h.resolveTaskAgentDefinitionForTask(c.Request().Context(), taskID, nil)
	systemContext := combineContexts(buildThreadSystemContext(task.Title, len(priorHistory) > 0, ""), h.taskGoalContext(c.Request().Context(), task.ID, agentDef))
	personalityContext := h.getPersonalityContext(c.Request().Context(), task.ProjectID)
	workDir, worktreeContext, workDirErr := h.resolveWorktreeWorkDir(c.Request().Context(), task)
	if workDirErr != nil {
		h.completeWithFailure(c.Request().Context(), exec.ID, taskID, workDirErr.Error(), 0)
		go h.startNextQueuedTurnAfter(context.Background(), streamingResponseParams{ProjectID: task.ProjectID, TaskID: taskID, IsTaskFollowup: true}, exec.ID)
		setHTMXToast(c, workDirErr.Error(), "failed")
		c.Response().Header().Set("HX-Redirect", fmt.Sprintf("/tasks/%s?tab=chat", taskID))
		return c.NoContent(http.StatusOK)
	}

	if err := h.startStreamingResponse(streamingResponseParams{
		ExecID:          exec.ID,
		TaskID:          taskID,
		Message:         reviewMessage,
		Agent:           *agent,
		AgentDefinition: agentDef,
		ChatHistory:     priorHistory,
		ProjectID:       task.ProjectID,
		SystemContext:   combineContexts(combineContexts(systemContext, worktreeContext), personalityContext),
		WorkDir:         workDir,
		IsTaskFollowup:  true,
	}); err != nil {
		c.Response().Header().Set("Retry-After", "30")
		return echo.NewHTTPError(http.StatusServiceUnavailable, err.Error())
	}

	// Clear the review comments after submission
	if err := h.reviewCommentRepo.DeleteByTask(c.Request().Context(), taskID); err != nil {
		applog.Infof("[handler] SubmitReview error clearing comments: %v", err)
	}

	// Redirect to chat tab to see the review being processed
	c.Response().Header().Set("HX-Redirect", fmt.Sprintf("/tasks/%s?tab=chat", taskID))
	return c.NoContent(http.StatusOK)
}
