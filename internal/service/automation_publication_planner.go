package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

const (
	automationAdapterContractVersion  = 8
	automationCompilerContractVersion = 8
)

type automationGitHubConnectionProvider interface {
	GetConnectionStatus(context.Context) (GitHubConnectionStatus, error)
}

type AutomationPublicationPlanner struct {
	automationRepo   *repository.AutomationRepo
	taskRepo         *repository.TaskRepo
	scheduleRepo     *repository.ScheduleRepo
	registry         *AutomationAdapterRegistry
	drafts           *AutomationDraftService
	projectRepo      *repository.ProjectRepo
	settingsRepo     *repository.SettingsRepo
	githubAuthRepo   *repository.GitHubAuthRepo
	agentRepo        *repository.AgentRepo
	githubConnection automationGitHubConnectionProvider
}

func NewAutomationPublicationPlanner(automationRepo *repository.AutomationRepo, taskRepo *repository.TaskRepo, scheduleRepo *repository.ScheduleRepo, registry *AutomationAdapterRegistry, drafts *AutomationDraftService) *AutomationPublicationPlanner {
	return &AutomationPublicationPlanner{automationRepo: automationRepo, taskRepo: taskRepo, scheduleRepo: scheduleRepo, registry: registry, drafts: drafts}
}

func (p *AutomationPublicationPlanner) SetCapabilityDependencies(projectRepo *repository.ProjectRepo, settingsRepo *repository.SettingsRepo, githubAuthRepo *repository.GitHubAuthRepo) {
	p.projectRepo = projectRepo
	p.settingsRepo = settingsRepo
	p.githubAuthRepo = githubAuthRepo
}

func (p *AutomationPublicationPlanner) SetAgentRepository(agentRepo *repository.AgentRepo) {
	p.agentRepo = agentRepo
}

func (p *AutomationPublicationPlanner) SetGitHubConnectionProvider(provider automationGitHubConnectionProvider) {
	p.githubConnection = provider
}

type automationPlanCanonical struct {
	SchemaVersion       int                                  `json:"schema_version"`
	AdapterKey          string                               `json:"adapter_key"`
	AdapterVersion      int                                  `json:"adapter_version"`
	CompilerVersion     int                                  `json:"compiler_version"`
	Candidate           automationPlanCandidateCanonical     `json:"candidate"`
	Effects             []models.AutomationPublicationEffect `json:"effects"`
	DependencySnapshots []automationPlanDependency           `json:"dependencies"`
}

type automationPlanCandidateCanonical struct {
	Name           string                        `json:"name"`
	Description    string                        `json:"description"`
	AutomationType string                        `json:"automation_type"`
	Nodes          []automationPlanNodeCanonical `json:"nodes"`
	Edges          []automationPlanEdgeCanonical `json:"edges"`
}

type automationPlanNodeCanonical struct {
	Key    string         `json:"key"`
	Name   string         `json:"name"`
	Type   string         `json:"type"`
	Role   string         `json:"role"`
	Config map[string]any `json:"config"`
}

type automationPlanEdgeCanonical struct {
	Key       string         `json:"key"`
	From      string         `json:"from"`
	To        string         `json:"to"`
	Label     string         `json:"label"`
	Condition map[string]any `json:"condition"`
}

type automationPlanDependency struct {
	Type       string         `json:"type"`
	ID         string         `json:"id"`
	ProjectID  string         `json:"project_id"`
	NodeKey    string         `json:"node_key"`
	Configured map[string]any `json:"configured"`
}

func (p *AutomationPublicationPlanner) Plan(ctx context.Context, projectID, automationID, versionID string) (*models.AutomationPublicationPlan, error) {
	if p == nil || p.automationRepo == nil || p.drafts == nil || p.registry == nil {
		return nil, errors.New("automation publication planner is unavailable")
	}
	definition, err := p.automationRepo.GetDefinitionVersion(ctx, projectID, automationID, versionID)
	if err != nil {
		return nil, err
	}
	if definition == nil || definition.Version.State != models.AutomationVersionDraft {
		return nil, errors.New("automation draft not found")
	}
	metadata, err := p.automationRepo.GetAutomationDraftMetadata(ctx, projectID, automationID, versionID)
	if err != nil {
		return nil, err
	}
	if metadata == nil {
		return nil, errors.New("automation draft metadata not found")
	}
	candidate, err := metadata.Candidate()
	if err != nil {
		return nil, err
	}
	candidate, err = p.drafts.NormalizeCandidate(candidate)
	if err != nil {
		return nil, err
	}
	issues, err := p.drafts.validateCandidateForProject(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	plan := &models.AutomationPublicationPlan{ProjectID: projectID, AutomationID: automationID, VersionID: versionID, Validation: issues,
		WillNot: []string{"merge pull requests", "release software", "deploy software"}}
	if len(issues) > 0 {
		automationobs.Event("automation.publication.validation_failure",
			automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
			automationobs.String("version_id", versionID), automationobs.String("adapter_key", candidate.AdapterKey))
		return plan, nil
	}
	adapter, _ := p.registry.Get(candidate.AdapterKey)
	dependencies, capabilityIssues, err := p.capabilityDependencies(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	agentDependencies, agentIssues, err := p.agentDependencies(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	dependencies = append(dependencies, agentDependencies...)
	capabilityIssues = append(capabilityIssues, agentIssues...)
	if len(capabilityIssues) > 0 {
		plan.Validation = append(plan.Validation, capabilityIssues...)
		automationobs.Event("automation.publication.validation_failure",
			automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
			automationobs.String("version_id", versionID), automationobs.String("adapter_key", candidate.AdapterKey))
		return plan, nil
	}
	published, err := p.automationRepo.GetDefinition(ctx, projectID, automationID)
	if err != nil {
		return nil, err
	}
	publishedResources := map[string]models.AutomationDefinitionResource{}
	if published != nil && published.Version.ID != "" && published.Version.ID != versionID {
		for _, resource := range published.Resources {
			publishedResources[resource.NodeKey+"\x00"+resource.ResourceType] = resource
		}
	}
	candidateNodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		candidateNodes[node.Key] = node
	}
	referenceEffects, err := p.taskReferenceEffects(ctx, projectID, candidate)
	if err != nil {
		return nil, err
	}
	plan.Effects = append(plan.Effects, referenceEffects...)
	resourceNodes := adapter.Nodes
	if adapter.DynamicTopology {
		resourceNodes = make([]AutomationAdapterNode, 0, len(candidate.Nodes))
		for _, node := range candidate.Nodes {
			resource := AutomationAdapterNode{Key: node.Key, Name: node.Name, Type: string(node.Type), Role: node.Role, AllowedResources: map[string]bool{}}
			switch node.Type {
			case models.AutomationNodeAgentTask:
				if node.Role != "implementation" {
					resource.AllowedResources["task"] = true
				}
			case models.AutomationNodeTrigger:
				resource.AllowedResources["task"] = true
				resource.AllowedResources["schedule"] = true
			}
			resourceNodes = append(resourceNodes, resource)
		}
	}
	for _, node := range resourceNodes {
		candidateNode := candidateNodes[node.Key]
		if node.AllowedResources["task"] {
			effect, dependency, effectErr := p.planTask(ctx, definition, candidate, candidateNode, publishedResources)
			if effectErr != nil {
				return nil, effectErr
			}
			plan.Effects = append(plan.Effects, effect)
			if dependency.ID != "" {
				dependencies = append(dependencies, dependency)
			}
		}
	}
	if candidate.AdapterKey == AutomationAdapterCustom {
		for _, node := range candidate.Nodes {
			var effect *models.AutomationPublicationEffect
			switch {
			case node.Type == models.AutomationNodeAction && node.Role == "create_notification":
				effect = &models.AutomationPublicationEffect{StepKey: "alert_configuration:" + node.Key, Operation: "configure", TargetKey: "alert_configuration:" + node.Key, ResourceType: "alert_configuration", Name: node.Name}
			case node.Type == models.AutomationNodeHumanGate && node.Role == "native_approval":
				effect = &models.AutomationPublicationEffect{StepKey: "human_approval:" + node.Key, Operation: "configure", TargetKey: "human_approval:" + node.Key, ResourceType: "human_approval", Name: node.Name}
			case node.Type == models.AutomationNodeAction && node.Role == "create_github_issue":
				effect = &models.AutomationPublicationEffect{StepKey: "github_issue_configuration:" + node.Key, Operation: "configure", TargetKey: "github_issue_configuration:" + node.Key, ResourceType: "github_issue_configuration", Name: node.Name}
			case node.Type == models.AutomationNodeHumanGate && node.Role == "github_assignment":
				effect = &models.AutomationPublicationEffect{StepKey: "github_assignment:" + node.Key, Operation: "configure", TargetKey: "github_assignment:" + node.Key, ResourceType: "github_assignment", Name: node.Name}
			case node.Type == models.AutomationNodeAgentTask && node.Role == "implementation":
				effect = &models.AutomationPublicationEffect{StepKey: "implementation_task_template:" + node.Key, Operation: "configure", TargetKey: "implementation_task_template:" + node.Key, ResourceType: "implementation_task_template", Name: node.Name}
			case node.Type == models.AutomationNodeAction && node.Role == "open_pull_request":
				effect = &models.AutomationPublicationEffect{StepKey: "pull_request_configuration:" + node.Key, Operation: "configure", TargetKey: "pull_request_configuration:" + node.Key, ResourceType: "pull_request_configuration", Name: node.Name}
			case node.Type == models.AutomationNodeHumanGate && node.Role == "pull_request_review":
				effect = &models.AutomationPublicationEffect{StepKey: "pull_request_review:" + node.Key, Operation: "configure", TargetKey: "pull_request_review:" + node.Key, ResourceType: "pull_request_review", Name: node.Name}
			}
			if effect != nil {
				plan.Effects = append(plan.Effects, *effect)
			}
		}
	}
	for _, node := range resourceNodes {
		if !node.AllowedResources["schedule"] {
			continue
		}
		candidateNode := candidateNodes[node.Key]
		effect, dependency, changed, effectErr := p.planSchedule(ctx, definition, candidateNode,
			publishedResources[node.Key+"\x00schedule"], publishedResources[node.Key+"\x00task"])
		if effectErr != nil {
			return nil, effectErr
		}
		plan.Effects = append(plan.Effects, effect)
		if dependency.ID != "" {
			dependencies = append(dependencies, dependency)
		}
		if changed && dependency.ID != "" {
			plan.Effects = append(plan.Effects, models.AutomationPublicationEffect{StepKey: "delete:schedule:" + node.Key, Operation: "delete", TargetKey: "schedule:" + node.Key + ":previous", ResourceType: "schedule", Name: node.Name, ResourceID: dependency.ID})
		}
	}
	var publishedScheduleKeys []string
	for key, resource := range publishedResources {
		if resource.ResourceType == "schedule" {
			publishedScheduleKeys = append(publishedScheduleKeys, key)
		}
	}
	sort.Strings(publishedScheduleKeys)
	for _, key := range publishedScheduleKeys {
		resource := publishedResources[key]
		nodeKey := strings.SplitN(key, "\x00", 2)[0]
		if _, retained := candidateNodes[nodeKey]; retained {
			continue
		}
		if p.scheduleRepo == nil {
			return nil, errors.New("schedule repository is unavailable")
		}
		schedule, scheduleErr := p.scheduleRepo.GetByID(ctx, resource.ResourceID)
		if scheduleErr != nil {
			return nil, scheduleErr
		}
		if schedule == nil {
			return nil, fmt.Errorf("published schedule resource %q is unavailable", resource.ResourceID)
		}
		name := nodeKey
		for _, oldNode := range published.Nodes {
			if oldNode.NodeKey == nodeKey {
				name = oldNode.Name
				break
			}
		}
		plan.Effects = append(plan.Effects, models.AutomationPublicationEffect{
			StepKey: "delete:schedule:" + nodeKey, Operation: "delete", TargetKey: "schedule:" + nodeKey + ":previous",
			ResourceType: "schedule", Name: name, ResourceID: schedule.ID,
		})
		dependencies = append(dependencies, automationPlanDependency{Type: "schedule", ID: schedule.ID,
			ProjectID: definition.Automation.ProjectID, NodeKey: nodeKey, Configured: map[string]any{
				"task_id": schedule.TaskID, "run_at": schedule.RunAt.Format("15:04"), "repeat_type": schedule.RepeatType,
				"repeat_interval": schedule.RepeatInterval, "enabled": schedule.Enabled,
			}})
	}
	sort.SliceStable(dependencies, func(i, j int) bool {
		if dependencies[i].Type != dependencies[j].Type {
			return dependencies[i].Type < dependencies[j].Type
		}
		if dependencies[i].NodeKey != dependencies[j].NodeKey {
			return dependencies[i].NodeKey < dependencies[j].NodeKey
		}
		return dependencies[i].ID < dependencies[j].ID
	})
	canonical := automationPlanCanonical{SchemaVersion: candidate.SchemaVersion, AdapterKey: candidate.AdapterKey,
		AdapterVersion: automationAdapterContractVersion, CompilerVersion: automationCompilerContractVersion,
		Candidate: canonicalAutomationPlanCandidate(candidate), Effects: plan.Effects, DependencySnapshots: dependencies}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(encoded)
	plan.PlanRevision = hex.EncodeToString(hash[:])
	automationobs.Event("automation.publication.planned",
		automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
		automationobs.String("version_id", versionID), automationobs.String("adapter_key", candidate.AdapterKey))
	return plan, nil
}

func (p *AutomationPublicationPlanner) taskReferenceEffects(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) ([]models.AutomationPublicationEffect, error) {
	var effects []models.AutomationPublicationEffect
	for _, node := range candidate.Nodes {
		if node.Type != models.AutomationNodeAgentTask && node.Type != models.AutomationNodeTrigger {
			continue
		}
		ref, _ := node.Config["agent_ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref != "" {
			agent, err := resolveAutomationAgent(ctx, p.agentRepo, projectID, ref)
			if err != nil {
				return nil, err
			}
			if agent == nil {
				return nil, fmt.Errorf("Agent selection for node %q is unavailable in this project", node.Key)
			}
			effects = append(effects, models.AutomationPublicationEffect{StepKey: "agent:" + node.Key, Operation: "reuse", TargetKey: "agent:" + node.Key, ResourceType: "agent", Name: agent.Name, ResourceID: agent.ID})
		}
		skills, _ := draftStringSlice(node.Config["skills"])
		for index, skill := range normalizeDraftReferences(skills) {
			name := skill
			if separator := strings.Index(skill, ":"); separator >= 0 && separator+1 < len(skill) {
				name = skill[separator+1:]
			}
			effects = append(effects, models.AutomationPublicationEffect{StepKey: fmt.Sprintf("skill:%s:%d", node.Key, index), Operation: "reuse", TargetKey: "skill:" + node.Key, ResourceType: "skill", Name: name, ResourceID: skill})
		}
		sourceFiles, _ := draftStringSlice(node.Config["source_files"])
		for index, sourceFile := range normalizeDraftReferences(sourceFiles) {
			effects = append(effects, models.AutomationPublicationEffect{StepKey: fmt.Sprintf("source_file:%s:%d", node.Key, index), Operation: "reuse", TargetKey: "source_file:" + node.Key, ResourceType: "source_file", Name: sourceFile, ResourceID: sourceFile})
		}
	}
	return effects, nil
}

func (p *AutomationPublicationPlanner) agentDependencies(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) ([]automationPlanDependency, []models.AutomationValidationIssue, error) {
	var dependencies []automationPlanDependency
	var issues []models.AutomationValidationIssue
	for _, node := range candidate.Nodes {
		if node.Type != models.AutomationNodeAgentTask && node.Type != models.AutomationNodeTrigger {
			continue
		}
		ref, _ := node.Config["agent_ref"].(string)
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		agent, err := resolveAutomationAgent(ctx, p.agentRepo, projectID, ref)
		if err != nil {
			return nil, nil, err
		}
		if agent == nil {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "agent_ref", Message: "Agent selection is unavailable in this project."})
			continue
		}
		skills, _ := draftStringSlice(node.Config["skills"])
		dependencies = append(dependencies, automationPlanDependency{Type: "agent", ID: agent.ID, ProjectID: projectID, NodeKey: node.Key,
			Configured: map[string]any{"agent_ref": ref, "updated_at": agent.UpdatedAt.UTC().Format(time.RFC3339Nano), "skills": normalizeDraftReferences(skills)}})
	}
	return dependencies, issues, nil
}

func resolveAutomationAgent(ctx context.Context, agentRepo *repository.AgentRepo, projectID, ref string) (*models.Agent, error) {
	if agentRepo == nil || strings.TrimSpace(ref) == "" {
		return nil, nil
	}
	agents, err := agentRepo.ListSelectableForProject(ctx, projectID, automationCapabilityLimit)
	if err != nil {
		return nil, err
	}
	ref = strings.TrimSpace(ref)
	for i := range agents {
		key := strings.TrimSpace(agents[i].Key)
		if key == "" {
			key = agents[i].ID
		}
		if key == ref && (agents[i].ProjectID == "" || agents[i].ProjectID == projectID) {
			return &agents[i], nil
		}
	}
	return nil, nil
}

func (p *AutomationPublicationPlanner) capabilityDependencies(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) ([]automationPlanDependency, []models.AutomationValidationIssue, error) {
	var dependencies []automationPlanDependency
	var issues []models.AutomationValidationIssue
	var project *models.Project
	if p.projectRepo != nil {
		var err error
		project, err = p.projectRepo.GetByID(ctx, projectID)
		if err != nil {
			return nil, nil, err
		}
		if project == nil {
			return nil, nil, errors.New("project not found")
		}
		dependencies = append(dependencies, automationPlanDependency{Type: "project", ID: project.ID,
			ProjectID: project.ID, NodeKey: "", Configured: map[string]any{"repository_url": strings.TrimSpace(project.RepoURL)}})
	}
	if candidate.AdapterKey != AutomationAdapterGitHubSDLC && !customAutomationUsesGitHub(candidate) {
		return dependencies, nil, nil
	}

	configured := map[string]any{"auth_mode": "", "auth_configured": false, "inbox_login": "", "inbox_user_id": nil, "inbox_enabled": false}
	authConfigured := false
	if p.settingsRepo != nil {
		mode, err := p.settingsRepo.Get(ctx, GitHubSettingAuthMode)
		if err != nil {
			return nil, nil, err
		}
		mode = strings.ToLower(strings.TrimSpace(mode))
		configured["auth_mode"] = mode
		switch mode {
		case GitHubAuthModePAT:
			pat, getErr := p.settingsRepo.Get(ctx, GitHubSettingPAT)
			if getErr != nil {
				return nil, nil, getErr
			}
			authConfigured = strings.TrimSpace(pat) != ""
		case GitHubAuthModeApp:
			appID, getErr := p.settingsRepo.Get(ctx, GitHubSettingAppID)
			if getErr != nil {
				return nil, nil, getErr
			}
			appSlug, getErr := p.settingsRepo.Get(ctx, GitHubSettingAppSlug)
			if getErr != nil {
				return nil, nil, getErr
			}
			privateKey, getErr := p.settingsRepo.Get(ctx, GitHubSettingAppPrivateKey)
			if getErr != nil {
				return nil, nil, getErr
			}
			installationID, getErr := p.settingsRepo.Get(ctx, githubSettingInstallationID)
			if getErr != nil {
				return nil, nil, getErr
			}
			authConfigured = strings.TrimSpace(appID) != "" && strings.TrimSpace(appSlug) != "" && strings.TrimSpace(privateKey) != "" && strings.TrimSpace(installationID) != ""
		}
	}
	if p.githubConnection != nil {
		status, err := p.githubConnection.GetConnectionStatus(ctx)
		if err != nil {
			return nil, nil, err
		}
		configured["auth_mode"] = status.AuthMode
		authConfigured = status.Configured && status.Connected
	}
	configured["auth_configured"] = authConfigured
	if !authConfigured {
		issues = append(issues, models.AutomationValidationIssue{Code: "github_auth_unavailable", Message: "Configure the selected GitHub authentication mode before publishing this Automation."})
	}

	inboxReady := false
	if p.githubAuthRepo != nil {
		inbox, err := p.githubAuthRepo.GetProjectInbox(ctx, projectID)
		if err != nil {
			return nil, nil, err
		}
		if inbox != nil {
			configured["inbox_login"] = inbox.GitHubLogin
			configured["inbox_enabled"] = inbox.Enabled
			inboxReady = inbox.Enabled && strings.TrimSpace(inbox.GitHubLogin) != ""
			if inbox.GitHubUserID != nil {
				configured["inbox_user_id"] = *inbox.GitHubUserID
			}
		}
	}
	if !inboxReady {
		issues = append(issues, models.AutomationValidationIssue{Code: "github_approval_inbox_unavailable", Message: "Enable a project GitHub approval inbox before publishing this Automation."})
	}
	if project == nil || strings.TrimSpace(project.RepoURL) == "" {
		issues = append(issues, models.AutomationValidationIssue{Code: "github_repository_unavailable", Message: "Configure an explicit GitHub repository for this project before publishing this Automation."})
	}
	dependencies = append(dependencies, automationPlanDependency{Type: "integration", ID: "github", ProjectID: projectID, Configured: configured})
	return dependencies, issues, nil
}

func customAutomationUsesGitHub(candidate models.AutomationDraftCandidate) bool {
	if candidate.AdapterKey != AutomationAdapterCustom {
		return false
	}
	for _, node := range candidate.Nodes {
		switch node.Role {
		case "create_github_issue", "github_assignment", "github_inbox", "implementation", "open_pull_request", "pull_request_review":
			return true
		}
	}
	return false
}

func canonicalAutomationPlanCandidate(candidate models.AutomationDraftCandidate) automationPlanCandidateCanonical {
	out := automationPlanCandidateCanonical{Name: candidate.Name, Description: candidate.Description, AutomationType: candidate.AutomationType}
	for _, node := range candidate.Nodes {
		out.Nodes = append(out.Nodes, automationPlanNodeCanonical{Key: node.Key, Name: node.Name, Type: string(node.Type), Role: node.Role, Config: node.Config})
	}
	for _, edge := range candidate.Edges {
		out.Edges = append(out.Edges, automationPlanEdgeCanonical{Key: edge.Key, From: edge.From, To: edge.To, Label: edge.Label, Condition: edge.Condition})
	}
	return out
}

func (p *AutomationPublicationPlanner) planTask(ctx context.Context, definition *models.AutomationDefinition, candidate models.AutomationDraftCandidate, node models.AutomationDraftNode, publishedResources map[string]models.AutomationDefinitionResource) (models.AutomationPublicationEffect, automationPlanDependency, error) {
	existing := publishedResources[node.Key+"\x00task"]
	effect := models.AutomationPublicationEffect{StepKey: "task:" + node.Key, Operation: "create", TargetKey: "task:" + node.Key, ResourceType: "task", Name: automationTaskTitle(definition.Automation, node)}
	var dependency automationPlanDependency
	if existing.ResourceID == "" {
		return effect, dependency, nil
	}
	if p.taskRepo == nil {
		return effect, dependency, errors.New("task repository is unavailable")
	}
	task, err := p.taskRepo.GetByID(ctx, existing.ResourceID)
	if err != nil {
		return effect, dependency, err
	}
	if task == nil || task.ProjectID != definition.Automation.ProjectID {
		return effect, dependency, fmt.Errorf("published task resource %q is unavailable", existing.ResourceID)
	}
	configured := map[string]any{"title": task.Title, "prompt": task.Prompt, "category": task.Category, "priority": task.Priority, "agent_id": nilString(task.AgentID), "agent_definition_id": nilString(task.AgentDefinitionID), "parent_task_id": nilString(task.ParentTaskID), "chain_config": task.ChainConfig}
	dependency = automationPlanDependency{Type: "task", ID: task.ID, ProjectID: task.ProjectID, NodeKey: node.Key, Configured: configured}
	effect.ResourceID = task.ID
	desiredPrompt, desiredCategory, desiredPriority := automationNodeTaskConfiguration(candidate, node)
	desiredAgent, err := p.resolveNodeAgent(ctx, definition.Automation.ProjectID, node)
	if err != nil {
		return effect, dependency, err
	}
	var desiredAgentID *string
	if desiredAgent != nil {
		desiredAgentID = &desiredAgent.ID
	}
	desiredParentID := (*string)(nil)
	desiredChainConfig := "{}"
	topologyComplete := true
	if candidate.AdapterKey == AutomationAdapterCustom {
		parentKey, childNode := customAutomationTaskNeighbors(candidate, node.Key)
		if parentKey != "" {
			parentResource := publishedResources[parentKey+"\x00task"]
			if parentResource.ResourceID == "" {
				topologyComplete = false
			} else {
				desiredParentID = &parentResource.ResourceID
			}
		}
		if childNode != nil {
			childResource := publishedResources[childNode.Key+"\x00task"]
			if childResource.ResourceID == "" {
				topologyComplete = false
			}
			desiredChainConfig, err = customAutomationTaskChainConfig(definition.Automation, candidate, *childNode, childResource.ResourceID)
			if err != nil {
				return effect, dependency, err
			}
		}
	}
	if topologyComplete && task.Title == effect.Name && task.Prompt == desiredPrompt && task.Category == desiredCategory && task.Priority == desiredPriority && equalOptionalString(task.AgentDefinitionID, desiredAgentID) && equalOptionalString(task.ParentTaskID, desiredParentID) && normalizedChainConfig(task.ChainConfig) == normalizedChainConfig(desiredChainConfig) {
		effect.Operation = "unchanged"
	} else {
		effect.Operation = "update"
	}
	return effect, dependency, nil
}

func (p *AutomationPublicationPlanner) resolveNodeAgent(ctx context.Context, projectID string, node models.AutomationDraftNode) (*models.Agent, error) {
	ref, _ := node.Config["agent_ref"].(string)
	if strings.TrimSpace(ref) == "" {
		return nil, nil
	}
	agent, err := resolveAutomationAgent(ctx, p.agentRepo, projectID, ref)
	if err != nil {
		return nil, err
	}
	if agent == nil {
		return nil, fmt.Errorf("Agent selection for node %q is unavailable in this project", node.Key)
	}
	return agent, nil
}

func equalOptionalString(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func normalizedChainConfig(value string) string {
	if strings.TrimSpace(value) == "" {
		return "{}"
	}
	return value
}

func customAutomationTaskNeighbors(candidate models.AutomationDraftCandidate, nodeKey string) (string, *models.AutomationDraftNode) {
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
	}
	parentKey := ""
	var child *models.AutomationDraftNode
	for _, edge := range candidate.Edges {
		source := nodes[edge.From]
		target := nodes[edge.To]
		isTaskHandoff := (source.Type == models.AutomationNodeTrigger && target.Type == models.AutomationNodeAgentTask && (target.Role == "task" || target.Role == "github_inbox")) ||
			(source.Type == models.AutomationNodeAgentTask && source.Role == "task" && target.Type == models.AutomationNodeAgentTask && target.Role == "task")
		if !isTaskHandoff {
			continue
		}
		if edge.To == nodeKey {
			parentKey = edge.From
		}
		if edge.From == nodeKey {
			value := target
			child = &value
		}
	}
	return parentKey, child
}

func customAutomationTaskChainConfig(automation models.Automation, candidate models.AutomationDraftCandidate, child models.AutomationDraftNode, childTaskID string) (string, error) {
	config := models.ChainConfiguration{
		Enabled: true, Trigger: "on_completion", ChildTaskID: childTaskID, ChildAutomationNodeKey: child.Key,
		ChildTitle: automationTaskTitle(automation, child), ChildPromptPrefix: automationCompiledTaskPrompt(candidate, child),
	}
	category, _ := child.Config["category"].(string)
	if parentKey, _ := customAutomationTaskNeighbors(candidate, child.Key); parentKey != "" {
		for _, node := range candidate.Nodes {
			if node.Key == parentKey && node.Type == models.AutomationNodeTrigger {
				category = string(models.CategoryActive)
				break
			}
		}
	}
	config.ChildCategory = category
	encoded, err := json.Marshal(config)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func automationNodeTaskConfiguration(candidate models.AutomationDraftCandidate, node models.AutomationDraftNode) (string, models.TaskCategory, int) {
	prompt := automationCompiledTaskPrompt(candidate, node)
	category, _ := node.Config["category"].(string)
	priority, _ := draftInt(node.Config["priority"])
	return prompt, models.TaskCategory(category), priority
}

func automationCompiledTaskPrompt(candidate models.AutomationDraftCandidate, node models.AutomationDraftNode) string {
	prompt, _ := node.Config["prompt"].(string)
	prompt = strings.TrimSpace(prompt)
	skills, _ := draftStringSlice(node.Config["skills"])
	sourceFiles, _ := draftStringSlice(node.Config["source_files"])
	skills = normalizeDraftReferences(skills)
	sourceFiles = normalizeDraftReferences(sourceFiles)
	if len(skills) > 0 {
		prompt += "\n\nConfigured Agent skills:\n- " + strings.Join(automationSkillNames(skills), "\n- ")
	}
	if len(sourceFiles) > 0 {
		prompt += "\n\nFocus source files:\n- " + strings.Join(sourceFiles, "\n- ")
	}
	if notification := customAutomationNotificationTarget(candidate, node.Key); notification != nil {
		notificationType, _ := notification.Config["notification_type"].(string)
		instructions, _ := notification.Config["instructions"].(string)
		prompt += "\n\nHuman approval handoff:\n" + strings.TrimSpace(instructions) +
			"\nWhen you have prepared the proposal, call create_notification exactly once with type \"" + strings.TrimSpace(notificationType) +
			"\" and include the proposal in its body. Creating the notification requests review; it does not approve, merge, release, or deploy anything."
	}
	if issue := customAutomationTargetByRole(candidate, node.Key, "create_github_issue"); issue != nil {
		instructions, _ := issue.Config["instructions"].(string)
		labels, _ := draftStringSlice(issue.Config["labels"])
		prompt += "\n\nGitHub issue handoff:\n" + strings.TrimSpace(instructions) +
			"\nWhen the suggestion is ready, call github_create_issue exactly once for the current project's repository. Use the suggestion as the issue title/body and these labels: " + strings.Join(normalizeDraftReferences(labels), ", ") +
			". Do not assign the issue. A human assignment in GitHub is the approval signal; creating the issue must not approve, implement, merge, release, or deploy anything."
	}
	if node.Role == "github_inbox" {
		if implementation := customAutomationTargetByRole(candidate, node.Key, "implementation"); implementation != nil {
			implementationPrompt := automationCompiledImplementationPrompt(candidate, *implementation)
			category, _ := implementation.Config["category"].(string)
			priority, _ := draftInt(implementation.Config["priority"])
			prompt += "\n\nGitHub assignment handoff:\nCall github_get_project_inbox, then github_list_assigned_issues for an authorized configured inbox login. Reconcile existing work with list_tasks before calling create_task. Create at most one visible task per actionable assigned issue and include source_github_issue_number and source_github_repo_url so existing GitHub/Automation provenance is preserved. Use category " + category + " and priority " + fmt.Sprintf("%d", priority) + ". The implementation task prompt must include:\n" + implementationPrompt +
				"\nAssignment is a human approval signal only. You must not approve an issue, approve a PR, merge, release, or deploy on the human's behalf."
		}
	}
	return prompt
}

func customAutomationNotificationTarget(candidate models.AutomationDraftCandidate, taskNodeKey string) *models.AutomationDraftNode {
	if candidate.AdapterKey != AutomationAdapterCustom {
		return nil
	}
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
	}
	for _, edge := range candidate.Edges {
		target := nodes[edge.To]
		if edge.From == taskNodeKey && target.Type == models.AutomationNodeAction && target.Role == "create_notification" {
			return &target
		}
	}
	return nil
}

func customAutomationTargetByRole(candidate models.AutomationDraftCandidate, sourceNodeKey, role string) *models.AutomationDraftNode {
	if candidate.AdapterKey != AutomationAdapterCustom {
		return nil
	}
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
	}
	for _, edge := range candidate.Edges {
		target := nodes[edge.To]
		if edge.From == sourceNodeKey && target.Role == role {
			return &target
		}
	}
	return nil
}

func automationCompiledImplementationPrompt(candidate models.AutomationDraftCandidate, node models.AutomationDraftNode) string {
	prompt := automationCompiledTaskPrompt(candidate, node)
	if pullRequest := customAutomationTargetByRole(candidate, node.Key, "open_pull_request"); pullRequest != nil {
		instructions, _ := pullRequest.Config["instructions"].(string)
		base, _ := pullRequest.Config["base"].(string)
		draft, _ := pullRequest.Config["draft"].(bool)
		prompt += "\n\nPull request handoff:\n" + strings.TrimSpace(instructions) +
			"\nAfter the implementation and validation are complete, call github_open_pull_request exactly once for this task and its source issue. Use base \"" + strings.TrimSpace(base) + "\" and draft=" + fmt.Sprintf("%t", draft) +
			". Opening a PR requests human review; it must not approve, merge, release, or deploy anything."
	}
	return prompt
}

func automationSkillNames(refs []string) []string {
	names := make([]string, len(refs))
	for i, ref := range refs {
		names[i] = ref
		if separator := strings.Index(ref, ":"); separator >= 0 && separator+1 < len(ref) {
			names[i] = ref[separator+1:]
		}
	}
	return names
}

func (p *AutomationPublicationPlanner) planSchedule(ctx context.Context, definition *models.AutomationDefinition, node models.AutomationDraftNode, existing, scheduledTask models.AutomationDefinitionResource) (models.AutomationPublicationEffect, automationPlanDependency, bool, error) {
	effect := models.AutomationPublicationEffect{StepKey: "schedule:" + node.Key, Operation: "create", TargetKey: "schedule:" + node.Key, ResourceType: "schedule", Name: node.Name}
	var dependency automationPlanDependency
	if existing.ResourceID == "" {
		return effect, dependency, false, nil
	}
	if p.scheduleRepo == nil {
		return effect, dependency, false, errors.New("schedule repository is unavailable")
	}
	schedule, err := p.scheduleRepo.GetByID(ctx, existing.ResourceID)
	if err != nil {
		return effect, dependency, false, err
	}
	if schedule == nil {
		return effect, dependency, false, fmt.Errorf("published schedule resource %q is unavailable", existing.ResourceID)
	}
	configured := map[string]any{"task_id": schedule.TaskID, "run_at": schedule.RunAt.Format("15:04"), "repeat_type": schedule.RepeatType, "repeat_interval": schedule.RepeatInterval, "enabled": schedule.Enabled}
	dependency = automationPlanDependency{Type: "schedule", ID: schedule.ID, ProjectID: definition.Automation.ProjectID, NodeKey: node.Key, Configured: configured}
	desiredRunAt, _ := node.Config["run_at"].(string)
	desiredRepeat, _ := node.Config["repeat_type"].(string)
	desiredInterval, _ := draftInt(node.Config["repeat_interval"])
	desiredEnabled, _ := node.Config["enabled"].(bool)
	correctTask := true
	if definition.Version.AdapterKey == AutomationAdapterCustom {
		correctTask = scheduledTask.ResourceID != "" && schedule.TaskID == scheduledTask.ResourceID
	}
	if correctTask && schedule.RunAt.Format("15:04") == desiredRunAt && string(schedule.RepeatType) == desiredRepeat && schedule.RepeatInterval == desiredInterval && schedule.Enabled == desiredEnabled {
		effect.Operation = "reuse"
		effect.ResourceID = schedule.ID
		return effect, dependency, false, nil
	}
	return effect, dependency, true, nil
}

func automationTaskTitle(automation models.Automation, node models.AutomationDraftNode) string {
	suffix := automation.ID
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	return fmt.Sprintf("%s: %s [%s]", automation.Name, node.Name, suffix)
}

func nilString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func publicationEffectMap(effects []models.AutomationPublicationEffect) map[string]models.AutomationPublicationEffect {
	out := make(map[string]models.AutomationPublicationEffect, len(effects))
	for _, effect := range effects {
		out[effect.StepKey] = effect
	}
	return out
}

func findDraftNode(candidate models.AutomationDraftCandidate, key string) (models.AutomationDraftNode, bool) {
	for _, node := range candidate.Nodes {
		if node.Key == strings.TrimSpace(key) {
			return node, true
		}
	}
	return models.AutomationDraftNode{}, false
}
