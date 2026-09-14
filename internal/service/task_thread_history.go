package service

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

// taskThreadExecutionFetchBatchSize keeps zero-limit runtime reads bounded.
const taskThreadExecutionFetchBatchSize = 20

// LoadBoundedTaskThreadExecutions loads a task's chronological executions in
// bounded pages until the history ends or budgetExceeded reports that the
// caller's transcript budget has been reached.
func LoadBoundedTaskThreadExecutions(
	ctx context.Context,
	execRepo *repository.ExecutionRepo,
	task *models.Task,
	total, offset int,
	budgetExceeded func([]models.Execution) bool,
) ([]models.Execution, error) {
	if execRepo == nil || task == nil || total <= 0 || offset < 0 || offset >= total {
		return []models.Execution{}, nil
	}

	capacity := taskThreadExecutionFetchBatchSize
	if remaining := total - offset; remaining < capacity {
		capacity = remaining
	}
	executions := make([]models.Execution, 0, capacity)
	nextOffset := offset
	for nextOffset < total {
		batchLimit := taskThreadExecutionFetchBatchSize
		if remaining := total - nextOffset; remaining < batchLimit {
			batchLimit = remaining
		}
		batch, err := execRepo.ListByTaskChronologicalPage(ctx, task.ID, nextOffset, batchLimit)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		executions = append(executions, batch...)
		if budgetExceeded != nil && budgetExceeded(executions) {
			break
		}
		if len(batch) < batchLimit {
			break
		}
		nextOffset += len(batch)
	}
	return executions, nil
}
