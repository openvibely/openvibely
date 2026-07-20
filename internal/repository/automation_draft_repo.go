package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type AutomationDraftWrite struct {
	ProjectID        string
	AutomationID     string
	VersionID        string
	StableKey        string
	Source           string
	CreatedVia       string
	Candidate        models.AutomationDraftCandidate
	ValidationErrors []models.AutomationValidationIssue
}

func (r *AutomationRepo) CreateAutomationDraft(ctx context.Context, in AutomationDraftWrite) (*models.AutomationDefinition, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	if in.AutomationID == "" {
		in.AutomationID = NewID()
	}
	if in.VersionID == "" {
		in.VersionID = NewID()
	}
	if in.StableKey == "" {
		in.StableKey = "draft/" + in.AutomationID
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	if in.CreatedVia == "" {
		in.CreatedVia = "web"
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO automations
		(id, project_id, stable_key, name, description, automation_type, lifecycle_state, created_via)
		VALUES (?, ?, ?, ?, ?, ?, 'draft', ?)`, in.AutomationID, in.ProjectID, in.StableKey,
		in.Candidate.Name, in.Candidate.Description, in.Candidate.AutomationType, in.CreatedVia); err != nil {
		return nil, fmt.Errorf("creating automation draft: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, schema_version)
		VALUES (?, ?, ?, 1, 'draft', ?, ?, ?)`, in.VersionID, in.ProjectID, in.AutomationID,
		in.Source, in.Candidate.AdapterKey, in.Candidate.SchemaVersion); err != nil {
		return nil, fmt.Errorf("creating automation draft version: %w", err)
	}
	if err := replaceAutomationDraftGraph(ctx, conn, in); err != nil {
		return nil, err
	}
	automation, err := getAutomationByStableKeyQuery(ctx, conn, in.ProjectID, in.StableKey)
	if err != nil {
		return nil, err
	}
	definition, err := r.loadDefinition(ctx, conn, *automation, in.VersionID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	return definition, nil
}

func (r *AutomationRepo) CreateAutomationDraftVersion(ctx context.Context, in AutomationDraftWrite) (*models.AutomationDefinition, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var automation models.Automation
	if err := scanAutomation(conn.QueryRowContext(ctx, `SELECT id, project_id, stable_key, name, description, automation_type,
		lifecycle_state, health_state, health_reason, health_evaluated_at, published_version_id,
		created_via, created_at, updated_at, archived_at FROM automations WHERE project_id = ? AND id = ?`,
		in.ProjectID, in.AutomationID), &automation); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("automation not found")
		}
		return nil, err
	}
	if automation.PublishedVersionID == nil || *automation.PublishedVersionID == "" {
		return nil, errors.New("automation has no published version to clone")
	}
	if in.VersionID == "" {
		in.VersionID = NewID()
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	var nextVersion int
	if err := conn.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) + 1 FROM automation_versions
		WHERE project_id = ? AND automation_id = ?`, in.ProjectID, in.AutomationID).Scan(&nextVersion); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO automation_versions
		(id, project_id, automation_id, version, state, source, adapter_key, schema_version)
		VALUES (?, ?, ?, ?, 'draft', ?, ?, ?)`, in.VersionID, in.ProjectID, in.AutomationID, nextVersion,
		in.Source, in.Candidate.AdapterKey, in.Candidate.SchemaVersion); err != nil {
		return nil, fmt.Errorf("creating cloned automation draft version: %w", err)
	}
	if err := replaceAutomationDraftGraph(ctx, conn, in); err != nil {
		return nil, err
	}
	definition, err := r.loadDefinition(ctx, conn, automation, in.VersionID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	return definition, nil
}

func (r *AutomationRepo) ReplaceAutomationDraft(ctx context.Context, in AutomationDraftWrite) (*models.AutomationDefinition, error) {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return nil, err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	var stableKey string
	var state models.AutomationVersionState
	if err := conn.QueryRowContext(ctx, `SELECT a.stable_key, v.state FROM automations a
		JOIN automation_versions v ON v.automation_id = a.id AND v.project_id = a.project_id
		WHERE a.project_id = ? AND a.id = ? AND v.id = ?`, in.ProjectID, in.AutomationID, in.VersionID).Scan(&stableKey, &state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	if state != models.AutomationVersionDraft {
		return nil, errors.New("published automation versions are immutable")
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automations SET name = ?, description = ?, automation_type = ?, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND id = ?`, in.Candidate.Name, in.Candidate.Description, in.Candidate.AutomationType, in.ProjectID, in.AutomationID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `UPDATE automation_versions SET adapter_key = ?, schema_version = ?
		WHERE project_id = ? AND automation_id = ? AND id = ? AND state = 'draft'`, in.Candidate.AdapterKey,
		in.Candidate.SchemaVersion, in.ProjectID, in.AutomationID, in.VersionID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM automation_edges WHERE project_id = ? AND automation_id = ? AND version_id = ?`, in.ProjectID, in.AutomationID, in.VersionID); err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM automation_nodes WHERE project_id = ? AND automation_id = ? AND version_id = ?`, in.ProjectID, in.AutomationID, in.VersionID); err != nil {
		return nil, err
	}
	if err := replaceAutomationDraftGraph(ctx, conn, in); err != nil {
		return nil, err
	}
	automation, err := getAutomationByStableKeyQuery(ctx, conn, in.ProjectID, stableKey)
	if err != nil {
		return nil, err
	}
	definition, err := r.loadDefinition(ctx, conn, *automation, in.VersionID)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return nil, err
	}
	committed = true
	return definition, nil
}

func replaceAutomationDraftGraph(ctx context.Context, conn *sql.Conn, in AutomationDraftWrite) error {
	nodeIDs := make(map[string]string, len(in.Candidate.Nodes))
	for _, node := range in.Candidate.Nodes {
		nodeID := NewID()
		nodeIDs[node.Key] = nodeID
		config, err := json.Marshal(node.Config)
		if err != nil {
			return err
		}
		x, y := 0.0, 0.0
		if node.Position != nil {
			x, y = node.Position.X, node.Position.Y
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_nodes
			(id, project_id, automation_id, version_id, node_key, name, node_type, role, config_json, position_x, position_y)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, in.ProjectID, in.AutomationID, in.VersionID,
			node.Key, node.Name, node.Type, node.Role, string(config), x, y); err != nil {
			return fmt.Errorf("creating automation draft node %q: %w", node.Key, err)
		}
	}
	for index, edge := range in.Candidate.Edges {
		condition, err := json.Marshal(edge.Condition)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_edges
			(id, project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, label, condition_json, display_order)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, NewID(), in.ProjectID, in.AutomationID, in.VersionID,
			nodeIDs[edge.From], nodeIDs[edge.To], edge.Key, edge.Label, string(condition), index); err != nil {
			return fmt.Errorf("creating automation draft edge %q: %w", edge.Key, err)
		}
	}
	candidateJSON, _ := json.Marshal(in.Candidate)
	assumptionsJSON, _ := json.Marshal(in.Candidate.Assumptions)
	warningsJSON, _ := json.Marshal(in.Candidate.Warnings)
	validationJSON, _ := json.Marshal(in.ValidationErrors)
	_, err := conn.ExecContext(ctx, `INSERT INTO automation_draft_metadata
		(version_id, project_id, automation_id, candidate_json, assumptions_json, warnings_json, validation_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(version_id) DO UPDATE SET candidate_json = excluded.candidate_json,
		assumptions_json = excluded.assumptions_json, warnings_json = excluded.warnings_json,
		validation_json = excluded.validation_json, updated_at = CURRENT_TIMESTAMP`, in.VersionID, in.ProjectID,
		in.AutomationID, string(candidateJSON), string(assumptionsJSON), string(warningsJSON), string(validationJSON))
	return err
}

func (r *AutomationRepo) DiscardAutomationDraft(ctx context.Context, projectID, automationID, versionID string) error {
	conn, err := r.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()
	result, err := conn.ExecContext(ctx, `DELETE FROM automation_versions
		WHERE project_id = ? AND automation_id = ? AND id = ? AND state = 'draft'`, projectID, automationID, versionID)
	if err != nil {
		return err
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return errors.New("automation draft not found")
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM automations
		WHERE project_id = ? AND id = ? AND published_version_id IS NULL
		AND NOT EXISTS (SELECT 1 FROM automation_versions WHERE project_id = ? AND automation_id = ?)`,
		projectID, automationID, projectID, automationID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *AutomationRepo) GetLatestAutomationDraftMetadata(ctx context.Context, projectID, automationID string) (*models.AutomationDraftMetadata, error) {
	var metadata models.AutomationDraftMetadata
	var assumptionsJSON, warningsJSON, validationJSON string
	err := r.db.QueryRowContext(ctx, `SELECT m.project_id, m.automation_id, m.version_id, m.candidate_json,
		m.assumptions_json, m.warnings_json, m.validation_json, m.updated_at
		FROM automation_draft_metadata m
		JOIN automation_versions v ON v.id = m.version_id AND v.project_id = m.project_id AND v.automation_id = m.automation_id
		WHERE m.project_id = ? AND m.automation_id = ? AND v.state = 'draft'
		ORDER BY v.version DESC LIMIT 1`, projectID, automationID).
		Scan(&metadata.ProjectID, &metadata.AutomationID, &metadata.VersionID, &metadata.CandidateJSON,
			&assumptionsJSON, &warningsJSON, &validationJSON, &metadata.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(assumptionsJSON), &metadata.Assumptions); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(warningsJSON), &metadata.Warnings); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(validationJSON), &metadata.ValidationErrors); err != nil {
		return nil, err
	}
	return &metadata, nil
}

func (r *AutomationRepo) GetAutomationDraftMetadata(ctx context.Context, projectID, automationID, versionID string) (*models.AutomationDraftMetadata, error) {
	var metadata models.AutomationDraftMetadata
	var assumptionsJSON, warningsJSON, validationJSON string
	err := r.db.QueryRowContext(ctx, `SELECT project_id, automation_id, version_id, candidate_json,
		assumptions_json, warnings_json, validation_json, updated_at FROM automation_draft_metadata
		WHERE project_id = ? AND automation_id = ? AND version_id = ?`, projectID, automationID, versionID).
		Scan(&metadata.ProjectID, &metadata.AutomationID, &metadata.VersionID, &metadata.CandidateJSON,
			&assumptionsJSON, &warningsJSON, &validationJSON, &metadata.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(assumptionsJSON), &metadata.Assumptions); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(warningsJSON), &metadata.Warnings); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(validationJSON), &metadata.ValidationErrors); err != nil {
		return nil, err
	}
	return &metadata, nil
}
