package handler

import (
	"context"

	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Adapters for tests of the production entry points.

// completeWithSuccess marks an execution and its task as completed.
// Also moves Active tasks to the Completed category so they appear in the right column.
// Logs errors but does not fail since this runs in a background goroutine.
// Captures git diff if workDir is provided.
func (h *Handler) completeWithSuccess(ctx context.Context, execID, taskID, output, workDir string, tokensUsed int, durationMs int64, completionOptions ...interface{}) persistence.CompleteSuccessOutcome {
	outcome, err := h.completeWithSuccessWithPRReconciliation(ctx, execID, taskID, output, workDir, tokensUsed, durationMs, false, completionOptions...)
	if err != nil {
		applog.Infof("[handler] completeWithSuccess exec=%s completion policy failed: %v", execID, err)
		h.completeWithFinalizationError(ctx, execID, taskID, err, output, tokensUsed, durationMs, completionOptions...)
		return persistence.CompleteSuccessAlreadyTerminal
	}
	return outcome
}

// formatThreadTranscript formats a task's execution history as a readable thread transcript.
// offset/limit control pagination: offset is the execution index to start from (0-based),
// limit is the max number of executions to include (0 = all).
func (h *Handler) formatThreadTranscript(task *models.Task, executions []models.Execution, offset, limit int) string {
	total := len(executions)
	if offset > 0 {
		if offset >= total {
			return h.formatThreadTranscriptPage(task, nil, total, offset).transcript
		}
		executions = executions[offset:]
	}
	if limit > 0 && limit < len(executions) {
		executions = executions[:limit]
	}
	return h.formatThreadTranscriptWithTotal(task, executions, total, offset)
}
