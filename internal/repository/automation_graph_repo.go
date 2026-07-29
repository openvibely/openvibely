package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openvibely/openvibely/internal/models"
)

type AutomationGraphWrite struct {
	ProjectID        string
	AutomationID     string
	GraphID          string
	Candidate        models.AutomationDraftCandidate
	ValidationErrors []models.AutomationValidationIssue
}

func writeAutomationGraph(ctx context.Context, conn *sql.Conn, in AutomationGraphWrite) error {
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
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, in.ProjectID, in.AutomationID, in.GraphID,
			node.Key, node.Name, node.Type, node.Role, string(config), x, y); err != nil {
			return fmt.Errorf("creating Automation graph node %q: %w", node.Key, err)
		}
	}
	for index, edge := range in.Candidate.Edges {
		condition, err := json.Marshal(edge.Condition)
		if err != nil {
			return err
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_edges
			(id, project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, label, condition_json, display_order)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, NewID(), in.ProjectID, in.AutomationID, in.GraphID,
			nodeIDs[edge.From], nodeIDs[edge.To], edge.Key, edge.Label, string(condition), index); err != nil {
			return fmt.Errorf("creating Automation graph edge %q: %w", edge.Key, err)
		}
	}
	candidateJSON, _ := json.Marshal(in.Candidate)
	assumptionsJSON, _ := json.Marshal(in.Candidate.Assumptions)
	warningsJSON, _ := json.Marshal(in.Candidate.Warnings)
	validationJSON, _ := json.Marshal(in.ValidationErrors)
	_, err := conn.ExecContext(ctx, `INSERT INTO automation_graph_metadata
		(version_id, project_id, automation_id, candidate_json, assumptions_json, warnings_json, validation_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, in.GraphID, in.ProjectID, in.AutomationID, string(candidateJSON),
		string(assumptionsJSON), string(warningsJSON), string(validationJSON))
	return err
}

func (r *AutomationRepo) GetAutomationGraphMetadata(ctx context.Context, projectID, automationID, graphID string) (*models.AutomationGraphMetadata, error) {
	var metadata models.AutomationGraphMetadata
	err := r.db.QueryRowContext(ctx, `SELECT version_id, project_id, automation_id, candidate_json,
		assumptions_json, warnings_json, validation_json, updated_at FROM automation_graph_metadata
		WHERE project_id = ? AND automation_id = ? AND version_id = ?`, projectID, automationID, graphID).
		Scan(&metadata.GraphID, &metadata.ProjectID, &metadata.AutomationID, &metadata.CandidateJSON,
			&metadata.AssumptionsJSON, &metadata.WarningsJSON, &metadata.ValidationJSON, &metadata.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(metadata.AssumptionsJSON), &metadata.Assumptions); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(metadata.WarningsJSON), &metadata.Warnings); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(metadata.ValidationJSON), &metadata.ValidationErrors); err != nil {
		return nil, err
	}
	return &metadata, nil
}
