package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type AutomationRegistrationRequest struct {
	ProjectID  string                             `json:"-"`
	AdapterKey string                             `json:"adapter_key"`
	StableKey  string                             `json:"stable_key"`
	Name       string                             `json:"name"`
	Resources  []models.AutomationResourceBinding `json:"resources"`
	CreatedVia string                             `json:"-"`
}

type AutomationRegistrationService struct {
	repo     *repository.AutomationRepo
	registry *AutomationAdapterRegistry
}

func NewAutomationRegistrationService(repo *repository.AutomationRepo, registry *AutomationAdapterRegistry) *AutomationRegistrationService {
	return &AutomationRegistrationService{repo: repo, registry: registry}
}

func (s *AutomationRegistrationService) Register(ctx context.Context, req AutomationRegistrationRequest) (*models.AutomationDefinition, bool, error) {
	if strings.TrimSpace(req.ProjectID) == "" {
		return nil, false, errors.New("automation project is required")
	}
	adapterKey := strings.TrimSpace(req.AdapterKey)
	if adapterKey != AutomationAdapterNativeSDLC && adapterKey != AutomationAdapterGitHubSDLC {
		return nil, false, fmt.Errorf("unsupported maintained automation adapter %q", req.AdapterKey)
	}
	adapter, ok := s.registry.Get(adapterKey)
	if !ok {
		return nil, false, fmt.Errorf("unsupported maintained automation adapter %q", req.AdapterKey)
	}
	stableKey := strings.TrimSpace(req.StableKey)
	if stableKey == "" || len(stableKey) > 120 {
		return nil, false, errors.New("automation stable key is required and must not exceed 120 characters")
	}
	if len(req.Resources) == 0 || len(req.Resources) > 100 {
		return nil, false, errors.New("registered automation requires between 1 and 100 resource bindings")
	}
	resources := append([]models.AutomationResourceBinding(nil), req.Resources...)
	seen := make(map[string]struct{}, len(resources))
	hasSchedule, hasTask := false, false
	for i := range resources {
		resources[i].NodeKey = strings.TrimSpace(resources[i].NodeKey)
		resources[i].ResourceType = strings.TrimSpace(resources[i].ResourceType)
		resources[i].ResourceID = strings.TrimSpace(resources[i].ResourceID)
		resources[i].Relation = strings.TrimSpace(resources[i].Relation)
		if resources[i].Relation == "" {
			resources[i].Relation = "owned"
		}
		if resources[i].Relation != "owned" && resources[i].Relation != "shared" {
			return nil, false, fmt.Errorf("resource binding %q has unsupported relation %q", resources[i].NodeKey, resources[i].Relation)
		}
		if err := adapter.ValidateBinding(resources[i].NodeKey, resources[i].ResourceType); err != nil {
			return nil, false, err
		}
		if resources[i].ResourceID == "" {
			return nil, false, fmt.Errorf("resource binding %q requires an ID", resources[i].NodeKey)
		}
		key := strings.Join([]string{resources[i].NodeKey, resources[i].ResourceType, resources[i].ResourceID, resources[i].Relation}, "\x00")
		if _, duplicate := seen[key]; duplicate {
			return nil, false, fmt.Errorf("duplicate automation resource binding for node %q", resources[i].NodeKey)
		}
		seen[key] = struct{}{}
		hasSchedule = hasSchedule || resources[i].ResourceType == "schedule"
		if resources[i].ResourceType == "schedule" && resources[i].Relation != "owned" {
			return nil, false, fmt.Errorf("trigger schedule %q must use exclusive owned relation", resources[i].ResourceID)
		}
		hasTask = hasTask || resources[i].ResourceType == "task"
	}
	if !hasSchedule || !hasTask {
		return nil, false, errors.New("registered automation requires at least one trigger schedule and one visible task")
	}
	sort.Slice(resources, func(i, j int) bool {
		left := resources[i].NodeKey + "\x00" + resources[i].ResourceType + "\x00" + resources[i].ResourceID + "\x00" + resources[i].Relation
		right := resources[j].NodeKey + "\x00" + resources[j].ResourceType + "\x00" + resources[j].ResourceID + "\x00" + resources[j].Relation
		return left < right
	})

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = adapter.DefaultName
	}
	if len(name) > 200 {
		return nil, false, errors.New("automation name must not exceed 200 characters")
	}
	nodes := make([]models.AutomationNodeSpec, 0, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		nodes = append(nodes, models.AutomationNodeSpec{Key: node.Key, Name: node.Name, Type: models.AutomationNodeType(node.Type), Role: node.Role, ConfigJSON: "{}", PositionX: node.X, PositionY: node.Y})
	}
	edges := make([]models.AutomationEdgeSpec, 0, len(adapter.Edges))
	for i, edge := range adapter.Edges {
		edges = append(edges, models.AutomationEdgeSpec{Key: edge.Key, SourceNodeKey: edge.From, TargetNodeKey: edge.To, Label: edge.Label, ConditionJSON: edge.Condition, DisplayOrder: i})
	}
	createdVia := req.CreatedVia
	if createdVia == "" {
		createdVia = "bootstrap"
	}
	return s.repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: req.ProjectID, StableKey: stableKey, Name: name, Description: adapter.Description,
		AutomationType: adapter.AutomationType, AdapterKey: adapter.Key, CreatedVia: createdVia,
		Nodes: nodes, Edges: edges, Resources: resources,
	})
}

type AutomationGraphService struct{ repo *repository.AutomationRepo }

func NewAutomationGraphService(repo *repository.AutomationRepo) *AutomationGraphService {
	return &AutomationGraphService{repo: repo}
}

func (s *AutomationGraphService) List(ctx context.Context, projectID string) ([]models.AutomationCard, error) {
	automations, err := s.repo.ListByProject(ctx, projectID, 100)
	if err != nil {
		return nil, err
	}
	cards := make([]models.AutomationCard, 0, len(automations))
	for _, automation := range automations {
		definition, err := s.repo.GetDefinition(ctx, projectID, automation.ID)
		if err != nil {
			return nil, err
		}
		if definition == nil {
			continue
		}
		if definition.Version.ID == "" {
			metadata, err := s.repo.GetLatestAutomationDraftMetadata(ctx, projectID, automation.ID)
			if err != nil {
				return nil, err
			}
			if metadata != nil {
				definition, err = s.repo.GetDefinitionVersion(ctx, projectID, automation.ID, metadata.VersionID)
				if err != nil {
					return nil, err
				}
				if definition == nil {
					continue
				}
			}
		}
		card := models.AutomationCard{Automation: definition.Automation, Version: definition.Version}
		if definition.Version.ID != "" {
			card.Resources, err = s.repo.ListResourceSummaries(ctx, projectID, automation.ID, definition.Version.ID, 12)
			if err != nil {
				return nil, err
			}
			for _, resource := range definition.Resources {
				if resource.ResourceType != "schedule" {
					continue
				}
				var nextRun, lastRun sql.NullTime
				if err := s.repo.DB().QueryRowContext(ctx, `SELECT next_run, last_run FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE s.id = ? AND t.project_id = ?`, resource.ResourceID, projectID).Scan(&nextRun, &lastRun); err == nil {
					if nextRun.Valid && (card.NextRun == nil || nextRun.Time.Before(*card.NextRun)) {
						t := nextRun.Time
						card.NextRun = &t
					}
					if lastRun.Valid && (card.LastRun == nil || lastRun.Time.After(*card.LastRun)) {
						t := lastRun.Time
						card.LastRun = &t
					}
				}
			}
		}
		cards = append(cards, card)
	}
	return cards, nil
}

func (s *AutomationGraphService) GetLive(ctx context.Context, projectID, automationID string, now time.Time) (*models.AutomationLiveGraph, error) {
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return nil, err
	}
	cutoff := now.UTC().Add(-24 * time.Hour)
	counts, activeInvocations, activeWorkItems, err := s.repo.LiveNodeCounts(ctx, projectID, automationID, definition.Version.ID, cutoff)
	if err != nil {
		return nil, err
	}
	olderCounts, legacyWork, err := s.repo.LiveOlderVersionPositions(ctx, projectID, automationID, definition.Version.ID)
	if err != nil {
		return nil, err
	}
	edgeCounts, err := s.repo.LiveEdgeCounts(ctx, projectID, automationID, definition.Version.ID, cutoff)
	if err != nil {
		return nil, err
	}
	resources, err := s.repo.ListResourceSummaries(ctx, projectID, automationID, definition.Version.ID, 50)
	if err != nil {
		return nil, err
	}
	graph := &models.AutomationLiveGraph{Automation: definition.Automation, Version: definition.Version,
		Resources: resources, ActiveInvocations: activeInvocations,
		ActiveWorkItems: activeWorkItems, RecentCutoff: cutoff, LegacyWork: legacyWork}
	for _, edge := range definition.Edges {
		values := edgeCounts[edge.ID]
		graph.Edges = append(graph.Edges, models.AutomationLiveEdge{AutomationEdge: edge,
			TransitionCount: values[0], RecentTransitionCount: values[1], Highlighted: values[1] > 0})
	}
	for _, node := range definition.Nodes {
		nodeCounts := counts[node.ID]
		older := olderCounts[node.ID]
		nodeCounts.Running += older.Running
		nodeCounts.Waiting += older.Waiting
		nodeCounts.Blocked += older.Blocked
		nodeCounts.Failed += older.Failed
		display := "idle"
		switch {
		case nodeCounts.Failed > 0:
			display = "failed"
		case nodeCounts.Blocked > 0:
			display = "blocked"
		case nodeCounts.Waiting > 0:
			display = "waiting_human"
		case nodeCounts.Running > 0:
			display = "running"
		case nodeCounts.CompletedRecently > 0:
			display = "recently_completed"
		}
		graph.Nodes = append(graph.Nodes, models.AutomationLiveNode{AutomationNode: node, Counts: nodeCounts, DisplayState: display})
	}
	return graph, nil
}

func (s *AutomationGraphService) ListNodeResources(ctx context.Context, projectID, automationID, nodeID string, limit int) ([]models.AutomationNodeResource, error) {
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return nil, err
	}
	found := false
	for _, node := range definition.Nodes {
		if node.ID == nodeID {
			found = true
			break
		}
	}
	if !found {
		return nil, nil
	}
	return s.repo.ListNodeRuntimeResources(ctx, projectID, automationID, definition.Version.ID, nodeID, limit)
}

func (s *AutomationGraphService) ContextForThreadInput(ctx context.Context, projectID, inputID string) (models.AutomationContext, error) {
	return s.repo.ContextForThreadInput(ctx, projectID, inputID)
}

func (s *AutomationGraphService) ContextForExecution(ctx context.Context, projectID, executionID string) (models.AutomationContext, error) {
	return s.repo.ContextForExecution(ctx, projectID, executionID)
}

func (s *AutomationGraphService) GetDefinition(ctx context.Context, projectID, automationID string) (*models.AutomationDefinition, []models.AutomationResourceSummary, error) {
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return definition, nil, err
	}
	resources, err := s.repo.ListResourceSummaries(ctx, projectID, automationID, definition.Version.ID, 100)
	return definition, resources, err
}

func (s *AutomationGraphService) ListInvocations(ctx context.Context, projectID, automationID string, limit int, cursor string) (models.AutomationInvocationPage, error) {
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return models.AutomationInvocationPage{}, err
	}
	return s.repo.ListAutomationInvocations(ctx, projectID, automationID, limit, cursor)
}

func (s *AutomationGraphService) ListWorkItems(ctx context.Context, projectID, automationID, status string, limit int, cursor string) (models.AutomationWorkItemPage, error) {
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return models.AutomationWorkItemPage{}, err
	}
	return s.repo.ListAutomationWorkItems(ctx, projectID, automationID, status, limit, cursor)
}

func (s *AutomationGraphService) GetInvocationHistory(ctx context.Context, projectID, automationID, invocationID string, limit int, transitionCursor, activityCursor string) (*models.AutomationInvocationHistory, error) {
	invocation, err := s.repo.GetAutomationInvocation(ctx, projectID, automationID, invocationID)
	if err != nil || invocation == nil {
		return nil, err
	}
	definition, err := s.repo.GetDefinitionVersion(ctx, projectID, automationID, invocation.VersionID)
	if err != nil || definition == nil {
		return nil, err
	}
	activities, err := s.repo.ListAutomationActivities(ctx, projectID, automationID, invocationID, "", limit, activityCursor)
	if err != nil {
		return nil, err
	}
	transitions, err := s.repo.ListAutomationTransitions(ctx, projectID, automationID, invocationID, "", limit, transitionCursor)
	if err != nil {
		return nil, err
	}
	touchedNodeIDs, err := s.repo.ListAutomationInvocationNodeIDs(ctx, projectID, automationID, invocationID, 100)
	if err != nil {
		return nil, err
	}
	return &models.AutomationInvocationHistory{Invocation: *invocation, Definition: *definition, Activities: activities,
		Transitions: transitions, TouchedNodeIDs: touchedNodeIDs}, nil
}

func (s *AutomationGraphService) GetWorkItemHistory(ctx context.Context, projectID, automationID, workItemID string, limit int, transitionCursor, activityCursor string) (*models.AutomationWorkItemHistory, error) {
	item, err := s.repo.GetAutomationWorkItem(ctx, projectID, automationID, workItemID)
	if err != nil || item == nil {
		return nil, err
	}
	definition, err := s.repo.GetDefinitionVersion(ctx, projectID, automationID, item.OriginVersionID)
	if err != nil || definition == nil {
		return nil, err
	}
	activities, err := s.repo.ListAutomationActivities(ctx, projectID, automationID, "", workItemID, limit, activityCursor)
	if err != nil {
		return nil, err
	}
	transitions, err := s.repo.ListAutomationTransitions(ctx, projectID, automationID, "", workItemID, limit, transitionCursor)
	if err != nil {
		return nil, err
	}
	replay, err := s.repo.ReplayAutomationTransitionPage(ctx, projectID, automationID, workItemID, transitionCursor, transitions.Items)
	if err != nil {
		return nil, err
	}
	return &models.AutomationWorkItemHistory{WorkItem: *item, Definition: *definition, Activities: activities,
		Transitions: transitions, Replay: replay}, nil
}

func (s *AutomationGraphService) GetHistoryDashboard(ctx context.Context, projectID, automationID, invocationCursor, workItemStatus, workItemCursor string, now time.Time) (*models.AutomationHistoryDashboard, error) {
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return nil, err
	}
	invocations, err := s.repo.ListAutomationInvocations(ctx, projectID, automationID, 20, invocationCursor)
	if err != nil {
		return nil, err
	}
	workItems, err := s.repo.ListAutomationWorkItems(ctx, projectID, automationID, workItemStatus, 20, workItemCursor)
	if err != nil {
		return nil, err
	}
	metrics, err := s.repo.GetAutomationMetrics(ctx, projectID, automationID, definition.Version.ID, now)
	if err != nil {
		return nil, err
	}
	health, err := s.repo.RecomputeAutomationHealth(ctx, projectID, automationID, now)
	if err != nil {
		return nil, err
	}
	definition.Automation.HealthState = health.State
	definition.Automation.HealthReason = health.Reason
	definition.Automation.HealthEvaluatedAt = &health.EvaluatedAt
	return &models.AutomationHistoryDashboard{Automation: definition.Automation, Invocations: invocations,
		WorkItems: workItems, Metrics: metrics, Health: health}, nil
}
