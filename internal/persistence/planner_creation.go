package persistence

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
)

type plannerCreationKey struct{}

type plannerCreation struct {
	parent      models.Task
	followup    bool
	swarmStatus string
	swarmConfig string
}

// WithPlannerCreationParent pins the parent observed before first-planner creation.
func WithPlannerCreationParent(ctx context.Context, parent models.Task) context.Context {
	return context.WithValue(ctx, plannerCreationKey{}, plannerCreation{parent: parent})
}

// WithPlannerCreationFollowup also commits the follow-up's parent generation
// change with the first planner, so a failed creation leaves the parent intact.
func WithPlannerCreationFollowup(ctx context.Context, parent models.Task, swarmStatus, swarmConfig string) context.Context {
	return context.WithValue(ctx, plannerCreationKey{}, plannerCreation{parent: parent, followup: true, swarmStatus: swarmStatus, swarmConfig: swarmConfig})
}

func PlannerCreationParent(ctx context.Context) (models.Task, bool) {
	creation, ok := ctx.Value(plannerCreationKey{}).(plannerCreation)
	return creation.parent, ok
}

// PlannerCreationFollowup returns the parent swarm fields a follow-up commits.
func PlannerCreationFollowup(ctx context.Context) (swarmStatus, swarmConfig string, ok bool) {
	creation, found := ctx.Value(plannerCreationKey{}).(plannerCreation)
	return creation.swarmStatus, creation.swarmConfig, found && creation.followup
}
