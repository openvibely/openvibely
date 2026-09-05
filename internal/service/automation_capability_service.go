package service

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

const automationCapabilityLimit = 50

type AutomationCapabilitySnapshotBuilder struct {
	projectRepo      *repository.ProjectRepo
	agentRepo        *repository.AgentRepo
	taskRepo         *repository.TaskRepo
	settingsRepo     *repository.SettingsRepo
	githubAuthRepo   *repository.GitHubAuthRepo
	githubConnection automationGitHubConnectionProvider
	llmConfigRepo    *repository.LLMConfigRepo
}

func NewAutomationCapabilitySnapshotBuilder(projectRepo *repository.ProjectRepo, agentRepo *repository.AgentRepo, taskRepo *repository.TaskRepo, settingsRepo *repository.SettingsRepo) *AutomationCapabilitySnapshotBuilder {
	return &AutomationCapabilitySnapshotBuilder{projectRepo: projectRepo, agentRepo: agentRepo, taskRepo: taskRepo, settingsRepo: settingsRepo}
}

func (b *AutomationCapabilitySnapshotBuilder) SetGitHubAuthRepository(githubAuthRepo *repository.GitHubAuthRepo) {
	b.githubAuthRepo = githubAuthRepo
}

func (b *AutomationCapabilitySnapshotBuilder) SetGitHubConnectionProvider(provider automationGitHubConnectionProvider) {
	b.githubConnection = provider
}

func (b *AutomationCapabilitySnapshotBuilder) SetLLMConfigRepository(llmConfigRepo *repository.LLMConfigRepo) {
	b.llmConfigRepo = llmConfigRepo
}

func (b *AutomationCapabilitySnapshotBuilder) Build(ctx context.Context, projectID string) (models.AutomationCapabilitySnapshot, error) {
	return b.build(ctx, projectID, true, false)
}

// BuildForValidation builds the project capability data needed by Automation
// validation. It loads the compact selectable-Agent identity projection only
// when includeAgentReferences is true; full Agent configuration remains part of
// Build for description generation and other capability consumers.
func (b *AutomationCapabilitySnapshotBuilder) BuildForValidation(ctx context.Context, projectID string, includeAgentReferences bool) (models.AutomationCapabilitySnapshot, error) {
	return b.build(ctx, projectID, false, includeAgentReferences)
}

func (b *AutomationCapabilitySnapshotBuilder) build(ctx context.Context, projectID string, includeAgentCapabilities, includeAgentReferences bool) (models.AutomationCapabilitySnapshot, error) {
	var snapshot models.AutomationCapabilitySnapshot
	if b == nil || b.projectRepo == nil {
		return snapshot, errors.New("project repository is unavailable")
	}
	project, err := b.projectRepo.GetByID(ctx, projectID)
	if err != nil {
		return snapshot, err
	}
	if project == nil {
		return snapshot, errors.New("project not found")
	}
	snapshot.Project.ID = project.ID
	snapshot.Project.Name = project.Name
	snapshot.SupportedNodeTypes = customAutomationDescriptionNodeTypes()
	snapshot.SupportedRoles = supportedAutomationRoles()
	snapshot.Integrations = map[string]models.AutomationIntegrationCapability{
		"native": {Configured: true, ApprovalModes: []string{"notification_approval"}},
		"github": {Configured: b.githubConfigured(ctx, project), ApprovalModes: []string{"assignment", "pull_request_review"}},
	}
	snapshot.SafetyBoundaries = map[string]bool{
		"human_approval_required": true, "merge_requires_separate_authorization": true,
		"release_requires_separate_authorization": true, "deployment_requires_separate_authorization": true,
	}
	if b.agentRepo != nil {
		if includeAgentCapabilities {
			agents, listErr := b.agentRepo.ListSelectableForProject(ctx, projectID, automationCapabilityLimit)
			if listErr != nil {
				return snapshot, listErr
			}
			snapshot.AgentDefinitionIDs = make(map[string]string, len(agents))
			for _, agent := range agents {
				if len(snapshot.Agents) >= automationCapabilityLimit {
					break
				}
				if !agent.Enabled || agent.ArchivedAt != nil || !agent.SelectableAsPrimary || (agent.ProjectID != "" && agent.ProjectID != projectID) {
					continue
				}
				key := strings.TrimSpace(agent.Key)
				if key == "" {
					key = agent.ID
				}
				capabilities := append([]string(nil), agent.Tools...)
				sort.Strings(capabilities)
				snapshot.Agents = append(snapshot.Agents, models.AutomationCapabilityRef{ID: key, Name: agent.Name, Capabilities: capabilities})
				if key != "" && agent.ID != "" {
					if _, exists := snapshot.AgentDefinitionIDs[key]; !exists {
						snapshot.AgentDefinitionIDs[key] = agent.ID
					}
				}
			}
		} else if includeAgentReferences {
			references, listErr := b.agentRepo.ListSelectableReferencesForProject(ctx, projectID, automationCapabilityLimit)
			if listErr != nil {
				return snapshot, listErr
			}
			snapshot.AgentDefinitionIDs = make(map[string]string, len(references))
			for _, reference := range references {
				key := strings.TrimSpace(reference.Key)
				if key == "" {
					key = reference.ID
				}
				snapshot.Agents = append(snapshot.Agents, models.AutomationCapabilityRef{ID: key})
				if key != "" && reference.ID != "" {
					if _, exists := snapshot.AgentDefinitionIDs[key]; !exists {
						snapshot.AgentDefinitionIDs[key] = reference.ID
					}
				}
			}
		}
	}
	if b.llmConfigRepo != nil {
		configs, listErr := b.llmConfigRepo.ListPickerOptions(ctx)
		if listErr != nil {
			return snapshot, listErr
		}
		for _, cfg := range configs {
			if len(snapshot.Models) >= automationCapabilityLimit {
				break
			}
			name := strings.TrimSpace(cfg.Name)
			if name == "" {
				name = cfg.ID
			}
			snapshot.Models = append(snapshot.Models, models.AutomationCapabilityRef{ID: cfg.ID, Name: name, Capabilities: []string{strings.TrimSpace(cfg.Model)}})
		}
	}
	if b.taskRepo != nil {
		tasks, listErr := b.taskRepo.ListAutomationReusableTasks(ctx, projectID, automationCapabilityLimit)
		if listErr != nil {
			return snapshot, listErr
		}
		for _, task := range tasks {
			snapshot.ReusableResources = append(snapshot.ReusableResources, models.AutomationCapabilityRef{ID: task.ID, Name: task.Title, Capabilities: []string{"task"}})
		}
	}
	sort.Slice(snapshot.Agents, func(i, j int) bool { return capabilityRefLess(snapshot.Agents[i], snapshot.Agents[j]) })
	sort.Slice(snapshot.Models, func(i, j int) bool { return capabilityRefLess(snapshot.Models[i], snapshot.Models[j]) })
	sort.Slice(snapshot.ReusableResources, func(i, j int) bool {
		return capabilityRefLess(snapshot.ReusableResources[i], snapshot.ReusableResources[j])
	})
	return snapshot, nil
}

func capabilityRefLess(a, b models.AutomationCapabilityRef) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	return a.ID < b.ID
}

func supportedAutomationRoles() []string {
	return customAutomationDescriptionRoles()
}

func (b *AutomationCapabilitySnapshotBuilder) githubConfigured(ctx context.Context, project *models.Project) bool {
	if b == nil || b.settingsRepo == nil || b.githubAuthRepo == nil || b.githubConnection == nil || project == nil {
		return false
	}
	inboxReady, err := automationGitHubAuthorizedInboxReady(ctx, b.githubAuthRepo)
	if err != nil || !inboxReady {
		return false
	}
	if _, err := resolveAutomationProjectGitHubRepository(ctx, b.githubConnection, project); err != nil {
		return false
	}
	mode, err := b.settingsRepo.Get(ctx, GitHubSettingAuthMode)
	if err != nil {
		return false
	}
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != GitHubAuthModePAT && mode != GitHubAuthModeApp {
		return false
	}
	status, err := b.githubConnection.GetConnectionStatus(ctx)
	if err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(status.AuthMode), mode) && status.Configured && status.Connected
}
