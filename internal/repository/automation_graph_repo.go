package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

type AutomationGraphWrite struct {
	ProjectID        string
	AutomationID     string
	GraphID          string
	Candidate        models.AutomationDraftCandidate
	ValidationErrors []models.AutomationValidationIssue
}

func writeAutomationGraph(ctx context.Context, conn *sql.Conn, in AutomationGraphWrite) (map[string]string, error) {
	nodes := make([]models.AutomationNodeSpec, 0, len(in.Candidate.Nodes))
	for _, node := range in.Candidate.Nodes {
		config, err := json.Marshal(node.Config)
		if err != nil {
			return nil, err
		}
		x, y := 0.0, 0.0
		if node.Position != nil {
			x, y = node.Position.X, node.Position.Y
		}
		nodes = append(nodes, models.AutomationNodeSpec{Key: node.Key, Name: node.Name, Type: node.Type, Role: node.Role,
			ConfigJSON: string(config), PositionX: x, PositionY: y})
	}
	edges := make([]models.AutomationEdgeSpec, 0, len(in.Candidate.Edges))
	for index, edge := range in.Candidate.Edges {
		condition, err := json.Marshal(edge.Condition)
		if err != nil {
			return nil, err
		}
		edges = append(edges, models.AutomationEdgeSpec{Key: edge.Key, SourceNodeKey: edge.From, TargetNodeKey: edge.To,
			Label: edge.Label, ConditionJSON: string(condition), DisplayOrder: index})
	}
	nodeIDs, err := writeAutomationGraphRows(ctx, conn, in.ProjectID, in.AutomationID, in.GraphID, nodes, edges)
	if err != nil {
		return nil, err
	}
	candidateJSON, _ := json.Marshal(in.Candidate)
	assumptionsJSON, _ := json.Marshal(in.Candidate.Assumptions)
	warningsJSON, _ := json.Marshal(in.Candidate.Warnings)
	validationJSON, _ := json.Marshal(in.ValidationErrors)
	_, err = conn.ExecContext(ctx, `INSERT INTO automation_graph_metadata
		(version_id, project_id, automation_id, candidate_json, assumptions_json, warnings_json, validation_json)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, in.GraphID, in.ProjectID, in.AutomationID, string(candidateJSON),
		string(assumptionsJSON), string(warningsJSON), string(validationJSON))
	if err != nil {
		return nil, err
	}
	return nodeIDs, nil
}

func writeAutomationGraphRows(ctx context.Context, conn *sql.Conn, projectID, automationID, versionID string, nodes []models.AutomationNodeSpec, edges []models.AutomationEdgeSpec) (map[string]string, error) {
	nodeIDs := make(map[string]string, len(nodes))
	for _, node := range nodes {
		nodeID := NewID()
		config := node.ConfigJSON
		if strings.TrimSpace(config) == "" {
			config = "{}"
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_nodes
			(id, project_id, automation_id, version_id, node_key, name, node_type, role, config_json, position_x, position_y)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, nodeID, projectID, automationID, versionID,
			node.Key, node.Name, node.Type, node.Role, config, node.PositionX, node.PositionY); err != nil {
			return nil, fmt.Errorf("creating automation node %q: %w", node.Key, err)
		}
		nodeIDs[node.Key] = nodeID
	}
	for _, edge := range edges {
		sourceID, sourceOK := nodeIDs[edge.SourceNodeKey]
		targetID, targetOK := nodeIDs[edge.TargetNodeKey]
		if !sourceOK || !targetOK {
			return nil, fmt.Errorf("edge %q references an unknown node", edge.Key)
		}
		condition := edge.ConditionJSON
		if strings.TrimSpace(condition) == "" {
			condition = "{}"
		}
		if _, err := conn.ExecContext(ctx, `INSERT INTO automation_edges
			(id, project_id, automation_id, version_id, source_node_id, target_node_id, edge_key, label, condition_json, display_order)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, NewID(), projectID, automationID, versionID,
			sourceID, targetID, edge.Key, edge.Label, condition, edge.DisplayOrder); err != nil {
			return nil, fmt.Errorf("creating automation edge %q: %w", edge.Key, err)
		}
	}
	return nodeIDs, nil
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
