package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"

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
	adapter, ok := s.registry.Get(strings.TrimSpace(req.AdapterKey))
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

func (s *AutomationGraphService) GetDefinition(ctx context.Context, projectID, automationID string) (*models.AutomationDefinition, []models.AutomationResourceSummary, error) {
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return definition, nil, err
	}
	resources, err := s.repo.ListResourceSummaries(ctx, projectID, automationID, definition.Version.ID, 100)
	return definition, resources, err
}
