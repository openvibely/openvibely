package components

import (
	"context"
	"fmt"
	"net/url"

	"github.com/openvibely/openvibely/internal/models"
)

// ThreadSurface supplies authorized endpoints to the existing task-thread view.
// An absent surface retains the task-thread endpoints and behavior.
type ThreadSurface struct{ BaseURL, ProjectID, SendURL, StopURL string }
type threadSurfaceKey struct{}

func WithThreadSurface(ctx context.Context, surface ThreadSurface) context.Context {
	return context.WithValue(ctx, threadSurfaceKey{}, surface)
}
func threadSurface(ctx context.Context) ThreadSurface {
	s, _ := ctx.Value(threadSurfaceKey{}).(ThreadSurface)
	return s
}
func threadAPIBase(ctx context.Context, task *models.Task) string {
	if s := threadSurface(ctx); s.BaseURL != "" {
		return s.BaseURL
	}
	return fmt.Sprintf("/tasks/%s/thread", task.ID)
}
func threadProjectID(ctx context.Context) string { return threadSurface(ctx).ProjectID }
func threadEndpoint(ctx context.Context, task *models.Task, suffix, query string) string {
	if s := threadSurface(ctx); s.ProjectID != "" {
		v, _ := url.ParseQuery(query)
		v.Set("project_id", s.ProjectID)
		query = v.Encode()
	}
	result := threadAPIBase(ctx, task) + suffix
	if query != "" {
		result += "?" + query
	}
	return result
}
func threadSendEndpoint(ctx context.Context, task *models.Task) string {
	if s := threadSurface(ctx); s.SendURL != "" {
		return s.SendURL
	}
	return fmt.Sprintf("/tasks/%s/thread?project_id=%s", task.ID, url.QueryEscape(task.ProjectID))
}
func threadStopEndpoint(ctx context.Context, task *models.Task) string {
	if s := threadSurface(ctx); s.StopURL != "" {
		return s.StopURL
	}
	return SwarmParentStopEndpoint(task, fmt.Sprintf("/tasks/%s/cancel?composer_stop=1", task.ID))
}
func threadQueuedSteerEndpoint(ctx context.Context, task *models.Task) func(models.ThreadInput) string {
	if threadProjectID(ctx) == "" {
		return taskQueuedSteerEndpoint
	}
	return func(in models.ThreadInput) string {
		return threadEndpoint(ctx, task, "/queued/"+url.PathEscape(in.ID)+"/steer", "expected_turn_id="+url.QueryEscape(in.RunExecutionID))
	}
}

func threadEmptyMessage(ctx context.Context) string {
	if threadProjectID(ctx) != "" {
		return "Ask a question about this page."
	}
	return "No messages yet. Click \"Run Now\" to start, then follow up here."
}
