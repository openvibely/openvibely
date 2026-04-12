package repository

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

// WebhookRepo manages webhook endpoint CRUD and agent assignments.
type WebhookRepo struct {
	db *sql.DB
}

func NewWebhookRepo(db *sql.DB) *WebhookRepo {
	return &WebhookRepo{db: db}
}

// GenerateToken creates a cryptographically random URL-safe token.
func GenerateToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// GenerateSecret creates a random secret for webhook signing.
func GenerateSecret() (string, error) {
	return GenerateToken(32)
}

func scanWebhookEndpoint(row interface{ Scan(dest ...any) error }) (*models.WebhookEndpoint, error) {
	var w models.WebhookEndpoint
	var enabled int
	err := row.Scan(&w.ID, &w.ProjectID, &w.Name, &enabled, &w.PathToken, &w.Secret,
		&w.SystemInstructions, &w.TitleTemplate, &w.PromptTemplate, &w.DefaultPriority,
		&w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return nil, err
	}
	w.Enabled = enabled != 0
	return &w, nil
}

const webhookColumns = `id, project_id, name, enabled, path_token, secret, system_instructions, title_template, prompt_template, default_priority, created_at, updated_at`

func (r *WebhookRepo) Create(ctx context.Context, w *models.WebhookEndpoint) error {
	if w.PathToken == "" {
		tok, err := GenerateToken(16)
		if err != nil {
			return fmt.Errorf("generating path token: %w", err)
		}
		w.PathToken = tok
	}
	if w.Secret == "" {
		sec, err := GenerateSecret()
		if err != nil {
			return fmt.Errorf("generating secret: %w", err)
		}
		w.Secret = sec
	}
	enabled := 0
	if w.Enabled {
		enabled = 1
	}
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO webhook_endpoints (project_id, name, enabled, path_token, secret, system_instructions, title_template, prompt_template, default_priority)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, created_at, updated_at`,
		w.ProjectID, w.Name, enabled, w.PathToken, w.Secret,
		w.SystemInstructions, w.TitleTemplate, w.PromptTemplate, w.DefaultPriority,
	).Scan(&w.ID, &w.CreatedAt, &w.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating webhook endpoint: %w", err)
	}
	return nil
}

func (r *WebhookRepo) GetByID(ctx context.Context, id string) (*models.WebhookEndpoint, error) {
	w, err := scanWebhookEndpoint(r.db.QueryRowContext(ctx,
		`SELECT `+webhookColumns+` FROM webhook_endpoints WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting webhook endpoint: %w", err)
	}
	return w, nil
}

func (r *WebhookRepo) GetByPathToken(ctx context.Context, pathToken string) (*models.WebhookEndpoint, error) {
	w, err := scanWebhookEndpoint(r.db.QueryRowContext(ctx,
		`SELECT `+webhookColumns+` FROM webhook_endpoints WHERE path_token = ?`, pathToken))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting webhook endpoint by path token: %w", err)
	}
	return w, nil
}

func (r *WebhookRepo) ListByProject(ctx context.Context, projectID string) ([]models.WebhookEndpoint, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+webhookColumns+` FROM webhook_endpoints WHERE project_id = ? ORDER BY name ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing webhook endpoints: %w", err)
	}
	defer rows.Close()

	var endpoints []models.WebhookEndpoint
	for rows.Next() {
		w, err := scanWebhookEndpoint(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning webhook endpoint: %w", err)
		}
		endpoints = append(endpoints, *w)
	}
	return endpoints, rows.Err()
}

func (r *WebhookRepo) Update(ctx context.Context, w *models.WebhookEndpoint) error {
	enabled := 0
	if w.Enabled {
		enabled = 1
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE webhook_endpoints SET name = ?, enabled = ?, system_instructions = ?,
		 title_template = ?, prompt_template = ?, default_priority = ?,
		 updated_at = datetime('now')
		 WHERE id = ?`,
		w.Name, enabled, w.SystemInstructions,
		w.TitleTemplate, w.PromptTemplate, w.DefaultPriority, w.ID)
	if err != nil {
		return fmt.Errorf("updating webhook endpoint: %w", err)
	}
	return nil
}

func (r *WebhookRepo) Delete(ctx context.Context, id string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM webhook_endpoints WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting webhook endpoint: %w", err)
	}
	return nil
}

// RotateSecret generates a new secret for an endpoint and returns it.
func (r *WebhookRepo) RotateSecret(ctx context.Context, id string) (string, error) {
	newSecret, err := GenerateSecret()
	if err != nil {
		return "", fmt.Errorf("generating new secret: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE webhook_endpoints SET secret = ?, updated_at = datetime('now') WHERE id = ?`,
		newSecret, id)
	if err != nil {
		return "", fmt.Errorf("rotating webhook secret: %w", err)
	}
	return newSecret, nil
}

// --- Webhook endpoint agent assignments ---

func (r *WebhookRepo) SetEndpointAgents(ctx context.Context, endpointID string, agentIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM webhook_endpoint_agents WHERE webhook_endpoint_id = ?`, endpointID); err != nil {
		return fmt.Errorf("clearing endpoint agents: %w", err)
	}
	for i, agentID := range agentIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO webhook_endpoint_agents (webhook_endpoint_id, agent_definition_id, position) VALUES (?, ?, ?)`,
			endpointID, agentID, i); err != nil {
			return fmt.Errorf("inserting endpoint agent: %w", err)
		}
	}
	return tx.Commit()
}

func (r *WebhookRepo) GetEndpointAgents(ctx context.Context, endpointID string) ([]models.WebhookEndpointAgent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT webhook_endpoint_id, agent_definition_id, position FROM webhook_endpoint_agents
		 WHERE webhook_endpoint_id = ? ORDER BY position ASC`, endpointID)
	if err != nil {
		return nil, fmt.Errorf("listing endpoint agents: %w", err)
	}
	defer rows.Close()

	var agents []models.WebhookEndpointAgent
	for rows.Next() {
		var a models.WebhookEndpointAgent
		if err := rows.Scan(&a.WebhookEndpointID, &a.AgentDefinitionID, &a.Position); err != nil {
			return nil, fmt.Errorf("scanning endpoint agent: %w", err)
		}
		agents = append(agents, a)
	}
	return agents, rows.Err()
}

// --- Task agent assignments (future multi-agent) ---

func (r *WebhookRepo) SetTaskAgentAssignments(ctx context.Context, taskID string, agentIDs []string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM task_agent_assignments WHERE task_id = ?`, taskID); err != nil {
		return fmt.Errorf("clearing task agent assignments: %w", err)
	}
	for i, agentID := range agentIDs {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO task_agent_assignments (task_id, agent_definition_id, position) VALUES (?, ?, ?)`,
			taskID, agentID, i); err != nil {
			return fmt.Errorf("inserting task agent assignment: %w", err)
		}
	}
	return tx.Commit()
}

func (r *WebhookRepo) GetTaskAgentAssignments(ctx context.Context, taskID string) ([]models.TaskAgentAssignment, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT task_id, agent_definition_id, position FROM task_agent_assignments
		 WHERE task_id = ? ORDER BY position ASC`, taskID)
	if err != nil {
		return nil, fmt.Errorf("listing task agent assignments: %w", err)
	}
	defer rows.Close()

	var assignments []models.TaskAgentAssignment
	for rows.Next() {
		var a models.TaskAgentAssignment
		if err := rows.Scan(&a.TaskID, &a.AgentDefinitionID, &a.Position); err != nil {
			return nil, fmt.Errorf("scanning task agent assignment: %w", err)
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}
