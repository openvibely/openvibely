package persistence

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
)

type plannerCreationKey struct{}

// WithPlannerCreationParent pins the parent observed before first-planner creation.
func WithPlannerCreationParent(ctx context.Context, parent models.Task) context.Context {
	return context.WithValue(ctx, plannerCreationKey{}, parent)
}
func PlannerCreationParent(ctx context.Context) (models.Task, bool) {
	parent, ok := ctx.Value(plannerCreationKey{}).(models.Task)
	return parent, ok
}
