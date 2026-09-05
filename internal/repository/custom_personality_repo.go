package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// CustomPersonalityRepo handles database operations for custom personalities.
type CustomPersonalityRepo struct {
	db *sql.DB
}

// NewCustomPersonalityRepo creates a new CustomPersonalityRepo.
func NewCustomPersonalityRepo(db *sql.DB) *CustomPersonalityRepo {
	return &CustomPersonalityRepo{db: db}
}

// Create inserts a new custom personality.
func (r *CustomPersonalityRepo) Create(ctx context.Context, p *models.CustomPersonality) error {
	return queryRowBoundSQLite(ctx, r.db,
		`INSERT INTO custom_personalities (name, key, description, system_prompt)
		 VALUES (?, ?, ?, ?)
		 RETURNING id, created_at, updated_at`,
		p.Name, p.Key, p.Description, p.SystemPrompt).
		Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
}

// GetByKey returns a custom personality by its unique key.
func (r *CustomPersonalityRepo) GetByKey(ctx context.Context, key string) (*models.CustomPersonality, error) {
	var p models.CustomPersonality
	err := r.db.QueryRowContext(ctx,
		`SELECT id, name, key, description, system_prompt, created_at, updated_at
		 FROM custom_personalities WHERE key = ?`, key).
		Scan(&p.ID, &p.Name, &p.Key, &p.Description, &p.SystemPrompt, &p.CreatedAt, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get custom personality by key: %w", err)
	}
	return &p, nil
}

const customPersonalityPromptPreviewLength = 150

// List returns custom personalities ordered by name using a compact card/list projection.
// SystemPrompt is intentionally left empty; callers that need the full prompt must use GetByKey.
func (r *CustomPersonalityRepo) List(ctx context.Context) ([]models.CustomPersonality, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, key, description, SUBSTR(system_prompt, 1, ?) AS system_prompt_preview, created_at, updated_at
		 FROM custom_personalities
		 ORDER BY name ASC`, customPersonalityPromptPreviewLength)
	if err != nil {
		return nil, fmt.Errorf("list custom personalities: %w", err)
	}
	defer rows.Close()

	var personalities []models.CustomPersonality
	for rows.Next() {
		var p models.CustomPersonality
		if err := rows.Scan(&p.ID, &p.Name, &p.Key, &p.Description, &p.SystemPromptPreview, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan custom personality: %w", err)
		}
		personalities = append(personalities, p)
	}
	return personalities, rows.Err()
}

// ListCardsByKeys returns compact card projections for fixed Personality context,
// such as preset overrides and the currently selected custom personality. The
// caller supplies a small key set; full prompts remain available only via GetByKey.
func (r *CustomPersonalityRepo) ListCardsByKeys(ctx context.Context, keys []string) ([]models.CustomPersonality, error) {
	normalized := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, key)
	}
	if len(normalized) == 0 {
		return []models.CustomPersonality{}, nil
	}
	placeholders := make([]string, len(normalized))
	args := make([]any, 0, len(normalized)+1)
	args = append(args, customPersonalityPromptPreviewLength)
	for i, key := range normalized {
		placeholders[i] = "?"
		args = append(args, key)
	}
	query := `SELECT id, name, key, description, SUBSTR(system_prompt, 1, ?) AS system_prompt_preview, created_at, updated_at
		FROM custom_personalities WHERE key IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY name ASC, id ASC`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list fixed custom personality cards: %w", err)
	}
	defer rows.Close()

	personalities := make([]models.CustomPersonality, 0, len(normalized))
	for rows.Next() {
		var p models.CustomPersonality
		if err := rows.Scan(&p.ID, &p.Name, &p.Key, &p.Description, &p.SystemPromptPreview, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fixed custom personality card: %w", err)
		}
		personalities = append(personalities, p)
	}
	return personalities, rows.Err()
}

// ListPage returns one bounded card projection. Search covers the metadata and
// prompt preview exposed by the Personality card.
func (r *CustomPersonalityRepo) ListPage(ctx context.Context, limit, offset int, search string) ([]models.CustomPersonality, error) {
	return r.listPage(ctx, limit, offset, search, nil)
}

// ListPageExcludingKeys returns a bounded visible-card page while excluding
// preset override rows that are rendered in the fixed built-in card section.
func (r *CustomPersonalityRepo) ListPageExcludingKeys(ctx context.Context, limit, offset int, search string, excludedKeys []string) ([]models.CustomPersonality, error) {
	return r.listPage(ctx, limit, offset, search, excludedKeys)
}

type CustomPersonalityPageFilter struct {
	Search   string
	Active   *bool
	Selected string
	Sort     string
}

func (r *CustomPersonalityRepo) ListPageExcludingKeysFiltered(ctx context.Context, limit, offset int, filter CustomPersonalityPageFilter, excludedKeys []string) ([]models.CustomPersonality, error) {
	return r.listPageFiltered(ctx, limit, offset, filter, excludedKeys)
}

func (r *CustomPersonalityRepo) listPage(ctx context.Context, limit, offset int, search string, excludedKeys []string) ([]models.CustomPersonality, error) {
	return r.listPageFiltered(ctx, limit, offset, CustomPersonalityPageFilter{Search: search}, excludedKeys)
}

func (r *CustomPersonalityRepo) listPageFiltered(ctx context.Context, limit, offset int, filter CustomPersonalityPageFilter, excludedKeys []string) ([]models.CustomPersonality, error) {
	limit, offset = normalizeCardPageArgs(limit, offset)
	query := `SELECT id, name, key, description, SUBSTR(system_prompt, 1, ?) AS system_prompt_preview, created_at, updated_at
		FROM custom_personalities`
	args := []any{customPersonalityPromptPreviewLength}

	normalizedExcluded := make([]string, 0, len(excludedKeys))
	seenExcluded := make(map[string]struct{}, len(excludedKeys))
	for _, key := range excludedKeys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if _, exists := seenExcluded[key]; exists {
			continue
		}
		seenExcluded[key] = struct{}{}
		normalizedExcluded = append(normalizedExcluded, key)
	}
	if len(normalizedExcluded) > 0 {
		placeholders := make([]string, len(normalizedExcluded))
		for i, key := range normalizedExcluded {
			placeholders[i] = "?"
			args = append(args, key)
		}
		query += ` WHERE key NOT IN (` + strings.Join(placeholders, ",") + `)`
	}
	activeCondition := filter.Active != nil && strings.TrimSpace(filter.Selected) != ""
	if activeCondition {
		if len(normalizedExcluded) == 0 {
			query += ` WHERE`
		} else {
			query += ` AND`
		}
		if *filter.Active {
			query += ` key = ?`
		} else {
			query += ` key != ?`
		}
		args = append(args, strings.TrimSpace(filter.Selected))
	}
	if search := strings.TrimSpace(filter.Search); search != "" {
		if len(normalizedExcluded) == 0 && !activeCondition {
			query += ` WHERE`
		} else {
			query += ` AND`
		}
		query += ` INSTR(LOWER(
			COALESCE(name, '') || ' ' || COALESCE(description, '') || ' ' ||
			COALESCE(SUBSTR(system_prompt, 1, 150), '')
		), ?) > 0`
		args = append(args, strings.ToLower(search))
	}
	if filter.Sort == "name_desc" {
		query += ` ORDER BY name DESC, id DESC`
	} else {
		query += ` ORDER BY name ASC, id ASC`
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list custom personality page: %w", err)
	}
	defer rows.Close()

	personalities := make([]models.CustomPersonality, 0, limit)
	for rows.Next() {
		var p models.CustomPersonality
		if err := rows.Scan(&p.ID, &p.Name, &p.Key, &p.Description, &p.SystemPromptPreview, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan custom personality page: %w", err)
		}
		personalities = append(personalities, p)
	}
	return personalities, rows.Err()
}

// Update modifies an existing custom personality identified by key.
func (r *CustomPersonalityRepo) Update(ctx context.Context, key string, p *models.CustomPersonality) error {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE custom_personalities SET name = ?, description = ?, system_prompt = ?, updated_at = datetime('now')
		 WHERE key = ?`,
		p.Name, p.Description, p.SystemPrompt, key)
	if err != nil {
		return fmt.Errorf("update custom personality: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("custom personality not found")
	}
	return nil
}

// Delete removes a custom personality by key.
func (r *CustomPersonalityRepo) DeleteBulk(ctx context.Context, keys []string) error {
	if len(keys) == 0 {
		return fmt.Errorf("at least one personality is required")
	}
	conn, finish, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return err
	}
	defer finish()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(keys)), ",")
	args := make([]any, len(keys))
	for i := range keys {
		args[i] = keys[i]
	}
	var count int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM custom_personalities WHERE key IN (`+placeholders+`)`, args...).Scan(&count); err != nil {
		return err
	}
	if count != len(keys) {
		return fmt.Errorf("custom personality not found")
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM custom_personalities WHERE key IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func (r *CustomPersonalityRepo) Delete(ctx context.Context, key string) error {
	result, err := execBoundSQLite(ctx, r.db,
		`DELETE FROM custom_personalities WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("delete custom personality: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("custom personality not found")
	}
	return nil
}
