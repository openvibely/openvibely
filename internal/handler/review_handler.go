package handler

import (
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
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
		log.Printf("[handler] ListReviewComments error: %v", err)
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
		log.Printf("[handler] AddReviewComment error: %v", err)
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
		log.Printf("[handler] UpdateReviewComment error: %v", err)
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
		log.Printf("[handler] DeleteReviewComment error: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete review comment")
	}

	// Return empty response; HTMX will remove the element
	return c.NoContent(http.StatusOK)
}

// SubmitReview collects all review comments, sends them to the task chat, and clears them.
func (h *Handler) SubmitReview(c echo.Context) error {
	taskID := c.Param("taskId")
	if h.reviewCommentRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "review comments not configured")
	}

	comments, err := h.reviewCommentRepo.ListByTask(c.Request().Context(), taskID)
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

	// Get the task to check its state
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

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

	// Set status to "queued" (same pattern as TaskThreadSend — processStreamingResponse
	// will acquire worker slots and transition to "running").
	if task.Status != models.StatusRunning && task.Status != models.StatusQueued {
		log.Printf("[handler] SubmitReview setting task=%s status=queued (was %s)", taskID, task.Status)
		if err := h.taskRepo.UpdateStatus(c.Request().Context(), taskID, models.StatusQueued); err != nil {
			log.Printf("[handler] SubmitReview error setting status: %v", err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to update task status")
		}
	}
	// Always move to active category so the task appears in the Active column
	if task.Category != models.CategoryActive {
		if err := h.taskRepo.UpdateCategory(c.Request().Context(), taskID, models.CategoryActive); err != nil {
			log.Printf("[handler] SubmitReview error updating category: %v", err)
		}
	}

	// Create execution record for the review follow-up
	exec := &models.Execution{
		TaskID:        taskID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    reviewMessage,
		IsFollowup:    true,
	}
	if err := h.execRepo.Create(c.Request().Context(), exec); err != nil {
		log.Printf("[handler] SubmitReview error creating execution: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create execution")
	}

	log.Printf("[handler] SubmitReview created review exec=%s for task=%s with %d comments", exec.ID, taskID, len(comments))

	// Build system context and spawn LLM processing
	priorExecs, _ := h.execRepo.ListByTaskChronological(c.Request().Context(), taskID)
	priorHistory := filterChatHistory(priorExecs, exec.ID)
	systemContext := buildThreadSystemContext(task.Title, len(priorHistory) > 0, "")
	personalityContext := h.getPersonalityContext(c.Request().Context(), task.ProjectID)
	workDir := h.resolveWorktreeWorkDir(c.Request().Context(), task)
	var agentDef *models.Agent
	if task.AgentDefinitionID != nil && h.agentRepo != nil {
		if ad, adErr := h.agentRepo.GetByID(c.Request().Context(), *task.AgentDefinitionID); adErr == nil && ad != nil {
			agentDef = ad
		}
	}

	go h.processStreamingResponse(streamingResponseParams{
		ExecID:          exec.ID,
		TaskID:          taskID,
		Message:         reviewMessage,
		Agent:           *agent,
		AgentDefinition: agentDef,
		ChatHistory:     priorHistory,
		ProjectID:       task.ProjectID,
		SystemContext:   combineContexts(systemContext, personalityContext),
		WorkDir:         workDir,
		IsTaskFollowup:  true,
	})

	// Clear the review comments after submission
	if err := h.reviewCommentRepo.DeleteByTask(c.Request().Context(), taskID); err != nil {
		log.Printf("[handler] SubmitReview error clearing comments: %v", err)
	}

	// Redirect to chat tab to see the review being processed
	c.Response().Header().Set("HX-Redirect", fmt.Sprintf("/tasks/%s?tab=chat", taskID))
	return c.NoContent(http.StatusOK)
}
