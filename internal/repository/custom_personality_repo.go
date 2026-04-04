package repository

import (
	"context"
	"database/sql"
	"fmt"

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
	return r.db.QueryRowContext(ctx,
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

// List returns all custom personalities ordered by name.
func (r *CustomPersonalityRepo) List(ctx context.Context) ([]models.CustomPersonality, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, key, description, system_prompt, created_at, updated_at
		 FROM custom_personalities
		 ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list custom personalities: %w", err)
	}
	defer rows.Close()

	var personalities []models.CustomPersonality
	for rows.Next() {
		var p models.CustomPersonality
		if err := rows.Scan(&p.ID, &p.Name, &p.Key, &p.Description, &p.SystemPrompt, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan custom personality: %w", err)
		}
		personalities = append(personalities, p)
	}
	return personalities, rows.Err()
}

// Update modifies an existing custom personality identified by key.
func (r *CustomPersonalityRepo) Update(ctx context.Context, key string, p *models.CustomPersonality) error {
	result, err := r.db.ExecContext(ctx,
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
func (r *CustomPersonalityRepo) Delete(ctx context.Context, key string) error {
	result, err := r.db.ExecContext(ctx,
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
