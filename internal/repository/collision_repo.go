package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type CollisionRepo struct {
	db *sql.DB
}

func NewCollisionRepo(db *sql.DB) *CollisionRepo {
	return &CollisionRepo{db: db}
}

// --- Impact Analyses ---

func (r *CollisionRepo) CreateImpactAnalysis(ctx context.Context, ia *models.ImpactAnalysis) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO impact_analyses (task_id, project_id, files_impacted, apis_impacted, schemas_impacted, components_impacted, impact_summary, confidence, analysis_model)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		ia.TaskID, ia.ProjectID, ia.FilesImpacted, ia.APIsImpacted, ia.SchemasImpacted, ia.ComponentsImpacted, ia.ImpactSummary, ia.Confidence, ia.AnalysisModel,
	).Scan(&ia.ID, &ia.CreatedAt, &ia.UpdatedAt)
}

func (r *CollisionRepo) GetImpactAnalysisByTaskID(ctx context.Context, taskID string) (*models.ImpactAnalysis, error) {
	var ia models.ImpactAnalysis
	err := r.db.QueryRowContext(ctx, `
		SELECT id, task_id, project_id, files_impacted, apis_impacted, schemas_impacted, components_impacted, impact_summary, confidence, analysis_model, created_at, updated_at
		FROM impact_analyses WHERE task_id = ? ORDER BY created_at DESC LIMIT 1`, taskID).Scan(
		&ia.ID, &ia.TaskID, &ia.ProjectID, &ia.FilesImpacted, &ia.APIsImpacted, &ia.SchemasImpacted, &ia.ComponentsImpacted, &ia.ImpactSummary, &ia.Confidence, &ia.AnalysisModel, &ia.CreatedAt, &ia.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ia, nil
}

func (r *CollisionRepo) ListImpactAnalysesByProject(ctx context.Context, projectID string) ([]models.ImpactAnalysis, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT ia.id, ia.task_id, ia.project_id, ia.files_impacted, ia.apis_impacted, ia.schemas_impacted, ia.components_impacted, ia.impact_summary, ia.confidence, ia.analysis_model, ia.created_at, ia.updated_at
		FROM impact_analyses ia
		JOIN tasks t ON ia.task_id = t.id
		WHERE ia.project_id = ? AND t.status IN ('pending', 'running')
		ORDER BY ia.created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var analyses []models.ImpactAnalysis
	for rows.Next() {
		var ia models.ImpactAnalysis
		if err := rows.Scan(&ia.ID, &ia.TaskID, &ia.ProjectID, &ia.FilesImpacted, &ia.APIsImpacted, &ia.SchemasImpacted, &ia.ComponentsImpacted, &ia.ImpactSummary, &ia.Confidence, &ia.AnalysisModel, &ia.CreatedAt, &ia.UpdatedAt); err != nil {
			return nil, err
		}
		analyses = append(analyses, ia)
	}
	return analyses, rows.Err()
}

func (r *CollisionRepo) DeleteImpactAnalysis(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM impact_analyses WHERE id = ?`, id)
	return err
}

// --- Conflict Predictions ---

func (r *CollisionRepo) CreateConflictPrediction(ctx context.Context, cp *models.ConflictPrediction) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO conflict_predictions (project_id, task_a_id, task_b_id, conflict_type, severity, description, overlapping_resources, resolution_strategy, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		cp.ProjectID, cp.TaskAID, cp.TaskBID, cp.ConflictType, cp.Severity, cp.Description, cp.OverlappingResources, cp.ResolutionStrategy, cp.Status,
	).Scan(&cp.ID, &cp.CreatedAt, &cp.UpdatedAt)
}

func (r *CollisionRepo) GetConflictPrediction(ctx context.Context, id string) (*models.ConflictPrediction, error) {
	var cp models.ConflictPrediction
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, task_a_id, task_b_id, conflict_type, severity, description, overlapping_resources, resolution_strategy, status, resolved_at, created_at, updated_at
		FROM conflict_predictions WHERE id = ?`, id).Scan(
		&cp.ID, &cp.ProjectID, &cp.TaskAID, &cp.TaskBID, &cp.ConflictType, &cp.Severity, &cp.Description, &cp.OverlappingResources, &cp.ResolutionStrategy, &cp.Status, &cp.ResolvedAt, &cp.CreatedAt, &cp.UpdatedAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &cp, nil
}

func (r *CollisionRepo) ListActiveConflicts(ctx context.Context, projectID string) ([]models.ConflictPrediction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, task_a_id, task_b_id, conflict_type, severity, description, overlapping_resources, resolution_strategy, status, resolved_at, created_at, updated_at
		FROM conflict_predictions
		WHERE project_id = ? AND status IN ('detected', 'acknowledged')
		ORDER BY
			CASE severity WHEN 'critical' THEN 0 WHEN 'high' THEN 1 WHEN 'medium' THEN 2 ELSE 3 END,
			created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conflicts []models.ConflictPrediction
	for rows.Next() {
		var cp models.ConflictPrediction
		if err := rows.Scan(&cp.ID, &cp.ProjectID, &cp.TaskAID, &cp.TaskBID, &cp.ConflictType, &cp.Severity, &cp.Description, &cp.OverlappingResources, &cp.ResolutionStrategy, &cp.Status, &cp.ResolvedAt, &cp.CreatedAt, &cp.UpdatedAt); err != nil {
			return nil, err
		}
		conflicts = append(conflicts, cp)
	}
	return conflicts, rows.Err()
}

func (r *CollisionRepo) ListConflictsForTask(ctx context.Context, taskID string) ([]models.ConflictPrediction, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, task_a_id, task_b_id, conflict_type, severity, description, overlapping_resources, resolution_strategy, status, resolved_at, created_at, updated_at
		FROM conflict_predictions
		WHERE (task_a_id = ? OR task_b_id = ?) AND status IN ('detected', 'acknowledged')
		ORDER BY created_at DESC`, taskID, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var conflicts []models.ConflictPrediction
	for rows.Next() {
		var cp models.ConflictPrediction
		if err := rows.Scan(&cp.ID, &cp.ProjectID, &cp.TaskAID, &cp.TaskBID, &cp.ConflictType, &cp.Severity, &cp.Description, &cp.OverlappingResources, &cp.ResolutionStrategy, &cp.Status, &cp.ResolvedAt, &cp.CreatedAt, &cp.UpdatedAt); err != nil {
			return nil, err
		}
		conflicts = append(conflicts, cp)
	}
	return conflicts, rows.Err()
}

func (r *CollisionRepo) UpdateConflictStatus(ctx context.Context, id string, status models.ConflictStatus) error {
	var resolvedAt interface{}
	if status == models.ConflictResolved || status == models.ConflictFalsePositive {
		now := time.Now().UTC()
		resolvedAt = now
	}
	_, err := r.db.ExecContext(ctx, `
		UPDATE conflict_predictions SET status = ?, resolved_at = ?, updated_at = datetime('now')
		WHERE id = ?`, status, resolvedAt, id)
	return err
}

// ExistingConflict checks if a conflict prediction already exists between two tasks
func (r *CollisionRepo) ExistingConflict(ctx context.Context, taskAID, taskBID string, conflictType models.ConflictType) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM conflict_predictions
		WHERE ((task_a_id = ? AND task_b_id = ?) OR (task_a_id = ? AND task_b_id = ?))
		AND conflict_type = ? AND status IN ('detected', 'acknowledged')`,
		taskAID, taskBID, taskBID, taskAID, conflictType).Scan(&count)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// --- Conflict History ---

func (r *CollisionRepo) CreateConflictHistory(ctx context.Context, ch *models.ConflictHistory) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO conflict_history (project_id, task_a_id, task_b_id, prediction_id, was_predicted, conflict_type, actual_files, resolution, impact_score)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		ch.ProjectID, ch.TaskAID, ch.TaskBID, ch.PredictionID, ch.WasPredicted, ch.ConflictType, ch.ActualFiles, ch.Resolution, ch.ImpactScore,
	).Scan(&ch.ID, &ch.CreatedAt)
}

func (r *CollisionRepo) ListConflictHistory(ctx context.Context, projectID string, limit int) ([]models.ConflictHistory, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, task_a_id, task_b_id, prediction_id, was_predicted, conflict_type, actual_files, resolution, impact_score, created_at
		FROM conflict_history
		WHERE project_id = ?
		ORDER BY created_at DESC
		LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var history []models.ConflictHistory
	for rows.Next() {
		var ch models.ConflictHistory
		if err := rows.Scan(&ch.ID, &ch.ProjectID, &ch.TaskAID, &ch.TaskBID, &ch.PredictionID, &ch.WasPredicted, &ch.ConflictType, &ch.ActualFiles, &ch.Resolution, &ch.ImpactScore, &ch.CreatedAt); err != nil {
			return nil, err
		}
		history = append(history, ch)
	}
	return history, rows.Err()
}

// PredictionAccuracy returns the ratio of correctly predicted conflicts to total actual conflicts
func (r *CollisionRepo) PredictionAccuracy(ctx context.Context, projectID string) (predicted int, total int, err error) {
	err = r.db.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(CASE WHEN was_predicted = 1 THEN 1 ELSE 0 END), 0), COUNT(*)
		FROM conflict_history
		WHERE project_id = ?`, projectID).Scan(&predicted, &total)
	return
}

// --- Execution Order Recommendations ---

func (r *CollisionRepo) CreateRecommendation(ctx context.Context, rec *models.ExecutionOrderRecommendation) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO execution_order_recommendations (project_id, task_ids, reasoning, conflict_count, batch_groups, status, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at`,
		rec.ProjectID, rec.TaskIDs, rec.Reasoning, rec.ConflictCount, rec.BatchGroups, rec.Status, rec.ExpiresAt,
	).Scan(&rec.ID, &rec.CreatedAt)
}

func (r *CollisionRepo) GetLatestRecommendation(ctx context.Context, projectID string) (*models.ExecutionOrderRecommendation, error) {
	var rec models.ExecutionOrderRecommendation
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, task_ids, reasoning, conflict_count, batch_groups, status, created_at, expires_at
		FROM execution_order_recommendations
		WHERE project_id = ? AND status = 'pending' AND expires_at > datetime('now')
		ORDER BY created_at DESC LIMIT 1`, projectID).Scan(
		&rec.ID, &rec.ProjectID, &rec.TaskIDs, &rec.Reasoning, &rec.ConflictCount, &rec.BatchGroups, &rec.Status, &rec.CreatedAt, &rec.ExpiresAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rec, nil
}

func (r *CollisionRepo) UpdateRecommendationStatus(ctx context.Context, id string, status models.RecommendationStatus) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE execution_order_recommendations SET status = ? WHERE id = ?`, status, id)
	return err
}

// ExpireOldRecommendations marks expired recommendations
func (r *CollisionRepo) ExpireOldRecommendations(ctx context.Context) (int64, error) {
	result, err := r.db.ExecContext(ctx, `
		UPDATE execution_order_recommendations SET status = 'expired'
		WHERE status = 'pending' AND expires_at <= datetime('now')`)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}
