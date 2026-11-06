package persistence

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
)

// PagePluginStore persists page-plugin packages, per-project instances, and side-chat bindings.
type PagePluginStore interface {
	CreatePackage(ctx context.Context, p *models.PagePluginPackage) error
	GetPackage(ctx context.Context, id string) (*models.PagePluginPackage, error)
	GetPackageByVersion(ctx context.Context, pluginID, version string) (*models.PagePluginPackage, error)
	ListPackages(ctx context.Context) ([]models.PagePluginPackage, error)
	DeletePackage(ctx context.Context, id string) error
	CountInstancesForPackage(ctx context.Context, packageID string) (int, error)

	StageReplacement(ctx context.Context, instanceID, oldPackageID, newPackageID string) error
	GetReplacement(ctx context.Context, instanceID string) (*models.PagePluginInstance, error)
	FinishReplacement(ctx context.Context, instanceID string, restore bool) error
	CreateInstance(ctx context.Context, in *models.PagePluginInstance) error
	GetInstance(ctx context.Context, id string) (*models.PagePluginInstance, error)
	ListInstances(ctx context.Context, projectID string, enabledOnly bool) ([]models.PagePluginInstance, error)
	ListAllInstances(ctx context.Context, enabledOnly bool) ([]models.PagePluginInstance, error)
	SetInstanceEnabled(ctx context.Context, id string, enabled bool) error
	ApproveInstance(ctx context.Context, id, contentHash, approvedBy string) error
	UpdateInstanceState(ctx context.Context, id, stateJSON string) error
	DeleteInstance(ctx context.Context, id string) error

	GetConversationTask(ctx context.Context, instanceID string) (string, error)
	SetConversationTask(ctx context.Context, instanceID, taskID string) error
	SaveTurnContext(ctx context.Context, executionID, instanceID, snapshotJSON string) error
	GetTurnContext(ctx context.Context, executionID string) (string, error)
}
