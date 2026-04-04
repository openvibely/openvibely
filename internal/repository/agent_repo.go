package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

type AgentRepo struct {
	db *sql.DB
}

func NewAgentRepo(db *sql.DB) *AgentRepo {
	return &AgentRepo{db: db}
}

const agentColumns = `id, name, description, system_prompt, model, tools, plugins, mcp_servers, skills, created_at, updated_at`

func scanAgent(row interface{ Scan(dest ...any) error }) (*models.Agent, error) {
	var a models.Agent
	var toolsJSON, pluginsJSON, mcpJSON, skillsJSON string
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.SystemPrompt,
		&a.Model, &toolsJSON, &pluginsJSON, &mcpJSON, &skillsJSON,
		&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if toolsJSON != "" && toolsJSON != "[]" {
		if err := json.Unmarshal([]byte(toolsJSON), &a.Tools); err != nil {
			return nil, fmt.Errorf("unmarshaling tools: %w", err)
		}
	}
	if a.Tools == nil {
		a.Tools = []string{}
	}
	if pluginsJSON != "" && pluginsJSON != "[]" {
		if err := json.Unmarshal([]byte(pluginsJSON), &a.Plugins); err != nil {
			return nil, fmt.Errorf("unmarshaling plugins: %w", err)
		}
	}
	if a.Plugins == nil {
		a.Plugins = []string{}
	}
	if mcpJSON != "" && mcpJSON != "[]" {
		if err := json.Unmarshal([]byte(mcpJSON), &a.MCPServers); err != nil {
			return nil, fmt.Errorf("unmarshaling mcp_servers: %w", err)
		}
	}
	if a.MCPServers == nil {
		a.MCPServers = []models.MCPServerConfig{}
	}
	if skillsJSON != "" && skillsJSON != "[]" {
		if err := json.Unmarshal([]byte(skillsJSON), &a.Skills); err != nil {
			return nil, fmt.Errorf("unmarshaling skills: %w", err)
		}
	}
	if a.Skills == nil {
		a.Skills = []models.SkillConfig{}
	}
	return &a, nil
}

func marshalJSON(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (r *AgentRepo) List(ctx context.Context) ([]models.Agent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+agentColumns+` FROM agents ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing agents: %w", err)
	}
	defer rows.Close()

	var agents []models.Agent
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning agent: %w", err)
		}
		agents = append(agents, *a)
	}
	return agents, rows.Err()
}

func (r *AgentRepo) GetByID(ctx context.Context, id string) (*models.Agent, error) {
	a, err := scanAgent(r.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent: %w", err)
	}
	return a, nil
}

func (r *AgentRepo) GetByName(ctx context.Context, name string) (*models.Agent, error) {
	a, err := scanAgent(r.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE LOWER(name) = LOWER(?)`, name))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent by name: %w", err)
	}
	return a, nil
}

func (r *AgentRepo) Create(ctx context.Context, a *models.Agent) error {
	toolsJSON, err := marshalJSON(a.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}
	pluginsJSON, err := marshalJSON(a.Plugins)
	if err != nil {
		return fmt.Errorf("marshaling plugins: %w", err)
	}
	mcpJSON, err := marshalJSON(a.MCPServers)
	if err != nil {
		return fmt.Errorf("marshaling mcp_servers: %w", err)
	}
	skillsJSON, err := marshalJSON(a.Skills)
	if err != nil {
		return fmt.Errorf("marshaling skills: %w", err)
	}
	err = r.db.QueryRowContext(ctx,
		`INSERT INTO agents (id, name, description, system_prompt, model, tools, plugins, mcp_servers, skills)
		 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, created_at, updated_at`,
		a.Name, a.Description, a.SystemPrompt, a.Model,
		toolsJSON, pluginsJSON, mcpJSON, skillsJSON).
		Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating agent: %w", err)
	}
	return nil
}

func (r *AgentRepo) Update(ctx context.Context, a *models.Agent) error {
	toolsJSON, err := marshalJSON(a.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}
	pluginsJSON, err := marshalJSON(a.Plugins)
	if err != nil {
		return fmt.Errorf("marshaling plugins: %w", err)
	}
	mcpJSON, err := marshalJSON(a.MCPServers)
	if err != nil {
		return fmt.Errorf("marshaling mcp_servers: %w", err)
	}
	skillsJSON, err := marshalJSON(a.Skills)
	if err != nil {
		return fmt.Errorf("marshaling skills: %w", err)
	}
	_, err = r.db.ExecContext(ctx,
		`UPDATE agents SET name = ?, description = ?, system_prompt = ?,
		 model = ?, tools = ?, plugins = ?, mcp_servers = ?, skills = ?,
		 updated_at = datetime('now')
		 WHERE id = ?`,
		a.Name, a.Description, a.SystemPrompt, a.Model,
		toolsJSON, pluginsJSON, mcpJSON, skillsJSON, a.ID)
	if err != nil {
		return fmt.Errorf("updating agent: %w", err)
	}
	return nil
}

func (r *AgentRepo) Delete(ctx context.Context, id string) error {
	// Nullify FK references in tasks before deleting
	if _, err := r.db.ExecContext(ctx, `UPDATE tasks SET agent_definition_id = NULL WHERE agent_definition_id = ?`, id); err != nil {
		return fmt.Errorf("nullifying agent in tasks: %w", err)
	}
	_, err := r.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("deleting agent: %w", err)
	}
	return nil
}

func (r *AgentRepo) GetByIDs(ctx context.Context, ids []string) (map[string]*models.Agent, error) {
	if len(ids) == 0 {
		return map[string]*models.Agent{}, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE id IN (`+strings.Join(placeholders, ",")+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("batch getting agents: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*models.Agent, len(ids))
	for rows.Next() {
		a, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning agent: %w", err)
		}
		result[a.ID] = a
	}
	return result, rows.Err()
}
