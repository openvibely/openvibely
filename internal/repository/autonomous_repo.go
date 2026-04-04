package repository

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
)

// AutonomousRepo handles autonomous build configuration storage.
type AutonomousRepo struct {
	db *sql.DB
}

func NewAutonomousRepo(db *sql.DB) *AutonomousRepo {
	return &AutonomousRepo{db: db}
}

// GetConfig returns the autonomous build config for a project.
func (r *AutonomousRepo) GetConfig(ctx context.Context, projectID string) (*models.AutonomousConfig, error) {
	var c models.AutonomousConfig
	var enabled int
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, enabled, max_execution_hours, protected_files, excluded_areas, schedule_hour, created_at, updated_at
		FROM autonomous_config WHERE project_id = ?`, projectID,
	).Scan(&c.ID, &c.ProjectID, &enabled, &c.MaxExecutionHours, &c.ProtectedFiles, &c.ExcludedAreas, &c.ScheduleHour, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.Enabled = enabled != 0
	return &c, nil
}

// UpsertConfig creates or updates the autonomous build config for a project.
func (r *AutonomousRepo) UpsertConfig(ctx context.Context, c *models.AutonomousConfig) error {
	enabled := 0
	if c.Enabled {
		enabled = 1
	}
	return r.db.QueryRowContext(ctx, `
		INSERT INTO autonomous_config (project_id, enabled, max_execution_hours, protected_files, excluded_areas, schedule_hour)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			enabled = excluded.enabled,
			max_execution_hours = excluded.max_execution_hours,
			protected_files = excluded.protected_files,
			excluded_areas = excluded.excluded_areas,
			schedule_hour = excluded.schedule_hour,
			updated_at = datetime('now')
		RETURNING id, created_at, updated_at`,
		c.ProjectID, enabled, c.MaxExecutionHours, c.ProtectedFiles, c.ExcludedAreas, c.ScheduleHour,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}
