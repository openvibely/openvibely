package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
)

var ErrAutomationTriggerOwned = errors.New("automation trigger schedule is already owned")

type AutomationRepo struct {
	db          *sql.DB
	broadcaster *events.Broadcaster
}

func NewAutomationRepo(db *sql.DB) *AutomationRepo { return &AutomationRepo{db: db} }
func (r *AutomationRepo) DB() *sql.DB              { return r.db }
func (r *AutomationRepo) SetBroadcaster(b *events.Broadcaster) {
	r.broadcaster = b
}

// ListBreadcrumbSelector returns a bounded compact saved-Automation search for one project.
func (r *AutomationRepo) ListBreadcrumbSelector(ctx context.Context, projectID, search, currentID string, limit int) ([]models.BreadcrumbSelectorItem, error) {
	if limit <= 0 || limit > 50 {
		limit = 20
	}
	search = strings.ToLower(strings.TrimSpace(search))
	rows, err := r.db.QueryContext(ctx, `SELECT a.id, a.name
		FROM automations a
		JOIN automation_versions v ON v.id = a.published_version_id
			AND v.project_id = a.project_id AND v.automation_id = a.id AND v.state = 'published'
			WHERE a.project_id = ? AND (? = '' OR INSTR(LOWER(a.name), ?) > 0)
		ORDER BY CASE WHEN a.id = ? THEN 0 ELSE 1 END,
			CASE WHEN LOWER(a.name) = ? THEN 0 WHEN LOWER(a.name) LIKE ? || '%' THEN 1 ELSE 2 END,
			a.updated_at DESC, a.id ASC
		LIMIT ?`, projectID, search, search, currentID, search, search, limit)
	if err != nil {
		return nil, fmt.Errorf("listing automation breadcrumb selector: %w", err)
	}
	defer rows.Close()
	items := make([]models.BreadcrumbSelectorItem, 0, limit)
	for rows.Next() {
		var item models.BreadcrumbSelectorItem
		if err := rows.Scan(&item.ID, &item.Name); err != nil {
			return nil, fmt.Errorf("scanning automation breadcrumb selector: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (r *AutomationRepo) ListByProject(ctx context.Context, projectID string, limit int) ([]models.Automation, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id, template_revision,
		created_via, created_at, updated_at, archived_at
		FROM automations WHERE project_id = ? ORDER BY updated_at DESC, id LIMIT ?`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing automations: %w", err)
	}
	defer rows.Close()
	var out []models.Automation
	for rows.Next() {
		var a models.Automation
		if err := scanAutomation(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) ListSavedByProject(ctx context.Context, projectID string) ([]models.Automation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id, template_revision,
		created_via, created_at, updated_at, archived_at
		FROM automations WHERE project_id = ? AND published_version_id IS NOT NULL
		ORDER BY updated_at DESC, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing saved automations: %w", err)
	}
	defer rows.Close()
	var out []models.Automation
	for rows.Next() {
		var a models.Automation
		if err := scanAutomation(rows, &a); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) ListPortfolioCards(ctx context.Context, projectID string) ([]models.AutomationCard, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT
		a.id, a.project_id, a.stable_key, a.name, a.description, a.automation_type,
		a.lifecycle_state, a.health_state, a.health_reason, a.health_evaluated_at, a.published_version_id, a.template_revision,
		a.created_via, a.created_at, a.updated_at, a.archived_at,
		v.id, v.project_id, v.automation_id, v.version, v.state, v.source, v.adapter_key, v.schema_version, v.created_at, v.published_at
		FROM automations a
		JOIN automation_versions v ON v.id = a.published_version_id AND v.automation_id = a.id AND v.project_id = a.project_id
		WHERE a.project_id = ? AND v.state = 'published'
		ORDER BY a.updated_at DESC, a.id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("listing automation portfolio cards: %w", err)
	}
	defer rows.Close()
	var out []models.AutomationCard
	for rows.Next() {
		var card models.AutomationCard
		if err := rows.Scan(&card.Automation.ID, &card.Automation.ProjectID, &card.Automation.StableKey, &card.Automation.Name, &card.Automation.Description, &card.Automation.AutomationType,
			&card.Automation.LifecycleState, &card.Automation.HealthState, &card.Automation.HealthReason, &card.Automation.HealthEvaluatedAt, &card.Automation.PublishedVersionID, &card.Automation.TemplateRevision,
			&card.Automation.CreatedVia, &card.Automation.CreatedAt, &card.Automation.UpdatedAt, &card.Automation.ArchivedAt,
			&card.Version.ID, &card.Version.ProjectID, &card.Version.AutomationID, &card.Version.Version, &card.Version.State, &card.Version.Source,
			&card.Version.AdapterKey, &card.Version.SchemaVersion, &card.Version.CreatedAt, &card.Version.PublishedAt); err != nil {
			return nil, err
		}
		out = append(out, card)
	}
	return out, rows.Err()
}

type AutomationCardListFilter struct {
	Search         string
	LifecycleState string
	HealthState    string
	AutomationType string
	Adapter        string
	Sort           string
}

// ListPortfolioCardsPage returns one bounded, project-scoped portfolio page.
// Search is limited to the metadata rendered by automationCardSearchText.
func (r *AutomationRepo) ListPortfolioCardsPage(ctx context.Context, projectID string, limit, offset int, search string) ([]models.AutomationCard, error) {
	return r.ListPortfolioCardsPageFiltered(ctx, projectID, limit, offset, AutomationCardListFilter{Search: search})
}

func (r *AutomationRepo) ListPortfolioCardsPageFiltered(ctx context.Context, projectID string, limit, offset int, filter AutomationCardListFilter) ([]models.AutomationCard, error) {
	limit, offset = normalizeCardPageArgs(limit, offset)
	query := `SELECT
		a.id, a.project_id, a.stable_key, a.name, a.description, a.automation_type,
		a.lifecycle_state, a.health_state, a.health_reason, a.health_evaluated_at, a.published_version_id, a.template_revision,
		a.created_via, a.created_at, a.updated_at, a.archived_at,
		v.id, v.project_id, v.automation_id, v.version, v.state, v.source, v.adapter_key, v.schema_version, v.created_at, v.published_at
		FROM automations a
		JOIN automation_versions v ON v.id = a.published_version_id AND v.automation_id = a.id AND v.project_id = a.project_id
		WHERE a.project_id = ? AND v.state = 'published'`
	args := []any{projectID}
	if search := strings.TrimSpace(filter.Search); search != "" {
		query += ` AND INSTR(LOWER(
			COALESCE(a.name, '') || ' ' || COALESCE(a.description, '') || ' ' ||
			COALESCE(v.adapter_key, '') || ' ' || COALESCE(a.lifecycle_state, '') || ' ' || COALESCE(a.health_state, '')
		), ?) > 0`
		args = append(args, strings.ToLower(search))
	}
	for _, condition := range []struct{ column, value string }{{"a.lifecycle_state", filter.LifecycleState}, {"a.health_state", filter.HealthState}, {"a.automation_type", filter.AutomationType}, {"v.adapter_key", filter.Adapter}} {
		if condition.value != "" {
			query += ` AND ` + condition.column + ` = ?`
			args = append(args, condition.value)
		}
	}
	switch filter.Sort {
	case "updated_asc":
		query += ` ORDER BY a.updated_at ASC, a.id ASC`
	case "name_asc":
		query += ` ORDER BY a.name ASC, a.id ASC`
	case "name_desc":
		query += ` ORDER BY a.name DESC, a.id DESC`
	default:
		query += ` ORDER BY a.updated_at DESC, a.id ASC`
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing automation portfolio card page: %w", err)
	}
	defer rows.Close()
	out := make([]models.AutomationCard, 0, limit)
	for rows.Next() {
		var card models.AutomationCard
		if err := rows.Scan(&card.Automation.ID, &card.Automation.ProjectID, &card.Automation.StableKey, &card.Automation.Name, &card.Automation.Description, &card.Automation.AutomationType,
			&card.Automation.LifecycleState, &card.Automation.HealthState, &card.Automation.HealthReason, &card.Automation.HealthEvaluatedAt, &card.Automation.PublishedVersionID, &card.Automation.TemplateRevision,
			&card.Automation.CreatedVia, &card.Automation.CreatedAt, &card.Automation.UpdatedAt, &card.Automation.ArchivedAt,
			&card.Version.ID, &card.Version.ProjectID, &card.Version.AutomationID, &card.Version.Version, &card.Version.State, &card.Version.Source,
			&card.Version.AdapterKey, &card.Version.SchemaVersion, &card.Version.CreatedAt, &card.Version.PublishedAt); err != nil {
			return nil, fmt.Errorf("scanning automation portfolio card page: %w", err)
		}
		out = append(out, card)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) GetByStableKey(ctx context.Context, projectID, stableKey string) (*models.Automation, error) {
	var a models.Automation
	err := scanAutomation(r.db.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id, template_revision,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND stable_key = ?`, projectID, stableKey), &a)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting automation by stable key: %w", err)
	}
	return &a, nil
}

func (r *AutomationRepo) Exists(ctx context.Context, projectID, automationID string) (bool, error) {
	var exists bool
	if err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM automations WHERE project_id = ? AND id = ?)`, projectID, automationID).Scan(&exists); err != nil {
		return false, fmt.Errorf("checking automation existence: %w", err)
	}
	return exists, nil
}

func (r *AutomationRepo) GetDefinition(ctx context.Context, projectID, automationID string) (*models.AutomationDefinition, error) {
	var a models.Automation
	err := scanAutomation(r.db.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id, template_revision,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`, projectID, automationID), &a)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting automation: %w", err)
	}
	if a.PublishedVersionID == nil {
		return &models.AutomationDefinition{Automation: a}, nil
	}
	return r.loadDefinition(ctx, r.db, a, *a.PublishedVersionID)
}

func (r *AutomationRepo) PublishRegistered(ctx context.Context, in models.AutomationRegisteredPublication) (*models.AutomationDefinition, bool, error) {
	conn, finishImmediate, err := beginImmediateConn(ctx, r.db)
	if err != nil {
		return nil, false, fmt.Errorf("beginning automation publication: %w", err)
	}
	defer finishImmediate()

	a, err := getAutomationByStableKeyQuery(ctx, conn, in.ProjectID, in.StableKey)
	if err != nil {
		return nil, false, err
	}
	if a != nil && a.PublishedVersionID != nil {
		var publishedAdapter string
		if err := conn.QueryRowContext(ctx, `SELECT adapter_key FROM automation_versions
			WHERE id = ? AND automation_id = ? AND project_id = ?`, *a.PublishedVersionID, a.ID, in.ProjectID).Scan(&publishedAdapter); err != nil {
			return nil, false, fmt.Errorf("loading published automation adapter: %w", err)
		}
		if publishedAdapter != in.AdapterKey {
			return nil, false, fmt.Errorf("published automation adapter cannot change from %q to %q", publishedAdapter, in.AdapterKey)
		}
		if err := deleteObsoleteOwnedAutomationSchedules(ctx, conn, in.ProjectID, a.ID, *a.PublishedVersionID); err != nil {
			return nil, false, err
		}
		if _, err := conn.ExecContext(ctx, `DELETE FROM automation_versions
			WHERE project_id = ? AND automation_id = ? AND id <> ?`, in.ProjectID, a.ID, *a.PublishedVersionID); err != nil {
			return nil, false, fmt.Errorf("removing retained automation graphs: %w", err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE automation_versions SET version = 1, state = 'published'
				WHERE project_id = ? AND automation_id = ? AND id = ?`, in.ProjectID, a.ID, *a.PublishedVersionID); err != nil {
			return nil, false, fmt.Errorf("normalizing current automation graph: %w", err)
		}
		def, err := r.loadDefinition(ctx, conn, *a, *a.PublishedVersionID)
		if err != nil {
			return nil, false, err
		}
		if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
			return nil, false, err
		}
		return def, true, nil
	}
	if a == nil {
		a = &models.Automation{}
		err = conn.QueryRowContext(ctx, `INSERT INTO automations
			(project_id, stable_key, name, description, automation_type, lifecycle_state, created_via)
			VALUES (?, ?, ?, ?, ?, 'draft', ?) RETURNING id, project_id, stable_key, name, description,
			automation_type, lifecycle_state, health_state, health_reason, health_evaluated_at,
			published_version_id, created_via, created_at, updated_at, archived_at`, in.ProjectID, in.StableKey,
			in.Name, in.Description, in.AutomationType, in.CreatedVia).Scan(&a.ID, &a.ProjectID, &a.StableKey,
			&a.Name, &a.Description, &a.AutomationType, &a.LifecycleState, &a.HealthState, &a.HealthReason,
			&a.HealthEvaluatedAt, &a.PublishedVersionID, &a.CreatedVia, &a.CreatedAt, &a.UpdatedAt, &a.ArchivedAt)
		if err != nil {
			return nil, false, fmt.Errorf("creating automation: %w", err)
		}
	} else {
		if _, err := conn.ExecContext(ctx, `UPDATE automations SET name = ?, description = ?, automation_type = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`, in.Name, in.Description, in.AutomationType, a.ID, in.ProjectID); err != nil {
			return nil, false, fmt.Errorf("updating automation identity: %w", err)
		}
	}

	effectiveLifecycle := a.LifecycleState
	if effectiveLifecycle == models.AutomationDraft {
		effectiveLifecycle = models.AutomationActive
	}
	var versionNumber int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM automation_versions WHERE automation_id = ?`, a.ID).Scan(&versionNumber); err != nil {
		return nil, false, fmt.Errorf("selecting automation version: %w", err)
	}
	var versionID string
	if err := conn.QueryRowContext(ctx, `INSERT INTO automation_versions (project_id, automation_id, version, state, source, adapter_key)
		VALUES (?, ?, ?, 'draft', 'bootstrap', ?) RETURNING id`, in.ProjectID, a.ID, versionNumber, in.AdapterKey).Scan(&versionID); err != nil {
		return nil, false, fmt.Errorf("creating automation version: %w", err)
	}

	nodeIDs, err := writeAutomationGraphRows(ctx, conn, in.ProjectID, a.ID, versionID, in.Nodes, in.Edges)
	if err != nil {
		return nil, false, err
	}

	newTriggerIDs := make(map[string]struct{})
	for _, binding := range in.Resources {
		nodeID, ok := nodeIDs[binding.NodeKey]
		if !ok {
			return nil, false, fmt.Errorf("resource binding references unknown node %q", binding.NodeKey)
		}
		if err := validateAutomationResource(ctx, conn, in.ProjectID, binding.ResourceType, binding.ResourceID); err != nil {
			return nil, false, err
		}
		relation := binding.Relation
		if relation == "" {
			relation = "owned"
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_definition_resources
			(project_id, automation_id, version_id, node_id, resource_type, resource_id, relation)
			VALUES (?, ?, ?, ?, ?, ?, ?)`, in.ProjectID, a.ID, versionID, nodeID, binding.ResourceType, binding.ResourceID, relation); err != nil {
			return nil, false, fmt.Errorf("linking automation resource: %w", err)
		}
		if binding.ResourceType == "schedule" {
			newTriggerIDs[binding.ResourceID] = struct{}{}
			var ownerAutomationID string
			err := conn.QueryRowContext(ctx, `SELECT automation_id FROM automation_trigger_owners WHERE schedule_id = ?`, binding.ResourceID).Scan(&ownerAutomationID)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return nil, false, fmt.Errorf("checking trigger ownership: %w", err)
			}
			if err == nil && ownerAutomationID != a.ID {
				return nil, false, fmt.Errorf("%w: %s", ErrAutomationTriggerOwned, binding.ResourceID)
			}
			ownershipState := string(effectiveLifecycle)
			if ownershipState == string(models.AutomationActive) {
				ownershipState = "active"
			}
			if _, err := conn.ExecContext(ctx, `INSERT INTO automation_trigger_owners
				(schedule_id, project_id, automation_id, version_id, node_id, ownership_state)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(schedule_id) DO UPDATE SET version_id = excluded.version_id, node_id = excluded.node_id,
				ownership_state = excluded.ownership_state, updated_at = CURRENT_TIMESTAMP`, binding.ResourceID, in.ProjectID,
				a.ID, versionID, nodeID, ownershipState); err != nil {
				return nil, false, fmt.Errorf("claiming trigger ownership: %w", err)
			}
			enabled := effectiveLifecycle == models.AutomationActive
			if _, err := conn.ExecContext(ctx, `UPDATE schedules SET enabled = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, enabled, binding.ResourceID); err != nil {
				return nil, false, fmt.Errorf("updating trigger lifecycle: %w", err)
			}
		}
	}
	if len(newTriggerIDs) == 0 {
		return nil, false, errors.New("registered automation requires at least one trigger schedule")
	}

	if err := deleteObsoleteOwnedAutomationSchedules(ctx, conn, in.ProjectID, a.ID, versionID); err != nil {
		return nil, false, err
	}

	if _, err := conn.ExecContext(ctx, `UPDATE automation_versions SET state = 'published', published_at = CURRENT_TIMESTAMP WHERE id = ? AND state = 'draft'`, versionID); err != nil {
		return nil, false, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automations SET lifecycle_state = ?, published_version_id = ?,
		archived_at = CASE WHEN ? = 'archived' THEN archived_at ELSE NULL END, updated_at = CURRENT_TIMESTAMP
		WHERE id = ? AND project_id = ?`, effectiveLifecycle, versionID, effectiveLifecycle, a.ID, in.ProjectID); err != nil {
		return nil, false, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM automation_versions WHERE project_id = ? AND automation_id = ? AND id <> ?`, in.ProjectID, a.ID, versionID); err != nil {
		return nil, false, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_versions SET version = 1 WHERE project_id = ? AND automation_id = ? AND id = ?`, in.ProjectID, a.ID, versionID); err != nil {
		return nil, false, err
	}
	updated, err := getAutomationByStableKeyQuery(ctx, conn, in.ProjectID, in.StableKey)
	if err != nil {
		return nil, false, fmt.Errorf("loading published automation identity: %w", err)
	}
	if updated == nil || updated.PublishedVersionID == nil {
		return nil, false, errors.New("published automation identity is missing")
	}
	def, err := r.loadDefinition(ctx, conn, *updated, versionID)
	if err != nil {
		return nil, false, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, false, fmt.Errorf("committing automation publication: %w", err)
	}
	r.PublishInvalidation(events.AutomationDefinitionUpdated, in.ProjectID, models.AutomationBinding{AutomationID: def.Automation.ID, VersionID: def.Version.ID})
	return def, false, nil
}

func deleteObsoleteOwnedAutomationSchedules(ctx context.Context, exec SQLExecutor, projectID, automationID, retainedVersionID string) error {
	_, err := exec.ExecContext(ctx, `DELETE FROM schedules
		WHERE id IN (
			SELECT owner.schedule_id
			FROM automation_trigger_owners owner
			WHERE owner.project_id = ? AND owner.automation_id = ? AND owner.version_id <> ?
			UNION
			SELECT resource.resource_id
			FROM automation_definition_resources resource
			WHERE resource.project_id = ? AND resource.automation_id = ? AND resource.version_id <> ?
				AND resource.resource_type = 'schedule' AND resource.relation = 'owned'
		)
		AND NOT EXISTS (
			SELECT 1 FROM automation_definition_resources retained
			WHERE retained.project_id = ? AND retained.automation_id = ? AND retained.version_id = ?
				AND retained.resource_type = 'schedule' AND retained.resource_id = schedules.id
		)`, projectID, automationID, retainedVersionID, projectID, automationID, retainedVersionID,
		projectID, automationID, retainedVersionID)
	if err != nil {
		return fmt.Errorf("deleting obsolete owned Automation schedules: %w", err)
	}
	return nil
}

func (r *AutomationRepo) ListResourceSummaries(ctx context.Context, projectID, automationID, versionID string, limit int) ([]models.AutomationResourceSummary, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `SELECT adr.node_id, n.node_key, adr.resource_type, adr.resource_id, adr.relation,
		CASE adr.resource_type WHEN 'task' THEN COALESCE(t.title, '') WHEN 'schedule' THEN COALESCE(st.title, '') ELSE adr.resource_id END,
		CASE adr.resource_type WHEN 'task' THEN COALESCE(t.status, '') WHEN 'schedule' THEN CASE WHEN s.enabled = 1 THEN 'enabled' ELSE 'disabled' END ELSE 'linked' END
		FROM automation_definition_resources adr
		JOIN automation_nodes n ON n.id = adr.node_id AND n.version_id = adr.version_id
		LEFT JOIN tasks t ON adr.resource_type = 'task' AND t.id = adr.resource_id AND t.project_id = adr.project_id
		LEFT JOIN schedules s ON adr.resource_type = 'schedule' AND s.id = adr.resource_id
		LEFT JOIN tasks st ON s.task_id = st.id AND st.project_id = adr.project_id
		WHERE adr.project_id = ? AND adr.automation_id = ? AND adr.version_id = ?
		ORDER BY n.node_key, adr.resource_type, adr.resource_id LIMIT ?`, projectID, automationID, versionID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing automation resource summaries: %w", err)
	}
	defer rows.Close()
	var out []models.AutomationResourceSummary
	for rows.Next() {
		var s models.AutomationResourceSummary
		if err := rows.Scan(&s.NodeID, &s.NodeKey, &s.ResourceType, &s.ResourceID, &s.Relation, &s.Name, &s.Status); err != nil {
			return nil, err
		}
		switch s.ResourceType {
		case "task":
			s.URL = "/tasks/" + s.ResourceID
		case "schedule":
			s.URL = "/schedule"
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *AutomationRepo) loadDefinition(ctx context.Context, q queryer, a models.Automation, versionID string) (*models.AutomationDefinition, error) {
	def := &models.AutomationDefinition{Automation: a}
	if err := q.QueryRowContext(ctx, `SELECT id, project_id, automation_id, version, state, source, adapter_key, schema_version, created_at, published_at
		FROM automation_versions WHERE id = ? AND automation_id = ? AND project_id = ?`, versionID, a.ID, a.ProjectID).Scan(&def.Version.ID,
		&def.Version.ProjectID, &def.Version.AutomationID, &def.Version.Version, &def.Version.State, &def.Version.Source,
		&def.Version.AdapterKey, &def.Version.SchemaVersion, &def.Version.CreatedAt, &def.Version.PublishedAt); err != nil {
		return nil, fmt.Errorf("loading automation version: %w", err)
	}
	nodeRows, err := q.QueryContext(ctx, `SELECT id, project_id, automation_id, version_id, node_key, name, node_type, role, config_json, position_x, position_y, created_at, updated_at
		FROM automation_nodes WHERE version_id = ? AND automation_id = ? AND project_id = ? ORDER BY position_x, position_y, node_key`, versionID, a.ID, a.ProjectID)
	if err != nil {
		return nil, err
	}
	for nodeRows.Next() {
		var n models.AutomationNode
		if err := nodeRows.Scan(&n.ID, &n.ProjectID, &n.AutomationID, &n.VersionID, &n.NodeKey, &n.Name, &n.NodeType, &n.Role, &n.ConfigJSON, &n.PositionX, &n.PositionY, &n.CreatedAt, &n.UpdatedAt); err != nil {
			nodeRows.Close()
			return nil, err
		}
		def.Nodes = append(def.Nodes, n)
	}
	nodeRows.Close()
	edgeRows, err := q.QueryContext(ctx, `SELECT id, project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, label, condition_json, display_order, created_at
		FROM automation_edges WHERE version_id = ? AND automation_id = ? AND project_id = ? ORDER BY display_order, edge_key`, versionID, a.ID, a.ProjectID)
	if err != nil {
		return nil, err
	}
	for edgeRows.Next() {
		var e models.AutomationEdge
		if err := edgeRows.Scan(&e.ID, &e.ProjectID, &e.AutomationID, &e.VersionID, &e.SourceNodeID, &e.TargetNodeID, &e.EdgeKey, &e.Label, &e.ConditionJSON, &e.DisplayOrder, &e.CreatedAt); err != nil {
			edgeRows.Close()
			return nil, err
		}
		def.Edges = append(def.Edges, e)
	}
	edgeRows.Close()
	resourceRows, err := q.QueryContext(ctx, `SELECT adr.id, adr.project_id, adr.automation_id, adr.version_id, adr.node_id, n.node_key, adr.resource_type, adr.resource_id, adr.relation, adr.created_at
		FROM automation_definition_resources adr JOIN automation_nodes n ON n.id = adr.node_id
		WHERE adr.version_id = ? AND adr.automation_id = ? AND adr.project_id = ? ORDER BY n.node_key, adr.resource_type, adr.resource_id`, versionID, a.ID, a.ProjectID)
	if err != nil {
		return nil, err
	}
	defer resourceRows.Close()
	for resourceRows.Next() {
		var resource models.AutomationDefinitionResource
		if err := resourceRows.Scan(&resource.ID, &resource.ProjectID, &resource.AutomationID, &resource.VersionID, &resource.NodeID, &resource.NodeKey, &resource.ResourceType, &resource.ResourceID, &resource.Relation, &resource.CreatedAt); err != nil {
			return nil, err
		}
		def.Resources = append(def.Resources, resource)
	}
	return def, resourceRows.Err()
}

type automationRowScanner interface{ Scan(...any) error }
type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanAutomation(row automationRowScanner, a *models.Automation) error {
	return row.Scan(&a.ID, &a.ProjectID, &a.StableKey, &a.Name, &a.Description, &a.AutomationType,
		&a.LifecycleState, &a.HealthState, &a.HealthReason, &a.HealthEvaluatedAt, &a.PublishedVersionID, &a.TemplateRevision,
		&a.CreatedVia, &a.CreatedAt, &a.UpdatedAt, &a.ArchivedAt)
}

func getAutomationByStableKeyQuery(ctx context.Context, q queryer, projectID, stableKey string) (*models.Automation, error) {
	var a models.Automation
	err := scanAutomation(q.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id, template_revision,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND stable_key = ?`, projectID, stableKey), &a)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting automation identity: %w", err)
	}
	return &a, nil
}

func validateAutomationResource(ctx context.Context, q queryer, projectID, resourceType, resourceID string) error {
	if strings.TrimSpace(resourceID) == "" {
		return errors.New("automation resource ID is required")
	}
	var ownerProjectID string
	switch resourceType {
	case "task":
		if err := q.QueryRowContext(ctx, `SELECT project_id FROM tasks WHERE id = ?`, resourceID).Scan(&ownerProjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task resource %q not found", resourceID)
			}
			return err
		}
	case "schedule":
		if err := q.QueryRowContext(ctx, `SELECT t.project_id FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE s.id = ?`, resourceID).Scan(&ownerProjectID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("schedule resource %q not found", resourceID)
			}
			return err
		}
	default:
		return fmt.Errorf("registered automation resource type %q is not supported in phase 1", resourceType)
	}
	if ownerProjectID != projectID {
		return fmt.Errorf("automation resource %q belongs to another project", resourceID)
	}
	return nil
}
