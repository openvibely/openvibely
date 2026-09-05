package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type automationTaskMutationService interface {
	Create(context.Context, *models.Task) error
	Update(context.Context, *models.Task) error
}

type automationTaskSubmissionService interface {
	SubmitSavedAutomationTask(models.Task)
}

type automationQueuedWorkPruner interface {
	PruneQueuedWork()
}

type automationSaveAgentResolver func(context.Context, string, []AutomationAdapterNode, map[string]models.AutomationDraftNode) (map[string]string, error)

type AutomationCompiler struct {
	automationRepo *repository.AutomationRepo
	taskSvc        automationTaskMutationService
	taskRepo       *repository.TaskRepo
	agentRepo      *repository.AgentRepo
	// saveAgentResolver is nil in production; tests may replace it to replay a
	// pre-optimization resolver through the same atomic Save path.
	saveAgentResolver automationSaveAgentResolver
	scheduleRepo      *repository.ScheduleRepo
	validator         *AutomationSaveValidator
	now               func() time.Time
}

type AutomationSaveRequest struct {
	ProjectID              string
	AutomationID           string
	StableKey              string
	Source                 string
	CreatedVia             string
	Candidate              models.AutomationDraftCandidate
	ConfirmationTokenID    string
	ConfirmationPrincipal  string
	ConfirmationThreadID   string
	ConfirmingUserInputID  string
	UpdateToLatestTemplate bool
	validatedCandidate     bool
}

type AutomationSaveResult struct {
	Definition *models.AutomationDefinition `json:"definition"`
}

func NewAutomationCompiler(automationRepo *repository.AutomationRepo, taskSvc automationTaskMutationService, taskRepo *repository.TaskRepo, scheduleRepo *repository.ScheduleRepo, validator *AutomationSaveValidator) *AutomationCompiler {
	return &AutomationCompiler{automationRepo: automationRepo, taskSvc: taskSvc, taskRepo: taskRepo, scheduleRepo: scheduleRepo, validator: validator, now: time.Now}
}

func (c *AutomationCompiler) SetAgentRepository(agentRepo *repository.AgentRepo) {
	c.agentRepo = agentRepo
}

func (c *AutomationCompiler) validateSaveCandidate(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) (models.AutomationDraftCandidate, []models.AutomationValidationIssue, error) {
	if c == nil || c.validator == nil || c.validator.drafts == nil || c.validator.registry == nil {
		return candidate, nil, errors.New("automation save is unavailable")
	}
	normalized, err := c.validator.drafts.NormalizeCandidate(candidate)
	if err != nil {
		return candidate, nil, err
	}
	issues, err := c.validator.drafts.validateCandidateForProject(ctx, projectID, normalized)
	if err != nil {
		return normalized, nil, err
	}
	capabilityIssues, err := c.validator.capabilityIssues(ctx, projectID, normalized)
	if err != nil {
		return normalized, nil, err
	}
	issues = append(issues, capabilityIssues...)
	if c.validator.drafts.capabilities == nil {
		agentIssues, err := c.validator.agentIssues(ctx, projectID, normalized)
		if err != nil {
			return normalized, nil, err
		}
		issues = append(issues, agentIssues...)
	}
	return normalized, issues, nil
}

func (c *AutomationCompiler) PreviewSave(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) (*models.AutomationSavePlan, models.AutomationDraftCandidate, error) {
	normalized, issues, err := c.validateSaveCandidate(ctx, projectID, candidate)
	if err != nil {
		return nil, normalized, err
	}
	plan := &models.AutomationSavePlan{Validation: issues, WillNot: []string{"merge pull requests", "release software", "deploy software"}}
	if len(issues) > 0 {
		automationobs.Event("automation.save.validation_failure", automationobs.String("project_id", projectID), automationobs.String("adapter_key", normalized.AdapterKey))
		return plan, normalized, nil
	}
	adapter, _ := c.validator.registry.Get(normalized.AdapterKey)
	resourceNodes := automationResourceNodes(adapter, normalized)
	for _, node := range resourceNodes {
		if node.AllowedResources["task"] {
			plan.Effects = append(plan.Effects, models.AutomationSaveEffect{Operation: "create", ResourceType: "task", Name: node.Name})
		}
		if node.AllowedResources["schedule"] {
			plan.Effects = append(plan.Effects, models.AutomationSaveEffect{Operation: "create", ResourceType: "schedule", Name: node.Name})
		}
	}
	return plan, normalized, nil
}

// SaveValidatedCandidate applies a candidate that has already passed PreviewSave.
// It preserves Save's atomic materialization and skips only the duplicate validation pass.
func (c *AutomationCompiler) SaveValidatedCandidate(ctx context.Context, request AutomationSaveRequest) (*AutomationSaveResult, error) {
	request.validatedCandidate = true
	return c.Save(ctx, request)
}

func automationResourceNodes(adapter AutomationAdapter, candidate models.AutomationDraftCandidate) []AutomationAdapterNode {
	canonical := make(map[string]AutomationAdapterNode, len(adapter.Nodes))
	for _, node := range adapter.Nodes {
		canonical[node.Key] = node
	}
	resources := make([]AutomationAdapterNode, 0, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		if resource, exists := canonical[node.Key]; exists {
			resources = append(resources, resource)
			continue
		}
		if !adapter.DynamicTopology && adapter.Key != AutomationAdapterNativeSDLC && adapter.Key != AutomationAdapterGitHubSDLC {
			continue
		}
		resource := AutomationAdapterNode{Key: node.Key, Name: node.Name, Type: string(node.Type), Role: node.Role, AllowedResources: map[string]bool{}}
		if customAutomationNodeMaterializesTask(candidate, node) {
			resource.AllowedResources["task"] = true
		}
		if node.Type == models.AutomationNodeTrigger {
			resource.AllowedResources["schedule"] = true
		}
		resources = append(resources, resource)
	}
	return resources
}

func automationNodeUsesCustomTopology(adapter AutomationAdapter, nodeKey string) bool {
	if adapter.DynamicTopology {
		return true
	}
	for _, node := range adapter.Nodes {
		if node.Key == nodeKey {
			return false
		}
	}
	return adapter.Key == AutomationAdapterNativeSDLC || adapter.Key == AutomationAdapterGitHubSDLC
}

func automationCandidateNodeUsesCustomTopology(candidate models.AutomationDraftCandidate, nodeKey string) bool {
	adapter, ok := NewAutomationAdapterRegistry().Get(candidate.AdapterKey)
	return ok && automationNodeUsesCustomTopology(adapter, nodeKey)
}

func customAutomationNodeMaterializesTask(candidate models.AutomationDraftCandidate, node models.AutomationDraftNode) bool {
	if node.Type == models.AutomationNodeTrigger {
		return true
	}
	return node.Type == models.AutomationNodeAgentTask &&
		!customAutomationGitHubIssueTask(candidate, node.Key) &&
		!customAutomationNativeImplementation(candidate, node.Key)
}

// Save validates and applies one complete Automation graph in a single SQLite
// transaction. It creates no intermediate persistent state.
func (c *AutomationCompiler) Save(ctx context.Context, request AutomationSaveRequest) (*AutomationSaveResult, error) {
	if c == nil || c.automationRepo == nil || c.validator == nil || c.validator.drafts == nil || c.validator.registry == nil || c.taskRepo == nil || c.scheduleRepo == nil {
		return nil, errors.New("automation save is unavailable")
	}
	if strings.TrimSpace(request.ProjectID) == "" {
		return nil, errors.New("project is required")
	}
	candidate := request.Candidate
	if !request.validatedCandidate {
		var issues []models.AutomationValidationIssue
		var err error
		candidate, issues, err = c.validateSaveCandidate(ctx, request.ProjectID, request.Candidate)
		if err != nil {
			return nil, err
		}
		if len(issues) > 0 {
			return nil, fmt.Errorf("automation graph validation failed: %s", issues[0].Message)
		}
		request.Candidate = candidate
	}

	automationID := strings.TrimSpace(request.AutomationID)
	if automationID == "" {
		automationID = repository.NewID()
	}
	current, err := c.automationRepo.GetDefinition(ctx, request.ProjectID, automationID)
	if err != nil {
		return nil, err
	}
	if candidate.AdapterKey == AutomationAdapterVisionDriver && current == nil {
		return nil, errors.New("Vision Driver cannot be created; use Native SDLC or GitHub SDLC for new Automations")
	}
	expectedGraphID := ""
	automation := models.Automation{ID: automationID, ProjectID: request.ProjectID, Name: candidate.Name,
		Description: candidate.Description, AutomationType: candidate.AutomationType, LifecycleState: models.AutomationActive}
	existingResources := map[string]models.AutomationDefinitionResource{}
	if current != nil {
		automation = current.Automation
		automation.Name = candidate.Name
		expectedGraphID = current.Version.ID
		for _, resource := range current.Resources {
			existingResources[resource.NodeKey+"\x00"+resource.ResourceType] = resource
		}
	}

	adapter, ok := c.validator.registry.Get(candidate.AdapterKey)
	if !ok {
		return nil, fmt.Errorf("unsupported automation adapter %q", candidate.AdapterKey)
	}
	templateRevision := automation.TemplateRevision
	if request.UpdateToLatestTemplate {
		if current == nil || adapter.TemplateRevision == 0 {
			return nil, errors.New("only an existing maintained template Automation can update to the latest template")
		}
		templateRevision = &adapter.TemplateRevision
	} else if current == nil && request.Source == "template" && adapter.TemplateRevision > 0 {
		templateRevision = &adapter.TemplateRevision
	}
	candidateNodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		candidateNodes[node.Key] = node
	}
	resourceNodes := automationResourceNodes(adapter, candidate)
	resolveAgentDefinitions := c.resolveSaveAgentDefinitions
	if c.saveAgentResolver != nil {
		resolveAgentDefinitions = c.saveAgentResolver
	}
	agentDefinitionIDs, err := resolveAgentDefinitions(ctx, request.ProjectID, resourceNodes, candidateNodes)
	if err != nil {
		return nil, err
	}

	write := repository.AutomationSaveWrite{ProjectID: request.ProjectID, AutomationID: automationID, GraphID: repository.NewID(),
		ExpectedCurrentGraphID: expectedGraphID, StableKey: request.StableKey, Source: request.Source,
		CreatedVia: request.CreatedVia, TemplateRevision: templateRevision, Candidate: candidate, ConfirmationTokenID: request.ConfirmationTokenID,
		ConfirmationPrincipal: request.ConfirmationPrincipal, ConfirmationThreadID: request.ConfirmationThreadID,
		ConfirmingUserInputID: request.ConfirmingUserInputID}
	for _, resourceNode := range resourceNodes {
		if !resourceNode.AllowedResources["task"] {
			continue
		}
		node := candidateNodes[resourceNode.Key]
		prompt, category, priority := automationNodeTaskConfiguration(candidate, node)
		var agentDefinitionID *string
		if ref, _ := node.Config["agent_ref"].(string); strings.TrimSpace(ref) != "" {
			if resolvedID := agentDefinitionIDs[strings.TrimSpace(ref)]; resolvedID != "" {
				agentDefinitionID = &resolvedID
			}
		}
		modelConfigID := automationExplicitModelConfigID(node.Config["model_config_id"])
		var agentID *string
		if modelConfigID != "" {
			agentID = &modelConfigID
		}
		goal, _ := node.Config["goal"].(string)
		taskWrite := repository.AutomationSaveTask{
			NodeKey:           node.Key,
			Title:             automationTaskTitle(automation, node),
			Prompt:            prompt,
			Goal:              strings.TrimSpace(goal),
			Category:          category,
			Priority:          priority,
			AgentID:           agentID,
			AgentDefinitionID: agentDefinitionID,
		}
		if existing := existingResources[node.Key+"\x00task"]; existing.ResourceID != "" {
			taskWrite.ExistingTaskID = existing.ResourceID
		}
		if automationNodeUsesCustomTopology(adapter, node.Key) {
			taskWrite.ApplyTopology = true
			taskWrite.ParentNodeKey, _ = customAutomationTaskNeighbors(candidate, node.Key)
			if _, child := customAutomationTaskNeighbors(candidate, node.Key); child != nil {
				taskWrite.ChildNodeKey = child.Key
				taskWrite.ChildTitle = automationTaskTitle(automation, *child)
				taskWrite.ChildPromptPrefix = automationCompiledTaskPrompt(candidate, *child)
				childCategory, _ := child.Config["category"].(string)
				taskWrite.ChildCategory = models.TaskCategory(childCategory)
				if parentKey, _ := customAutomationTaskNeighbors(candidate, child.Key); parentKey != "" {
					if parent := candidateNodes[parentKey]; parent.Type == models.AutomationNodeTrigger {
						taskWrite.ChildCategory = models.CategoryActive
					}
				}
			}
		}
		write.Tasks = append(write.Tasks, taskWrite)
	}
	for _, resourceNode := range resourceNodes {
		if !resourceNode.AllowedResources["schedule"] {
			continue
		}
		node := candidateNodes[resourceNode.Key]
		_, clearContextConfigured := node.Config["clear_context_on_start"]
		node.Config["enabled"] = true
		for i := range write.Candidate.Nodes {
			if write.Candidate.Nodes[i].Key == node.Key {
				write.Candidate.Nodes[i].Config["enabled"] = true
				break
			}
		}
		taskNodeKey := node.Key
		if !automationNodeUsesCustomTopology(adapter, node.Key) {
			taskNodeKey, _ = node.Config["target_node_key"].(string)
		}
		schedule, err := c.scheduleFromNode("", node)
		if err != nil {
			return nil, err
		}
		scheduleWrite := repository.AutomationSaveSchedule{NodeKey: node.Key, TaskNodeKey: taskNodeKey, RunAt: schedule.RunAt,
			RepeatType: schedule.RepeatType, RepeatInterval: schedule.RepeatInterval, Enabled: schedule.Enabled, ClearContextOnStart: schedule.ClearContextOnStart}
		if existing := existingResources[node.Key+"\x00schedule"]; existing.ResourceID != "" {
			scheduleWrite.ExistingScheduleID = existing.ResourceID
			stored, err := c.scheduleRepo.GetByID(ctx, existing.ResourceID)
			if err != nil {
				return nil, err
			}
			if stored != nil {
				if !clearContextConfigured {
					scheduleWrite.ClearContextOnStart = stored.ClearContextOnStart
					for i := range write.Candidate.Nodes {
						if write.Candidate.Nodes[i].Key == node.Key {
							write.Candidate.Nodes[i].Config["clear_context_on_start"] = stored.ClearContextOnStart
							break
						}
					}
				}
				scheduleWrite.PreserveTiming = stored.RunAt.In(time.Local).Format("15:04") == schedule.RunAt.In(time.Local).Format("15:04") &&
					stored.RepeatType == schedule.RepeatType && stored.RepeatInterval == schedule.RepeatInterval
			}
		}
		write.Schedules = append(write.Schedules, scheduleWrite)
	}

	definition, runnable, err := c.automationRepo.SaveCurrentGraph(ctx, write)
	if err != nil {
		return nil, err
	}
	if submitter, ok := c.taskSvc.(automationTaskSubmissionService); ok {
		for _, task := range runnable {
			submitter.SubmitSavedAutomationTask(task)
		}
	}
	automationobs.Event("automation.saved", automationobs.String("project_id", request.ProjectID),
		automationobs.String("automation_id", automationID), automationobs.String("graph_id", write.GraphID))
	return &AutomationSaveResult{Definition: definition}, nil
}

// resolveSaveAgentDefinitions loads the compact selectable Agent identities once
// for this Save and preserves the catalog's ordered first-match semantics.
func (c *AutomationCompiler) resolveSaveAgentDefinitions(ctx context.Context, projectID string, resourceNodes []AutomationAdapterNode, candidateNodes map[string]models.AutomationDraftNode) (map[string]string, error) {
	type agentReference struct {
		nodeKey string
		ref     string
	}

	references := make([]agentReference, 0)
	for _, resourceNode := range resourceNodes {
		if !resourceNode.AllowedResources["task"] {
			continue
		}
		node := candidateNodes[resourceNode.Key]
		ref, _ := node.Config["agent_ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		references = append(references, agentReference{nodeKey: node.Key, ref: ref})
	}
	if len(references) == 0 {
		return nil, nil
	}
	if c.agentRepo == nil {
		return nil, fmt.Errorf("Agent selection for node %q is unavailable in this project", references[0].nodeKey)
	}

	available, err := c.agentRepo.ListSelectableReferencesForProject(ctx, projectID, automationCapabilityLimit)
	if err != nil {
		return nil, err
	}
	resolved := make(map[string]string, len(available))
	for _, reference := range available {
		key := strings.TrimSpace(reference.Key)
		if key == "" {
			key = reference.ID
		}
		if key == "" || (reference.ProjectID != "" && reference.ProjectID != projectID) {
			continue
		}
		if _, exists := resolved[key]; !exists {
			resolved[key] = reference.ID
		}
	}
	for _, reference := range references {
		if resolved[reference.ref] == "" {
			return nil, fmt.Errorf("Agent selection for node %q is unavailable in this project", reference.nodeKey)
		}
	}
	return resolved, nil
}

func (c *AutomationCompiler) scheduleFromNode(taskID string, node models.AutomationDraftNode) (models.Schedule, error) {
	runAtText, _ := node.Config["run_at"].(string)
	clock, err := time.ParseInLocation("15:04", runAtText, time.Local)
	if err != nil {
		return models.Schedule{}, fmt.Errorf("invalid trigger time for %q", node.Key)
	}
	now := c.now().In(time.Local)
	runAt := time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), 0, 0, time.Local)
	if !runAt.After(now) {
		runAt = runAt.AddDate(0, 0, 1)
	}
	repeat, _ := node.Config["repeat_type"].(string)
	interval, _ := draftInt(node.Config["repeat_interval"])
	if err := models.ValidateScheduleRepeatInterval(interval); err != nil {
		return models.Schedule{}, fmt.Errorf("invalid repeat interval for %q: %w", node.Key, err)
	}
	clearContextOnStart, present := node.Config["clear_context_on_start"].(bool)
	if !present {
		clearContextOnStart = true
	}
	return models.Schedule{TaskID: taskID, RunAt: runAt.UTC(), RepeatType: models.RepeatType(repeat), RepeatInterval: interval, Enabled: true, ClearContextOnStart: clearContextOnStart}, nil
}

type AutomationLifecycleService struct {
	repo         *repository.AutomationRepo
	scheduleRepo *repository.ScheduleRepo
	taskSvc      automationTaskMutationService
}

func NewAutomationLifecycleService(repo *repository.AutomationRepo, scheduleRepo *repository.ScheduleRepo, taskSvc ...automationTaskMutationService) *AutomationLifecycleService {
	service := &AutomationLifecycleService{repo: repo, scheduleRepo: scheduleRepo}
	if len(taskSvc) > 0 {
		service.taskSvc = taskSvc[0]
	}
	return service
}

func (s *AutomationLifecycleService) RunNow(ctx context.Context, projectID, automationID string) ([]models.AutomationInvocation, []models.AutomationDispatch, error) {
	if s == nil || s.repo == nil {
		return nil, nil, errors.New("automation lifecycle service is unavailable")
	}
	return s.repo.ClaimManualAutomationRun(ctx, projectID, automationID, time.Now().UTC())
}

func (s *AutomationLifecycleService) Pause(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	if err := s.repo.SetAutomationLifecycle(ctx, projectID, automationID, models.AutomationPaused); err != nil {
		return err
	}
	if pruner, ok := s.taskSvc.(automationQueuedWorkPruner); ok {
		pruner.PruneQueuedWork()
	}
	return nil
}

func (s *AutomationLifecycleService) Resume(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	roots, err := s.repo.ResumeAutomation(ctx, projectID, automationID)
	if err != nil {
		return err
	}
	if submitter, ok := s.taskSvc.(automationTaskSubmissionService); ok {
		for _, task := range roots {
			task.Category = models.CategoryActive
			submitter.SubmitSavedAutomationTask(task)
		}
	}
	return nil
}

func (s *AutomationLifecycleService) Archive(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	if err := s.repo.SetAutomationLifecycle(ctx, projectID, automationID, models.AutomationArchived); err != nil {
		return err
	}
	if pruner, ok := s.taskSvc.(automationQueuedWorkPruner); ok {
		pruner.PruneQueuedWork()
	}
	return nil
}

func (s *AutomationLifecycleService) DeleteBulk(ctx context.Context, projectID string, automationIDs []string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	return s.repo.DeleteAutomations(ctx, projectID, automationIDs)
}

func (s *AutomationLifecycleService) Delete(ctx context.Context, projectID, automationID string) error {
	if s == nil || s.repo == nil {
		return errors.New("automation lifecycle service is unavailable")
	}
	return s.repo.DeleteAutomation(ctx, projectID, automationID)
}
