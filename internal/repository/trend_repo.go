package repository

import (
	"context"
	"database/sql"

	"github.com/openvibely/openvibely/internal/models"
)

// TrendRepo handles trend intelligence data storage.
type TrendRepo struct {
	db *sql.DB
}

func NewTrendRepo(db *sql.DB) *TrendRepo {
	return &TrendRepo{db: db}
}

// --- X Credentials ---

// GetXCredentials returns X API credentials for a project.
func (r *TrendRepo) GetXCredentials(ctx context.Context, projectID string) (*models.XCredentials, error) {
	var c models.XCredentials
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, api_key, api_secret, access_token, access_token_secret, bearer_token, created_at, updated_at
		FROM x_credentials WHERE project_id = ?`, projectID,
	).Scan(&c.ID, &c.ProjectID, &c.APIKey, &c.APISecret, &c.AccessToken, &c.AccessTokenSecret, &c.BearerToken, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &c, nil
}

// UpsertXCredentials creates or updates X API credentials.
func (r *TrendRepo) UpsertXCredentials(ctx context.Context, c *models.XCredentials) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO x_credentials (project_id, api_key, api_secret, access_token, access_token_secret, bearer_token)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			api_key = excluded.api_key,
			api_secret = excluded.api_secret,
			access_token = excluded.access_token,
			access_token_secret = excluded.access_token_secret,
			bearer_token = excluded.bearer_token,
			updated_at = datetime('now')
		RETURNING id, created_at, updated_at`,
		c.ProjectID, c.APIKey, c.APISecret, c.AccessToken, c.AccessTokenSecret, c.BearerToken,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}

// --- Trend Sources ---

// CreateSource creates a new trend source.
func (r *TrendRepo) CreateSource(ctx context.Context, s *models.TrendSource) error {
	var enabled int
	if s.Enabled {
		enabled = 1
	}
	return r.db.QueryRowContext(ctx, `
		INSERT INTO trend_sources (project_id, source_type, value, enabled)
		VALUES (?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		s.ProjectID, string(s.SourceType), s.Value, enabled,
	).Scan(&s.ID, &s.CreatedAt, &s.UpdatedAt)
}

// ListSources returns all trend sources for a project.
func (r *TrendRepo) ListSources(ctx context.Context, projectID string) ([]models.TrendSource, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, source_type, value, enabled, created_at, updated_at
		FROM trend_sources WHERE project_id = ? ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []models.TrendSource
	for rows.Next() {
		var s models.TrendSource
		var enabled int
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.SourceType, &s.Value, &enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.Enabled = enabled != 0
		sources = append(sources, s)
	}
	return sources, rows.Err()
}

// ListEnabledSources returns only enabled trend sources for a project.
func (r *TrendRepo) ListEnabledSources(ctx context.Context, projectID string) ([]models.TrendSource, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, source_type, value, enabled, created_at, updated_at
		FROM trend_sources WHERE project_id = ? AND enabled = 1 ORDER BY created_at DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sources []models.TrendSource
	for rows.Next() {
		var s models.TrendSource
		var enabled int
		if err := rows.Scan(&s.ID, &s.ProjectID, &s.SourceType, &s.Value, &enabled, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		s.Enabled = enabled != 0
		sources = append(sources, s)
	}
	return sources, rows.Err()
}

// DeleteSource deletes a trend source.
func (r *TrendRepo) DeleteSource(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM trend_sources WHERE id = ?`, id)
	return err
}

// ToggleSource enables or disables a trend source.
func (r *TrendRepo) ToggleSource(ctx context.Context, id string, enabled bool) error {
	e := 0
	if enabled {
		e = 1
	}
	_, err := r.db.ExecContext(ctx, `UPDATE trend_sources SET enabled = ?, updated_at = datetime('now') WHERE id = ?`, e, id)
	return err
}

// --- Trend Entries ---

// CreateEntry creates a new trend entry.
func (r *TrendRepo) CreateEntry(ctx context.Context, e *models.TrendEntry) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO trend_entries (project_id, source_id, source_type, content, author, url, engagement_score, sentiment, raw_data)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, collected_at`,
		e.ProjectID, e.SourceID, e.SourceType, e.Content, e.Author, e.URL, e.EngagementScore, string(e.Sentiment), e.RawData,
	).Scan(&e.ID, &e.CollectedAt)
}

// ListRecentEntries returns the most recent trend entries for a project.
func (r *TrendRepo) ListRecentEntries(ctx context.Context, projectID string, limit int) ([]models.TrendEntry, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, source_id, source_type, content, author, url, engagement_score, sentiment, collected_at, raw_data
		FROM trend_entries WHERE project_id = ? ORDER BY collected_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []models.TrendEntry
	for rows.Next() {
		var e models.TrendEntry
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.SourceID, &e.SourceType, &e.Content, &e.Author, &e.URL, &e.EngagementScore, &e.Sentiment, &e.CollectedAt, &e.RawData); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// CountEntries returns the total number of trend entries for a project.
func (r *TrendRepo) CountEntries(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trend_entries WHERE project_id = ?`, projectID).Scan(&count)
	return count, err
}

// --- Trend Patterns ---

// CreatePattern creates a new trend pattern.
func (r *TrendRepo) CreatePattern(ctx context.Context, p *models.TrendPattern) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO trend_patterns (project_id, pattern_type, title, description, evidence, confidence, signal_count, status, led_to_feature)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, first_seen, last_seen, created_at, updated_at`,
		p.ProjectID, string(p.PatternType), p.Title, p.Description, p.Evidence, p.Confidence, p.SignalCount, string(p.Status), p.LedToFeature,
	).Scan(&p.ID, &p.FirstSeen, &p.LastSeen, &p.CreatedAt, &p.UpdatedAt)
}

// ListActivePatterns returns active trend patterns for a project.
func (r *TrendRepo) ListActivePatterns(ctx context.Context, projectID string) ([]models.TrendPattern, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, pattern_type, title, description, evidence, confidence, signal_count, first_seen, last_seen, status, led_to_feature, created_at, updated_at
		FROM trend_patterns WHERE project_id = ? AND status = 'active' ORDER BY confidence DESC, signal_count DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patterns []models.TrendPattern
	for rows.Next() {
		var p models.TrendPattern
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.PatternType, &p.Title, &p.Description, &p.Evidence, &p.Confidence, &p.SignalCount, &p.FirstSeen, &p.LastSeen, &p.Status, &p.LedToFeature, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		patterns = append(patterns, p)
	}
	return patterns, rows.Err()
}

// ListAllPatterns returns all trend patterns for a project.
func (r *TrendRepo) ListAllPatterns(ctx context.Context, projectID string) ([]models.TrendPattern, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, pattern_type, title, description, evidence, confidence, signal_count, first_seen, last_seen, status, led_to_feature, created_at, updated_at
		FROM trend_patterns WHERE project_id = ? ORDER BY last_seen DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var patterns []models.TrendPattern
	for rows.Next() {
		var p models.TrendPattern
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.PatternType, &p.Title, &p.Description, &p.Evidence, &p.Confidence, &p.SignalCount, &p.FirstSeen, &p.LastSeen, &p.Status, &p.LedToFeature, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		patterns = append(patterns, p)
	}
	return patterns, rows.Err()
}

// UpdatePatternStatus updates the status of a trend pattern.
func (r *TrendRepo) UpdatePatternStatus(ctx context.Context, id string, status models.TrendPatternStatus, ledToFeature string) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE trend_patterns SET status = ?, led_to_feature = ?, updated_at = datetime('now') WHERE id = ?`,
		string(status), ledToFeature, id)
	return err
}

// ExistingPattern checks if a pattern with the same title and type already exists for the project (active only).
func (r *TrendRepo) ExistingPattern(ctx context.Context, projectID, title string, patternType models.TrendPatternType) (*models.TrendPattern, error) {
	var p models.TrendPattern
	err := r.db.QueryRowContext(ctx, `
		SELECT id, project_id, pattern_type, title, description, evidence, confidence, signal_count, first_seen, last_seen, status, led_to_feature, created_at, updated_at
		FROM trend_patterns WHERE project_id = ? AND title = ? AND pattern_type = ? AND status = 'active'`,
		projectID, title, string(patternType),
	).Scan(&p.ID, &p.ProjectID, &p.PatternType, &p.Title, &p.Description, &p.Evidence, &p.Confidence, &p.SignalCount, &p.FirstSeen, &p.LastSeen, &p.Status, &p.LedToFeature, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// BumpPattern updates signal count and last_seen for an existing pattern.
func (r *TrendRepo) BumpPattern(ctx context.Context, id string, additionalSignals int) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE trend_patterns SET signal_count = signal_count + ?, last_seen = datetime('now'), updated_at = datetime('now') WHERE id = ?`,
		additionalSignals, id)
	return err
}

// CountActivePatterns returns the number of active patterns for a project.
func (r *TrendRepo) CountActivePatterns(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trend_patterns WHERE project_id = ? AND status = 'active'`, projectID).Scan(&count)
	return count, err
}

// CountImplementedPatterns returns the number of patterns that led to features.
func (r *TrendRepo) CountImplementedPatterns(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trend_patterns WHERE project_id = ? AND status = 'implemented'`, projectID).Scan(&count)
	return count, err
}

// --- Competitor Updates ---

// CreateCompetitorUpdate creates a new competitor update.
func (r *TrendRepo) CreateCompetitorUpdate(ctx context.Context, u *models.CompetitorUpdate) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO competitor_updates (project_id, competitor_name, update_type, title, description, source_url, impact_assessment, relevance_score)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, detected_at`,
		u.ProjectID, u.CompetitorName, string(u.UpdateType), u.Title, u.Description, u.SourceURL, u.ImpactAssessment, u.RelevanceScore,
	).Scan(&u.ID, &u.DetectedAt)
}

// ListRecentCompetitorUpdates returns recent competitor updates for a project.
func (r *TrendRepo) ListRecentCompetitorUpdates(ctx context.Context, projectID string, limit int) ([]models.CompetitorUpdate, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, project_id, competitor_name, update_type, title, description, source_url, impact_assessment, relevance_score, detected_at
		FROM competitor_updates WHERE project_id = ? ORDER BY detected_at DESC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var updates []models.CompetitorUpdate
	for rows.Next() {
		var u models.CompetitorUpdate
		if err := rows.Scan(&u.ID, &u.ProjectID, &u.CompetitorName, &u.UpdateType, &u.Title, &u.Description, &u.SourceURL, &u.ImpactAssessment, &u.RelevanceScore, &u.DetectedAt); err != nil {
			return nil, err
		}
		updates = append(updates, u)
	}
	return updates, rows.Err()
}

// CountCompetitorUpdates returns the total number of competitor updates for a project.
func (r *TrendRepo) CountCompetitorUpdates(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM competitor_updates WHERE project_id = ?`, projectID).Scan(&count)
	return count, err
}

// CountSources returns the number of trend sources for a project.
func (r *TrendRepo) CountSources(ctx context.Context, projectID string) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM trend_sources WHERE project_id = ?`, projectID).Scan(&count)
	return count, err
}
