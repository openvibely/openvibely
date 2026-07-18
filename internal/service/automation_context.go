package service

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

type automationContextKey struct{}
type preparedAutomationExecutionKey struct{}
type automationExecutionKey struct{}

type automationExecutionIdentity struct {
	TaskID      string
	ExecutionID string
}

func WithAutomationContext(ctx context.Context, automationContext models.AutomationContext) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, automationContextKey{}, automationContext)
}

func AutomationContextFromContext(ctx context.Context) (models.AutomationContext, bool) {
	if ctx == nil {
		return models.AutomationContext{}, false
	}
	value, ok := ctx.Value(automationContextKey{}).(models.AutomationContext)
	return value, ok && value.ProjectID != "" && len(value.Bindings) > 0
}

func AutomationExecutionFromContext(ctx context.Context) (taskID, executionID string, ok bool) {
	if ctx == nil {
		return "", "", false
	}
	value, ok := ctx.Value(automationExecutionKey{}).(automationExecutionIdentity)
	return value.TaskID, value.ExecutionID, ok && value.TaskID != "" && value.ExecutionID != ""
}

func withAutomationExecution(ctx context.Context, taskID, executionID string) context.Context {
	return context.WithValue(ctx, automationExecutionKey{}, automationExecutionIdentity{TaskID: taskID, ExecutionID: executionID})
}

func withPreparedAutomationExecution(ctx context.Context, executionID string) context.Context {
	return context.WithValue(ctx, preparedAutomationExecutionKey{}, executionID)
}

func preparedAutomationExecutionID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(preparedAutomationExecutionKey{}).(string)
	return value
}
