package components

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
)

type taskFilesContextKey struct{}
type TaskFilesSnapshot struct {
	TaskID    string
	ProjectID string
	Messages  []models.ChatAttachment
	Generated []models.GeneratedFile
}

func WithTaskFiles(ctx context.Context, files TaskFilesSnapshot) context.Context {
	return context.WithValue(ctx, taskFilesContextKey{}, files)
}
func taskFilesSnapshot(ctx context.Context, taskID, projectID string) (TaskFilesSnapshot, bool) {
	files, ok := ctx.Value(taskFilesContextKey{}).(TaskFilesSnapshot)
	return files, ok && files.TaskID == taskID && files.ProjectID == projectID
}
func taskFilesTriggers(ctx context.Context, taskID, projectID string) string {
	if _, ok := taskFilesSnapshot(ctx, taskID, projectID); ok {
		return "taskAttachmentsChanged from:body, taskFilesChanged"
	}
	return "load, taskAttachmentsChanged from:body, taskFilesChanged"
}
