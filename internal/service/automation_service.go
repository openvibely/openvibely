package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
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

func (s *AutomationRegistrationService) Register(ctx context.Context, req AutomationRegistrationRequest) (definition *models.AutomationDefinition, reused bool, returnErr error) {
	defer func() {
		if returnErr != nil {
			automationobs.Event("automation.registration.validation_failure",
				automationobs.String("project_id", req.ProjectID), automationobs.String("adapter_key", req.AdapterKey))
		}
	}()
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
	existing, err := s.repo.GetByStableKey(ctx, req.ProjectID, stableKey)
	if err != nil {
		return nil, false, err
	}
	if existing != nil && existing.PublishedVersionID != nil {
		definition, reused, returnErr = s.repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
			ProjectID: req.ProjectID, StableKey: stableKey, AdapterKey: adapterKey,
		})
		if returnErr == nil && definition != nil {
			automationobs.Event("automation.registration.completed",
				automationobs.String("automation_id", definition.Automation.ID),
				automationobs.String("version_id", definition.Version.ID),
				automationobs.String("project_id", req.ProjectID),
				automationobs.String("created", fmt.Sprintf("%t", !reused)))
		}
		return definition, reused, returnErr
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
		return nil, false, errors.New("registered automation requires at least one scheduled task with task and schedule bindings")
	}
	taskByNode := make(map[string]string)
	scheduleByNode := make(map[string]string)
	for _, resource := range resources {
		switch resource.ResourceType {
		case "task":
			if _, exists := taskByNode[resource.NodeKey]; exists {
				return nil, false, fmt.Errorf("automation node %q has more than one task binding", resource.NodeKey)
			}
			taskByNode[resource.NodeKey] = resource.ResourceID
		case "schedule":
			if _, exists := scheduleByNode[resource.NodeKey]; exists {
				return nil, false, fmt.Errorf("automation node %q has more than one schedule binding", resource.NodeKey)
			}
			scheduleByNode[resource.NodeKey] = resource.ResourceID
		}
	}
	for nodeKey, scheduleID := range scheduleByNode {
		taskID, ok := taskByNode[nodeKey]
		if !ok {
			return nil, false, fmt.Errorf("scheduled automation node %q requires its task binding on that same node", nodeKey)
		}
		var scheduledTaskID string
		if err := s.repo.DB().QueryRowContext(ctx, `SELECT s.task_id FROM schedules s JOIN tasks t ON t.id = s.task_id WHERE s.id = ? AND t.project_id = ?`, scheduleID, req.ProjectID).Scan(&scheduledTaskID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, false, fmt.Errorf("schedule resource %q does not exist or belongs to another project", scheduleID)
			}
			return nil, false, fmt.Errorf("validate scheduled automation node %q: %w", nodeKey, err)
		}
		if scheduledTaskID != taskID {
			return nil, false, fmt.Errorf("schedule for automation node %q must target the task bound to that same node", nodeKey)
		}
	}
	for nodeKey := range taskByNode {
		if _, ok := scheduleByNode[nodeKey]; !ok {
			return nil, false, fmt.Errorf("scheduled automation node %q requires its schedule binding on that same node", nodeKey)
		}
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
		configJSON, err := s.registeredNodeConfig(ctx, req.ProjectID, adapter, node, taskByNode[node.Key], scheduleByNode[node.Key])
		if err != nil {
			return nil, false, err
		}
		nodes = append(nodes, models.AutomationNodeSpec{Key: node.Key, Name: node.Name, Type: models.AutomationNodeType(node.Type), Role: node.Role, ConfigJSON: configJSON, PositionX: node.X, PositionY: node.Y})
	}
	edges := make([]models.AutomationEdgeSpec, 0, len(adapter.Edges))
	for i, edge := range adapter.Edges {
		edges = append(edges, models.AutomationEdgeSpec{Key: edge.Key, SourceNodeKey: edge.From, TargetNodeKey: edge.To, Label: edge.Label, ConditionJSON: edge.Condition, DisplayOrder: i})
	}
	createdVia := req.CreatedVia
	if createdVia == "" {
		createdVia = "bootstrap"
	}
	definition, reused, returnErr = s.repo.PublishRegistered(ctx, models.AutomationRegisteredPublication{
		ProjectID: req.ProjectID, StableKey: stableKey, Name: name, Description: adapter.Description,
		AutomationType: adapter.AutomationType, AdapterKey: adapter.Key, CreatedVia: createdVia,
		Nodes: nodes, Edges: edges, Resources: resources,
	})
	if returnErr == nil && definition != nil {
		automationobs.Event("automation.registration.completed",
			automationobs.String("automation_id", definition.Automation.ID),
			automationobs.String("version_id", definition.Version.ID),
			automationobs.String("project_id", req.ProjectID),
			automationobs.String("created", fmt.Sprintf("%t", !reused)))
	}
	return definition, reused, returnErr
}

func (s *AutomationRegistrationService) registeredNodeConfig(ctx context.Context, projectID string, adapter AutomationAdapter, node AutomationAdapterNode, taskID, scheduleID string) (string, error) {
	defaults, err := defaultAutomationNodeConfigs(adapter)
	if err != nil {
		return "", fmt.Errorf("build registered node %q defaults: %w", node.Key, err)
	}
	config := defaults[node.Key]
	if config == nil {
		config = map[string]any{}
	}
	if taskID != "" {
		var prompt string
		var category models.TaskCategory
		var priority int
		if err := s.repo.DB().QueryRowContext(ctx, `SELECT prompt, category, priority FROM tasks WHERE id = ? AND project_id = ?`, taskID, projectID).Scan(&prompt, &category, &priority); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("task resource %q does not exist or belongs to another project", taskID)
			}
			return "", fmt.Errorf("load registered task configuration for node %q: %w", node.Key, err)
		}
		config["prompt"] = prompt
		config["category"] = string(category)
		config["priority"] = priority
	}
	if scheduleID != "" {
		var runAt time.Time
		var repeatType models.RepeatType
		var repeatInterval int
		var clearContextOnStart bool
		if err := s.repo.DB().QueryRowContext(ctx, `SELECT s.run_at, s.repeat_type, s.repeat_interval, s.clear_context_on_start
			FROM schedules s JOIN tasks t ON t.id = s.task_id
			WHERE s.id = ? AND t.project_id = ?`, scheduleID, projectID).Scan(&runAt, &repeatType, &repeatInterval, &clearContextOnStart); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return "", fmt.Errorf("schedule resource %q does not exist or belongs to another project", scheduleID)
			}
			return "", fmt.Errorf("load registered schedule configuration for node %q: %w", node.Key, err)
		}
		config["target_node_key"] = adapterScheduleTarget(adapter, node.Key)
		config["run_at"] = runAt.Local().Format("15:04")
		config["repeat_type"] = string(repeatType)
		config["repeat_interval"] = repeatInterval
		config["enabled"] = true
		config["clear_context_on_start"] = clearContextOnStart
	}
	if issues := validateAutomationNodeConfig(adapter, node, models.AutomationDraftNode{
		Key: node.Key, Name: node.Name, Type: models.AutomationNodeType(node.Type), Role: node.Role, Config: config,
	}); len(issues) > 0 {
		return "", fmt.Errorf("registered node %q configuration is invalid: %s", node.Key, issues[0].Message)
	}
	raw, err := json.Marshal(config)
	if err != nil {
		return "", fmt.Errorf("encode registered node %q configuration: %w", node.Key, err)
	}
	return string(raw), nil
}

const automationExternalRefreshCache = time.Minute

type AutomationGraphService struct{ repo *repository.AutomationRepo }

func NewAutomationGraphService(repo *repository.AutomationRepo) *AutomationGraphService {
	return &AutomationGraphService{repo: repo}
}

func (s *AutomationGraphService) List(ctx context.Context, projectID string) ([]models.AutomationCard, error) {
	cards, err := s.repo.ListPortfolioCards(ctx, projectID)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		currentTemplateRevision := CurrentAutomationTemplateRevision(cards[i].Version.AdapterKey)
		cards[i].TemplateUpdateAvailable = currentTemplateRevision > 0 &&
			(cards[i].Automation.TemplateRevision == nil || *cards[i].Automation.TemplateRevision < currentTemplateRevision)
	}
	return cards, nil
}

// ListPage returns one bounded portfolio page while keeping template revision
// enrichment identical to the full portfolio path.
func (s *AutomationGraphService) ListPage(ctx context.Context, projectID string, limit, offset int, search string) ([]models.AutomationCard, error) {
	return s.ListPageFiltered(ctx, projectID, limit, offset, repository.AutomationCardListFilter{Search: search})
}

func (s *AutomationGraphService) ListPageFiltered(ctx context.Context, projectID string, limit, offset int, filter repository.AutomationCardListFilter) ([]models.AutomationCard, error) {
	cards, err := s.repo.ListPortfolioCardsPageFiltered(ctx, projectID, limit, offset, filter)
	if err != nil {
		return nil, err
	}
	for i := range cards {
		currentTemplateRevision := CurrentAutomationTemplateRevision(cards[i].Version.AdapterKey)
		cards[i].TemplateUpdateAvailable = currentTemplateRevision > 0 &&
			(cards[i].Automation.TemplateRevision == nil || *cards[i].Automation.TemplateRevision < currentTemplateRevision)
	}
	return cards, nil
}

func (s *AutomationGraphService) ListBreadcrumbSelector(ctx context.Context, projectID, search, currentID string, limit int) ([]models.BreadcrumbSelectorItem, error) {
	return s.repo.ListBreadcrumbSelector(ctx, projectID, search, currentID, limit)
}

func (s *AutomationGraphService) GetLive(ctx context.Context, projectID, automationID string, now time.Time) (*models.AutomationLiveGraph, error) {
	queryStarted := time.Now()
	definition, err := s.repo.GetDefinition(ctx, projectID, automationID)
	if err != nil || definition == nil {
		return nil, err
	}
	if definition.Version.ID == "" || definition.Version.State != models.AutomationVersionPublished {
		return nil, nil
	}
	cutoff := now.UTC().Add(-24 * time.Hour)
	counts, activeInvocations, activeWorkItems, err := s.repo.LiveNodeCounts(ctx, projectID, automationID, definition.Version.ID, cutoff)
	if err != nil {
		return nil, err
	}
	edgeCounts, err := s.repo.LiveEdgeCounts(ctx, projectID, automationID, definition.Version.ID, cutoff)
	if err != nil {
		return nil, err
	}
	resources, err := s.repo.ListResourceSummaries(ctx, projectID, automationID, definition.Version.ID, 100)
	if err != nil {
		return nil, err
	}
	externalState, err := s.repo.AutomationExternalState(ctx, projectID, automationID, now.UTC().Add(-repository.AutomationExternalStaleAfter))
	if err != nil {
		return nil, err
	}
	graph := &models.AutomationLiveGraph{Automation: definition.Automation, Version: definition.Version,
		Resources: resources, ActiveInvocations: activeInvocations,
		ActiveWorkItems: activeWorkItems, RecentCutoff: cutoff, ExternalState: externalState}
	for _, edge := range definition.Edges {
		values := edgeCounts[edge.ID]
		graph.Edges = append(graph.Edges, models.AutomationLiveEdge{AutomationEdge: edge,
			TransitionCount: values[0], RecentTransitionCount: values[1], Highlighted: values[1] > 0})
	}
	for _, node := range definition.Nodes {
		nodeCounts := counts[node.ID]
		display := "idle"
		switch {
		case nodeCounts.Failed > 0:
			display = "failed"
		case nodeCounts.Blocked > 0:
			display = "blocked"
		case nodeCounts.Running > 0:
			display = "running"
		case nodeCounts.Waiting > 0:
			display = "waiting_human"
		case nodeCounts.CompletedRecently > 0:
			display = "recently_completed"
		}
		graph.Nodes = append(graph.Nodes, models.AutomationLiveNode{AutomationNode: node, Counts: nodeCounts, DisplayState: display})
	}
	encoded, _ := json.Marshal(graph)
	automationobs.Observe("automation.graph.query_duration_ms", time.Since(queryStarted).Milliseconds(),
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", definition.Version.ID))
	automationobs.Observe("automation.graph.payload_bytes", int64(len(encoded)),
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", definition.Version.ID))
	return graph, nil
}

func (s *AutomationGraphService) ListNodeResources(ctx context.Context, projectID, automationID, nodeID string, limit int, cursor string) (*models.AutomationNodeResourcePage, error) {
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
	page, err := s.repo.ListNodeRuntimeResources(ctx, projectID, automationID, definition.Version.ID, nodeID, limit, cursor)
	return &page, err
}

func (s *AutomationGraphService) ContextForThreadInput(ctx context.Context, projectID, inputID string) (models.AutomationContext, error) {
	return s.repo.ContextForThreadInput(ctx, projectID, inputID)
}

func (s *AutomationGraphService) ContextForExecution(ctx context.Context, projectID, executionID string) (models.AutomationContext, error) {
	return s.repo.ContextForExecution(ctx, projectID, executionID)
}

func (s *AutomationGraphService) ContextForTask(ctx context.Context, projectID, taskID string) (models.AutomationContext, error) {
	return s.repo.ContextForTask(ctx, projectID, taskID)
}

func (s *AutomationGraphService) GitHubIssueTaskProvenance(ctx context.Context, projectID, taskID string) (*repository.AutomationGitHubIssueTaskProvenance, error) {
	if s == nil || s.repo == nil {
		return nil, nil
	}
	return s.repo.GitHubIssueTaskProvenance(ctx, projectID, taskID)
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
