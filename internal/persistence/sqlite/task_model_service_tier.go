package sqlite

import (
	"context"
	"github.com/openvibely/openvibely/internal/models"
)

func (r *TaskRepo) SetModelServiceTier(ctx context.Context, taskID string, agent models.LLMConfig, tier string) error {
	return r.SetModelOverrides(ctx, taskID, agent, nil, &tier)
}
func (r *TaskRepo) ModelServiceTier(ctx context.Context, taskID string, agent models.LLMConfig) (string, error) {
	_, tier, err := modelOverrides(ctx, r.db, taskID, agent)
	return tier, err
}
