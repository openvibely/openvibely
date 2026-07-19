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

	"github.com/openvibely/openvibely/internal/automationobs"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const (
	automationAdapterContractVersion  = 1
	automationCompilerContractVersion = 1
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
	issues := p.drafts.ValidateCandidate(candidate)
	plan := &models.AutomationPublicationPlan{ProjectID: projectID, AutomationID: automationID, VersionID: versionID, Validation: issues,
		WillNot: []string{"merge pull requests", "release software", "deploy software"}}
	if len(issues) > 0 {
		automationobs.Event("automation.publication.validation_failure",
			automationobs.String("project_id", projectID), automationobs.String("automation_id", automationID),
			automationobs.String("version_id", versionID), automationobs.String("adapter_key", candidate.AdapterKey))
		return plan, nil
	}
	adapter, _ := p.registry.Get(candidate.AdapterKey)
	dependencies, capabilityIssues, err := p.capabilityDependencies(ctx, projectID, candidate.AdapterKey)
	if err != nil {
		return nil, err
	}
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
	for _, node := range adapter.Nodes {
		candidateNode := candidateNodes[node.Key]
		if node.AllowedResources["task"] {
			effect, dependency, effectErr := p.planTask(ctx, definition, candidateNode, publishedResources[node.Key+"\x00task"])
			if effectErr != nil {
				return nil, effectErr
			}
			plan.Effects = append(plan.Effects, effect)
			if dependency.ID != "" {
				dependencies = append(dependencies, dependency)
			}
		}
	}
	for _, node := range adapter.Nodes {
		if !node.AllowedResources["schedule"] {
			continue
		}
		candidateNode := candidateNodes[node.Key]
		effect, dependency, changed, effectErr := p.planSchedule(ctx, definition, candidateNode, publishedResources[node.Key+"\x00schedule"])
		if effectErr != nil {
			return nil, effectErr
		}
		plan.Effects = append(plan.Effects, effect)
		if dependency.ID != "" {
			dependencies = append(dependencies, dependency)
		}
		if changed && dependency.ID != "" {
			plan.Effects = append(plan.Effects, models.AutomationPublicationEffect{StepKey: "disable:schedule:" + node.Key, Operation: "disable", TargetKey: "schedule:" + node.Key + ":previous", ResourceType: "schedule", Name: node.Name, ResourceID: dependency.ID})
		}
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

func (p *AutomationPublicationPlanner) capabilityDependencies(ctx context.Context, projectID, adapterKey string) ([]automationPlanDependency, []models.AutomationValidationIssue, error) {
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
	if adapterKey != AutomationAdapterGitHubSDLC {
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

func (p *AutomationPublicationPlanner) planTask(ctx context.Context, definition *models.AutomationDefinition, node models.AutomationDraftNode, existing models.AutomationDefinitionResource) (models.AutomationPublicationEffect, automationPlanDependency, error) {
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
	configured := map[string]any{"title": task.Title, "prompt": task.Prompt, "category": task.Category, "priority": task.Priority, "agent_id": nilString(task.AgentID), "agent_definition_id": nilString(task.AgentDefinitionID)}
	dependency = automationPlanDependency{Type: "task", ID: task.ID, ProjectID: task.ProjectID, NodeKey: node.Key, Configured: configured}
	effect.ResourceID = task.ID
	desiredPriority, _ := draftInt(node.Config["priority"])
	desiredCategory, _ := node.Config["category"].(string)
	desiredPrompt, _ := node.Config["prompt"].(string)
	if task.Title == effect.Name && task.Prompt == desiredPrompt && string(task.Category) == desiredCategory && task.Priority == desiredPriority {
		effect.Operation = "unchanged"
	} else {
		effect.Operation = "update"
	}
	return effect, dependency, nil
}

func (p *AutomationPublicationPlanner) planSchedule(ctx context.Context, definition *models.AutomationDefinition, node models.AutomationDraftNode, existing models.AutomationDefinitionResource) (models.AutomationPublicationEffect, automationPlanDependency, bool, error) {
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
	if schedule.RunAt.Format("15:04") == desiredRunAt && string(schedule.RepeatType) == desiredRepeat && schedule.RepeatInterval == desiredInterval && schedule.Enabled == desiredEnabled {
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
