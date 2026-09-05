package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type AgentRepo struct {
	db *sql.DB
}

var (
	ErrAgentNameRequired                = errors.New("agent name is required")
	ErrSelectableAgentNameAlreadyExists = errors.New("enabled selectable primary agent name already exists")
)

func NewAgentRepo(db *sql.DB) *AgentRepo {
	return &AgentRepo{db: db}
}

const agentColumns = `id, name, description, system_prompt, model, tools, tool_config, plugins, mcp_servers, skills, system_kind, ` +
	`COALESCE(key, ''), COALESCE(scope, 'global'), project_id, ` +
	`COALESCE(selectable_as_primary, 1), COALESCE(enabled, 1), ` +
	`COALESCE(permission_defaults_json, '{}'), ` +
	`COALESCE(model_defaults_json, '{}'), COALESCE(created_by, 'user'), ` +
	`COALESCE(generated_status, 'user_edited'), absorbed_into, ` +
	`COALESCE(source_refs_json, '[]'), archived_at, ` +
	`created_at, updated_at`

// AgentRuntimeSummary is the compact projection needed by runtime list_agents.
type AgentRuntimeSummary struct {
	Name           string
	Description    string
	Model          string
	SkillCount     int
	MCPServerCount int
}

// AgentPickerOption is the compact projection needed by settings pickers that
// render only an agent identifier and display name.
type AgentPickerOption struct {
	ID   string
	Name string
}

// AgentScheduleOption is the compact projection needed by the Schedule primary
// Agent selector. Availability is enforced by ListScheduleOptions; this type
// contains only the display fields rendered by the selector.
type AgentScheduleOption struct {
	ID    string
	Name  string
	Model string
}

// AgentSelectableReference is the compact identity and project-scope projection
// used when validating Automation Agent references.
type AgentSelectableReference struct {
	ID        string
	Key       string
	ProjectID string
}

// AgentSkillCatalogRef is the compact projection needed to discover agent-owned
// skill catalogs without hydrating prompt/config/plugin/skill JSON fields.
type AgentSkillCatalogRef struct {
	ID        string
	Key       string
	ProjectID string
}

// AgentListSummary is the compact projection needed by the lifecycle agent_list
// inspector. It excludes full prompt, model, tool, plugin, MCP, permission,
// model-default, source-reference, and timestamp fields. Skill names are
// extracted in SQL so skill descriptions and content are never decoded here.
type AgentListSummary struct {
	Key                 string
	Name                string
	Description         string
	Scope               models.AgentScope
	SelectableAsPrimary bool
	Enabled             bool
	GeneratedStatus     models.AgentGeneratedStatus
	SystemKind          string
	ArchivedAt          *time.Time
	AttachedSkillNames  []string
}

const agentScheduleOptionColumns = `id, name, model`
const agentSelectableReferenceColumns = `id, COALESCE(key, ''), COALESCE(project_id, '')`

func scanAgentScheduleOption(row interface{ Scan(dest ...any) error }) (*AgentScheduleOption, error) {
	var option AgentScheduleOption
	if err := row.Scan(&option.ID, &option.Name, &option.Model); err != nil {
		return nil, err
	}
	return &option, nil
}

func scanChatAssignableAgentDefinition(row interface{ Scan(dest ...any) error }) (*models.ChatAssignableAgentDefinition, error) {
	var a models.ChatAssignableAgentDefinition
	var selectableInt, enabledInt int
	var generatedStatus string
	var archivedAt sql.NullTime
	if err := row.Scan(&a.ID, &a.Name, &a.Description, &a.Key, &a.SystemKind, &selectableInt, &enabledInt, &generatedStatus, &archivedAt); err != nil {
		return nil, err
	}
	a.SelectableAsPrimary = selectableInt != 0
	a.Enabled = enabledInt != 0
	a.GeneratedStatus = models.AgentGeneratedStatus(generatedStatus)
	if archivedAt.Valid {
		t := archivedAt.Time
		a.ArchivedAt = &t
	}
	return &a, nil
}

const agentListSummaryColumns = `COALESCE(key, ''), name, description, COALESCE(scope, 'global'), ` +
	`COALESCE(selectable_as_primary, 1), COALESCE(enabled, 1), ` +
	`COALESCE(generated_status, 'user_edited'), COALESCE(system_kind, ''), archived_at, ` +
	`COALESCE((` +
	`SELECT json_group_array(skill_name) FROM (` +
	`SELECT json_extract(skill.value, '$.name') AS skill_name ` +
	`FROM json_each(CASE WHEN TRIM(COALESCE(agents.skills, '')) = '' THEN '[]' ELSE agents.skills END) AS skill ` +
	`ORDER BY CAST(skill.key AS INTEGER)` +
	`)), '[]')`

func scanAgentListSummary(row interface{ Scan(dest ...any) error }) (*AgentListSummary, error) {
	var summary AgentListSummary
	var (
		scope, generatedStatus, skillNamesJSON string
		selectableInt, enabledInt              int
		archivedAt                             sql.NullTime
	)
	if err := row.Scan(
		&summary.Key, &summary.Name, &summary.Description, &scope,
		&selectableInt, &enabledInt, &generatedStatus, &summary.SystemKind,
		&archivedAt, &skillNamesJSON,
	); err != nil {
		return nil, err
	}
	summary.Scope = models.AgentScope(scope)
	summary.SelectableAsPrimary = selectableInt != 0
	summary.Enabled = enabledInt != 0
	summary.GeneratedStatus = models.AgentGeneratedStatus(generatedStatus)
	if archivedAt.Valid {
		t := archivedAt.Time
		summary.ArchivedAt = &t
	}
	if s := strings.TrimSpace(skillNamesJSON); s != "" && s != "[]" {
		if err := json.Unmarshal([]byte(s), &summary.AttachedSkillNames); err != nil {
			return nil, fmt.Errorf("unmarshaling agent list skill names: %w", err)
		}
	}
	if summary.AttachedSkillNames == nil {
		summary.AttachedSkillNames = []string{}
	}
	return &summary, nil
}

func scanAgent(row interface{ Scan(dest ...any) error }) (*models.Agent, error) {
	var a models.Agent
	var (
		toolsJSON, toolConfigJSON, pluginsJSON, mcpJSON, skillsJSON string
		scope, createdBy, generatedStatus                           string
		permJSON, modelDefaultsJSON, sourceRefsJSON                 string
		selectableInt, enabledInt                                   int
		projectID, absorbedInto                                     sql.NullString
		archivedAt                                                  sql.NullTime
	)
	err := row.Scan(&a.ID, &a.Name, &a.Description, &a.SystemPrompt,
		&a.Model, &toolsJSON, &toolConfigJSON, &pluginsJSON, &mcpJSON, &skillsJSON,
		&a.SystemKind,
		&a.Key, &scope, &projectID,
		&selectableInt, &enabledInt,
		&permJSON, &modelDefaultsJSON, &createdBy,
		&generatedStatus, &absorbedInto,
		&sourceRefsJSON, &archivedAt,
		&a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	a.Scope = models.AgentScope(scope)
	if projectID.Valid {
		a.ProjectID = projectID.String
	}
	a.SelectableAsPrimary = selectableInt != 0
	a.Enabled = enabledInt != 0
	a.CreatedBy = models.AgentCreatedBy(createdBy)
	a.GeneratedStatus = models.AgentGeneratedStatus(generatedStatus)
	if absorbedInto.Valid {
		a.AbsorbedInto = absorbedInto.String
	}
	if archivedAt.Valid {
		t := archivedAt.Time
		a.ArchivedAt = &t
	}
	if s := strings.TrimSpace(permJSON); s != "" && s != "{}" {
		if err := json.Unmarshal([]byte(s), &a.PermissionDefaults); err != nil {
			return nil, fmt.Errorf("unmarshaling permission_defaults: %w", err)
		}
	}
	if s := strings.TrimSpace(modelDefaultsJSON); s != "" && s != "{}" {
		if err := json.Unmarshal([]byte(s), &a.ModelDefaults); err != nil {
			return nil, fmt.Errorf("unmarshaling model_defaults: %w", err)
		}
	}
	if s := strings.TrimSpace(sourceRefsJSON); s != "" && s != "[]" {
		if err := json.Unmarshal([]byte(s), &a.SourceRefs); err != nil {
			return nil, fmt.Errorf("unmarshaling source_refs: %w", err)
		}
	}
	if a.SourceRefs == nil {
		a.SourceRefs = []string{}
	}
	if toolsJSON != "" && toolsJSON != "[]" {
		if err := json.Unmarshal([]byte(toolsJSON), &a.Tools); err != nil {
			return nil, fmt.Errorf("unmarshaling tools: %w", err)
		}
	}
	if a.Tools == nil {
		a.Tools = []string{}
	}
	if strings.TrimSpace(toolConfigJSON) != "" && strings.TrimSpace(toolConfigJSON) != "{}" {
		if err := json.Unmarshal([]byte(toolConfigJSON), &a.ToolConfig); err != nil {
			return nil, fmt.Errorf("unmarshaling tool_config: %w", err)
		}
	}
	normalizeAgentToolConfig(&a)
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

func (r *AgentRepo) normalizeAndValidateAgentName(ctx context.Context, a *models.Agent) error {
	if a == nil {
		return nil
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		return ErrAgentNameRequired
	}
	if !a.Enabled || !a.SelectableAsPrimary {
		return nil
	}

	rows, err := r.db.QueryContext(ctx, `
		SELECT id
		FROM agents
		WHERE LOWER(TRIM(name)) = LOWER(?)
		  AND id <> ?
		  AND COALESCE(enabled, 1) = 1
		  AND COALESCE(selectable_as_primary, 1) = 1
		  AND COALESCE(generated_status, 'user_edited') <> 'archived'
		LIMIT 1`, a.Name, a.ID)
	if err != nil {
		return fmt.Errorf("checking agent name uniqueness: %w", err)
	}
	defer rows.Close()
	if rows.Next() {
		return ErrSelectableAgentNameAlreadyExists
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("checking agent name uniqueness: %w", err)
	}
	return nil
}

func normalizeAgentToolConfig(a *models.Agent) {
	if a == nil {
		return
	}
	if len(a.ToolConfig.ScopedFiles) == 0 {
		a.ToolConfig.SkipDefaultTools = false
	}
}

func (r *AgentRepo) List(ctx context.Context) ([]models.Agent, error) {
	return r.list(ctx, `SELECT `+agentColumns+` FROM agents WHERE COALESCE(generated_status, 'user_edited') <> 'archived' ORDER BY name ASC`)
}

// ListScheduleOptions returns active primary-Agent options available to a
// project without selecting or hydrating the Agent's rich configuration.
func (r *AgentRepo) ListScheduleOptions(ctx context.Context, projectID string) ([]AgentScheduleOption, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+agentScheduleOptionColumns+` FROM agents
		WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
		  AND archived_at IS NULL
		  AND COALESCE(enabled, 1) = 1
		  AND COALESCE(selectable_as_primary, 1) = 1
		  AND (
			COALESCE(scope, 'global') <> 'project'
			OR (project_id IS NOT NULL AND project_id <> '' AND project_id = ?)
		  )
		ORDER BY name ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing schedule agent options: %w", err)
	}
	defer rows.Close()

	var options []AgentScheduleOption
	for rows.Next() {
		option, err := scanAgentScheduleOption(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning schedule agent option: %w", err)
		}
		options = append(options, *option)
	}
	return options, rows.Err()
}

// ListAgentListSummaries returns the prompt-safe lifecycle agent_list projection.
// It keeps filtering metadata needed by the inspector while avoiding full Agent
// hydration and decoding only the attached skill names.
func (r *AgentRepo) ListAgentListSummaries(ctx context.Context) ([]AgentListSummary, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+agentListSummaryColumns+` FROM agents WHERE COALESCE(generated_status, 'user_edited') <> 'archived' ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing agent list summaries: %w", err)
	}
	defer rows.Close()

	var summaries []AgentListSummary
	for rows.Next() {
		summary, err := scanAgentListSummary(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning agent list summary: %w", err)
		}
		summaries = append(summaries, *summary)
	}
	return summaries, rows.Err()
}

// ListPage returns one bounded Models-page-compatible agent projection. The
// Agents card currently carries its edit metadata, so this method retains the
// existing row shape while bounding both the result count and response HTML.
type AgentPageFilter struct {
	Search  string
	Enabled *bool
	Scope   string
	Origin  string
	Sort    string
}

func (r *AgentRepo) ListPage(ctx context.Context, limit, offset int, search string) ([]models.Agent, error) {
	return r.ListPageFiltered(ctx, limit, offset, AgentPageFilter{Search: search})
}

func (r *AgentRepo) ListPageFiltered(ctx context.Context, limit, offset int, filter AgentPageFilter) ([]models.Agent, error) {
	limit, offset = normalizeCardPageArgs(limit, offset)
	query := `SELECT ` + agentColumns + ` FROM agents WHERE COALESCE(generated_status, 'user_edited') <> 'archived'`
	args := make([]any, 0, 3)
	if search := strings.TrimSpace(filter.Search); search != "" {
		query += ` AND INSTR(LOWER(
			COALESCE(name, '') || ' ' || COALESCE(description, '') || ' ' ||
			COALESCE(model, '') || ' ' || COALESCE(system_prompt, '')
		), ?) > 0`
		args = append(args, strings.ToLower(search))
	}
	if filter.Enabled != nil {
		query += ` AND COALESCE(enabled, 1) = ?`
		args = append(args, *filter.Enabled)
	}
	if filter.Scope != "" {
		query += ` AND scope = ?`
		args = append(args, filter.Scope)
	}
	if filter.Origin != "" {
		status := filter.Origin
		if status == "custom" {
			status = string(models.AgentStatusUserEdited)
		}
		query += ` AND COALESCE(generated_status, 'user_edited') = ?`
		args = append(args, status)
	}
	switch filter.Sort {
	case "name_desc":
		query += ` ORDER BY name DESC, id DESC`
	case "updated_desc":
		query += ` ORDER BY updated_at DESC, id DESC`
	case "created_desc":
		query += ` ORDER BY created_at DESC, id DESC`
	default:
		query += ` ORDER BY name ASC, id ASC`
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing agent page: %w", err)
	}
	defer rows.Close()

	agents := make([]models.Agent, 0, limit)
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning agent page: %w", err)
		}
		agents = append(agents, *agent)
	}
	return agents, rows.Err()
}

func (r *AgentRepo) ListChatAssignableDefinitions(ctx context.Context) ([]models.ChatAssignableAgentDefinition, error) {
	rows, err := r.db.QueryContext(ctx, `
			SELECT id, name, COALESCE(description, ''), COALESCE(key, ''), COALESCE(system_kind, ''),
			       COALESCE(selectable_as_primary, 1), COALESCE(enabled, 1),
			       COALESCE(generated_status, 'user_edited'), archived_at
			FROM agents
			WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
			ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing chat assignable agent definitions: %w", err)
	}
	defer rows.Close()

	agents := []models.ChatAssignableAgentDefinition{}
	for rows.Next() {
		agent, err := scanChatAssignableAgentDefinition(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning chat assignable agent definition: %w", err)
		}
		agents = append(agents, *agent)
	}
	return agents, rows.Err()
}

func (r *AgentRepo) ListRuntimeSummaries(ctx context.Context) ([]AgentRuntimeSummary, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT name, description, model,
		       COALESCE(json_array_length(skills), 0) AS skill_count,
		       COALESCE(json_array_length(mcp_servers), 0) AS mcp_server_count
		FROM agents
		WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
		ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing runtime agent summaries: %w", err)
	}
	defer rows.Close()

	var summaries []AgentRuntimeSummary
	for rows.Next() {
		var summary AgentRuntimeSummary
		if err := rows.Scan(&summary.Name, &summary.Description, &summary.Model, &summary.SkillCount, &summary.MCPServerCount); err != nil {
			return nil, fmt.Errorf("scanning runtime agent summary: %w", err)
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// GetTaskDetailAgentLabel returns the identity needed by the recurring Task Detail
// status fragment. The assigned agent must still be live and available to the
// task's project, but no full Agent configuration is selected or hydrated.
func (r *AgentRepo) GetTaskDetailAgentLabel(ctx context.Context, projectID, agentID string) (*AgentPickerOption, error) {
	if agentID == "" {
		return nil, nil
	}

	var option AgentPickerOption
	err := r.db.QueryRowContext(ctx, `
		SELECT id, name
		FROM agents
		WHERE id = ?
		  AND COALESCE(generated_status, 'user_edited') <> 'archived'
		  AND archived_at IS NULL
		  AND (
			COALESCE(scope, 'global') <> 'project'
			OR (project_id IS NOT NULL AND project_id <> '' AND project_id = ?)
		  )`, agentID, projectID).Scan(&option.ID, &option.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting task detail agent label: %w", err)
	}
	return &option, nil
}

func (r *AgentRepo) ListPickerOptions(ctx context.Context) ([]AgentPickerOption, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name
		FROM agents
		WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
		ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing agent picker options: %w", err)
	}
	defer rows.Close()

	var options []AgentPickerOption
	for rows.Next() {
		var option AgentPickerOption
		if err := rows.Scan(&option.ID, &option.Name); err != nil {
			return nil, fmt.Errorf("scanning agent picker option: %w", err)
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

// ListPickerOptionsForProject returns compact options for global agents and
// project-scoped agents owned by projectID. It intentionally keeps the picker
// projection small because the settings page only needs IDs and names.
func (r *AgentRepo) ListPickerOptionsForProject(ctx context.Context, projectID string) ([]AgentPickerOption, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, name
		FROM agents
		WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
		  AND (
			COALESCE(scope, 'global') <> 'project'
			OR (project_id IS NOT NULL AND project_id <> '' AND project_id = ?)
		  )
		ORDER BY name ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing project agent picker options: %w", err)
	}
	defer rows.Close()

	var options []AgentPickerOption
	for rows.Next() {
		var option AgentPickerOption
		if err := rows.Scan(&option.ID, &option.Name); err != nil {
			return nil, fmt.Errorf("scanning project agent picker option: %w", err)
		}
		options = append(options, option)
	}
	return options, rows.Err()
}

func (r *AgentRepo) ListSkillCatalogRefs(ctx context.Context) ([]AgentSkillCatalogRef, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT id, COALESCE(key, ''), project_id
		FROM agents
		WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
		ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing agent skill catalog refs: %w", err)
	}
	defer rows.Close()

	var refs []AgentSkillCatalogRef
	for rows.Next() {
		var ref AgentSkillCatalogRef
		var projectID sql.NullString
		if err := rows.Scan(&ref.ID, &ref.Key, &projectID); err != nil {
			return nil, fmt.Errorf("scanning agent skill catalog ref: %w", err)
		}
		if projectID.Valid {
			ref.ProjectID = projectID.String
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

// ListSelectableReferencesForProject returns the bounded identity set used by
// Automation validation. It intentionally omits every full Agent configuration
// field because validation only matches key/ID and project scope.
func (r *AgentRepo) ListSelectableReferencesForProject(ctx context.Context, projectID string, limit int) ([]AgentSelectableReference, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+agentSelectableReferenceColumns+` FROM agents
		WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
		  AND archived_at IS NULL
		  AND COALESCE(enabled, 1) = 1 AND COALESCE(selectable_as_primary, 1) = 1
		  AND (project_id IS NULL OR project_id = '' OR project_id = ?)
		ORDER BY name ASC, id ASC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing selectable project agent references: %w", err)
	}
	defer rows.Close()

	var references []AgentSelectableReference
	for rows.Next() {
		var reference AgentSelectableReference
		if err := rows.Scan(&reference.ID, &reference.Key, &reference.ProjectID); err != nil {
			return nil, fmt.Errorf("scanning selectable project agent reference: %w", err)
		}
		references = append(references, reference)
	}
	return references, rows.Err()
}

func (r *AgentRepo) ListSelectableForProject(ctx context.Context, projectID string, limit int) ([]models.Agent, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	rows, err := r.db.QueryContext(ctx, `SELECT `+agentColumns+` FROM agents
		WHERE COALESCE(generated_status, 'user_edited') <> 'archived'
		  AND archived_at IS NULL
		  AND COALESCE(enabled, 1) = 1 AND COALESCE(selectable_as_primary, 1) = 1
		  AND (project_id IS NULL OR project_id = '' OR project_id = ?)
		ORDER BY name ASC, id ASC LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing selectable project agents: %w", err)
	}
	defer rows.Close()
	var agents []models.Agent
	for rows.Next() {
		agent, err := scanAgent(rows)
		if err != nil {
			return nil, fmt.Errorf("scanning selectable project agent: %w", err)
		}
		agents = append(agents, *agent)
	}
	return agents, rows.Err()
}

// ListIncludingArchived returns every agent row, including generated agents that
// were archived/absorbed. Most callers should use List; this is for narrow
// reconciliation paths that need to remove obsolete rows from earlier cleanups.
func (r *AgentRepo) ListIncludingArchived(ctx context.Context) ([]models.Agent, error) {
	return r.list(ctx, `SELECT `+agentColumns+` FROM agents ORDER BY name ASC`)
}

func (r *AgentRepo) list(ctx context.Context, query string) ([]models.Agent, error) {
	rows, err := r.db.QueryContext(ctx, query)
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
	agents, err := r.ListByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, nil
	}
	return &agents[0], nil
}

func (r *AgentRepo) GetUniqueSelectableByName(ctx context.Context, name string) (*models.Agent, error) {
	agents, err := r.ListSelectableByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if len(agents) != 1 {
		return nil, nil
	}
	return &agents[0], nil
}

func (r *AgentRepo) ListByName(ctx context.Context, name string) ([]models.Agent, error) {
	return r.listByName(ctx, name, "")
}

func (r *AgentRepo) ListSelectableByName(ctx context.Context, name string) ([]models.Agent, error) {
	return r.listByName(ctx, name, ` AND COALESCE(enabled, 1) = 1 AND COALESCE(selectable_as_primary, 1) = 1`)
}

func (r *AgentRepo) listByName(ctx context.Context, name, extraWhere string) ([]models.Agent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE LOWER(TRIM(name)) = LOWER(?) AND COALESCE(generated_status, 'user_edited') <> 'archived'`+extraWhere+` ORDER BY created_at ASC`, strings.TrimSpace(name))
	if err != nil {
		return nil, fmt.Errorf("listing agents by name: %w", err)
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

// GetByKey returns an agent by its durable lifecycle key, ignoring archived
// rows. Returns (nil, nil) when no live agent has the key.
func (r *AgentRepo) GetByKey(ctx context.Context, key string) (*models.Agent, error) {
	if key == "" {
		return nil, nil
	}
	a, err := scanAgent(r.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE key = ? AND COALESCE(generated_status, 'user_edited') <> 'archived' ORDER BY created_at ASC LIMIT 1`, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent by key: %w", err)
	}
	return a, nil
}

// GetByKeyIncludingArchived returns an agent by key without filtering archived
// rows. Use this only for protection/maintenance checks that must not allow a
// hidden archived row to be bypassed by writing to the same key.
func (r *AgentRepo) GetByKeyIncludingArchived(ctx context.Context, key string) (*models.Agent, error) {
	if key == "" {
		return nil, nil
	}
	a, err := scanAgent(r.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE key = ? ORDER BY created_at ASC LIMIT 1`, key))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent by key including archived: %w", err)
	}
	return a, nil
}

// MarkArchived flips an agent's generated_status to archived and stores the
// absorbed_into/reason metadata for forwarding (runbook line 1760).
func (r *AgentRepo) MarkArchived(ctx context.Context, id, absorbedInto, reason string) error {
	if id == "" {
		return fmt.Errorf("MarkArchived: missing id")
	}
	refs := []string{}
	if reason != "" {
		refs = append(refs, "reason:"+reason)
	}
	refsJSON, _ := marshalJSON(refs)
	var absorbed any
	if absorbedInto != "" {
		absorbed = absorbedInto
	}
	_, err := execBoundSQLite(ctx, r.db,
		`UPDATE agents SET generated_status = 'archived', enabled = 0,
		 absorbed_into = ?, source_refs_json = ?, archived_at = datetime('now'),
		 updated_at = datetime('now') WHERE id = ?`,
		absorbed, refsJSON, id)
	if err != nil {
		return fmt.Errorf("archiving agent: %w", err)
	}
	return nil
}

func (r *AgentRepo) GetBySystemKind(ctx context.Context, systemKind string) (*models.Agent, error) {
	a, err := scanAgent(r.db.QueryRowContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE system_kind = ? AND COALESCE(generated_status, 'user_edited') <> 'archived' ORDER BY created_at ASC LIMIT 1`, systemKind))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting agent by system kind: %w", err)
	}
	return a, nil
}

func (r *AgentRepo) ListBySystemKind(ctx context.Context, systemKind string) ([]models.Agent, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+agentColumns+` FROM agents WHERE system_kind = ? AND COALESCE(generated_status, 'user_edited') <> 'archived' ORDER BY created_at ASC`, systemKind)
	if err != nil {
		return nil, fmt.Errorf("listing agents by system kind: %w", err)
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

func (r *AgentRepo) Create(ctx context.Context, a *models.Agent) error {
	applyAgentDefaults(a)
	if err := r.normalizeAndValidateAgentName(ctx, a); err != nil {
		return err
	}
	normalizeAgentToolConfig(a)
	toolsJSON, err := marshalJSON(a.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}
	toolConfigJSON, err := marshalJSON(a.ToolConfig)
	if err != nil {
		return fmt.Errorf("marshaling tool_config: %w", err)
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
	permJSON, err := marshalJSON(a.PermissionDefaults)
	if err != nil {
		return fmt.Errorf("marshaling permission_defaults: %w", err)
	}
	modelDefaultsJSON, err := marshalJSON(a.ModelDefaults)
	if err != nil {
		return fmt.Errorf("marshaling model_defaults: %w", err)
	}
	sourceRefsJSON, err := marshalJSON(a.SourceRefs)
	if err != nil {
		return fmt.Errorf("marshaling source_refs: %w", err)
	}
	var projectID, absorbedInto any
	if a.ProjectID != "" {
		projectID = a.ProjectID
	}
	if a.AbsorbedInto != "" {
		absorbedInto = a.AbsorbedInto
	}
	err = queryRowBoundSQLite(ctx, r.db,
		`INSERT INTO agents (
		   id, name, description, system_prompt, model, tools, tool_config,
		   plugins, mcp_servers, skills, system_kind,
		   key, scope, project_id, selectable_as_primary, enabled,
		   permission_defaults_json, model_defaults_json,
		   created_by, generated_status, absorbed_into, source_refs_json
		 ) VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		   ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 RETURNING id, created_at, updated_at`,
		a.Name, a.Description, a.SystemPrompt, a.Model,
		toolsJSON, toolConfigJSON, pluginsJSON, mcpJSON, skillsJSON, a.SystemKind,
		a.Key, string(a.Scope), projectID, boolToInt(a.SelectableAsPrimary), boolToInt(a.Enabled),
		permJSON, modelDefaultsJSON,
		string(a.CreatedBy), string(a.GeneratedStatus), absorbedInto, sourceRefsJSON,
	).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating agent: %w", err)
	}
	return nil
}

// applyAgentDefaults populates the lifecycle-era fields with safe defaults
// when callers leave them blank, so the new columns are always populated and
// the dialog/importer paths converge on one model.
func applyAgentDefaults(a *models.Agent) {
	if a == nil {
		return
	}
	if a.Scope == "" {
		a.Scope = models.AgentScopeGlobal
	}
	if a.CreatedBy == "" {
		a.CreatedBy = models.AgentCreatedByUser
	}
	if a.GeneratedStatus == "" {
		a.GeneratedStatus = models.AgentStatusUserEdited
	}
	if a.SourceRefs == nil {
		a.SourceRefs = []string{}
	}
}

func (r *AgentRepo) Update(ctx context.Context, a *models.Agent) error {
	applyAgentDefaults(a)
	if err := r.normalizeAndValidateAgentName(ctx, a); err != nil {
		return err
	}
	normalizeAgentToolConfig(a)
	toolsJSON, err := marshalJSON(a.Tools)
	if err != nil {
		return fmt.Errorf("marshaling tools: %w", err)
	}
	toolConfigJSON, err := marshalJSON(a.ToolConfig)
	if err != nil {
		return fmt.Errorf("marshaling tool_config: %w", err)
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
	permJSON, err := marshalJSON(a.PermissionDefaults)
	if err != nil {
		return fmt.Errorf("marshaling permission_defaults: %w", err)
	}
	modelDefaultsJSON, err := marshalJSON(a.ModelDefaults)
	if err != nil {
		return fmt.Errorf("marshaling model_defaults: %w", err)
	}
	sourceRefsJSON, err := marshalJSON(a.SourceRefs)
	if err != nil {
		return fmt.Errorf("marshaling source_refs: %w", err)
	}
	var projectID, absorbedInto, archivedAt any
	if a.ProjectID != "" {
		projectID = a.ProjectID
	}
	if a.AbsorbedInto != "" {
		absorbedInto = a.AbsorbedInto
	}
	if a.ArchivedAt != nil {
		archivedAt = a.ArchivedAt.UTC()
	}
	_, err = execBoundSQLite(ctx, r.db,
		`UPDATE agents SET name = ?, description = ?, system_prompt = ?,
		 model = ?, tools = ?, tool_config = ?, plugins = ?, mcp_servers = ?, skills = ?, system_kind = ?,
		 key = ?, scope = ?, project_id = ?, selectable_as_primary = ?, enabled = ?,
		 permission_defaults_json = ?, model_defaults_json = ?,
		 created_by = ?, generated_status = ?, absorbed_into = ?, source_refs_json = ?,
		 archived_at = ?,
		 updated_at = datetime('now')
		 WHERE id = ?`,
		a.Name, a.Description, a.SystemPrompt, a.Model,
		toolsJSON, toolConfigJSON, pluginsJSON, mcpJSON, skillsJSON, a.SystemKind,
		a.Key, string(a.Scope), projectID, boolToInt(a.SelectableAsPrimary), boolToInt(a.Enabled),
		permJSON, modelDefaultsJSON,
		string(a.CreatedBy), string(a.GeneratedStatus), absorbedInto, sourceRefsJSON,
		archivedAt,
		a.ID)
	if err != nil {
		return fmt.Errorf("updating agent: %w", err)
	}
	return nil
}

func (r *AgentRepo) DeleteBulk(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one agent is required")
	}
	conn, finish, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return err
	}
	defer finish()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i := range ids {
		args[i] = ids[i]
	}
	var count, protected int
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(CASE WHEN generated_status = 'protected' THEN 1 ELSE 0 END), 0) FROM agents WHERE id IN (`+placeholders+`)`, args...).Scan(&count, &protected); err != nil {
		return err
	}
	if count != len(ids) {
		return fmt.Errorf("agent not found")
	}
	if protected > 0 {
		return fmt.Errorf("protected system agents cannot be deleted")
	}
	if _, err := conn.ExecContext(ctx, `UPDATE tasks SET agent_definition_id = NULL WHERE agent_definition_id IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM agents WHERE id IN (`+placeholders+`)`, args...); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `COMMIT`)
	return err
}

func (r *AgentRepo) Delete(ctx context.Context, id string) error {
	// Nullify FK references in tasks before deleting
	if _, err := execBoundSQLite(ctx, r.db, `UPDATE tasks SET agent_definition_id = NULL WHERE agent_definition_id = ?`, id); err != nil {
		return fmt.Errorf("nullifying agent in tasks: %w", err)
	}
	_, err := execBoundSQLite(ctx, r.db, `DELETE FROM agents WHERE id = ?`, id)
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
