package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/repository"
)

type automationGitHubConnectionProvider interface {
	GetConnectionStatus(context.Context) (GitHubConnectionStatus, error)
}

type automationGitHubRepositoryResolver interface {
	ResolveRepo(context.Context, string, string) (*GitHubRepoRef, error)
}

func resolveAutomationProjectGitHubRepository(ctx context.Context, provider any, project *models.Project) (*GitHubRepoRef, error) {
	if project == nil {
		return nil, errors.New("project not found")
	}
	repoURL := strings.TrimSpace(project.RepoURL)
	repoPath := ""
	if repoURL == "" {
		repoPath = strings.TrimSpace(project.RepoPath)
	}
	if resolver, ok := provider.(automationGitHubRepositoryResolver); ok {
		return resolver.ResolveRepo(ctx, repoURL, repoPath)
	}
	if repoURL != "" {
		repo, err := ParseGitHubRepoURL(repoURL)
		if err != nil {
			return nil, err
		}
		return &repo, nil
	}
	return nil, errors.New("project has no GitHub repository URL or resolvable local Git remote")
}

func automationGitHubAuthorizedInboxReady(ctx context.Context, repo *repository.GitHubAuthRepo) (bool, error) {
	if repo == nil {
		return false, nil
	}
	actors, err := repo.ListAuthorizedInboxAssignees(ctx)
	if err != nil {
		return false, err
	}
	for _, actor := range actors {
		if strings.TrimSpace(actor.GitHubLogin) != "" {
			return true, nil
		}
	}
	return false, nil
}

type AutomationSaveValidator struct {
	registry         *AutomationAdapterRegistry
	drafts           *AutomationDraftService
	projectRepo      *repository.ProjectRepo
	settingsRepo     *repository.SettingsRepo
	githubAuthRepo   *repository.GitHubAuthRepo
	agentRepo        *repository.AgentRepo
	githubConnection automationGitHubConnectionProvider
}

func NewAutomationSaveValidator(registry *AutomationAdapterRegistry, drafts *AutomationDraftService) *AutomationSaveValidator {
	return &AutomationSaveValidator{registry: registry, drafts: drafts}
}

func (p *AutomationSaveValidator) SetCapabilityDependencies(projectRepo *repository.ProjectRepo, settingsRepo *repository.SettingsRepo, githubAuthRepo *repository.GitHubAuthRepo) {
	p.projectRepo = projectRepo
	p.settingsRepo = settingsRepo
	p.githubAuthRepo = githubAuthRepo
}

func (p *AutomationSaveValidator) SetAgentRepository(agentRepo *repository.AgentRepo) {
	p.agentRepo = agentRepo
}

func (p *AutomationSaveValidator) SetGitHubConnectionProvider(provider automationGitHubConnectionProvider) {
	p.githubConnection = provider
}

func (v *AutomationSaveValidator) agentIssues(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) ([]models.AutomationValidationIssue, error) {
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
		agent, err := resolveAutomationAgent(ctx, v.agentRepo, projectID, ref)
		if err != nil {
			return nil, err
		}
		if agent == nil {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: node.Key, Code: "agent_ref", Message: "Agent selection is unavailable in this project."})
		}
	}
	return issues, nil
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

func (v *AutomationSaveValidator) capabilityIssues(ctx context.Context, projectID string, candidate models.AutomationDraftCandidate) ([]models.AutomationValidationIssue, error) {
	var issues []models.AutomationValidationIssue
	var project *models.Project
	if v.projectRepo != nil {
		var err error
		project, err = v.projectRepo.GetByID(ctx, projectID)
		if err != nil {
			return nil, err
		}
		if project == nil {
			return nil, errors.New("project not found")
		}
	}
	if candidate.AdapterKey != AutomationAdapterGitHubSDLC && !customAutomationUsesGitHub(candidate) {
		return nil, nil
	}

	authConfigured := false
	if v.settingsRepo != nil {
		mode, err := v.settingsRepo.Get(ctx, GitHubSettingAuthMode)
		if err != nil {
			return nil, err
		}
		switch strings.ToLower(strings.TrimSpace(mode)) {
		case GitHubAuthModePAT:
			pat, getErr := v.settingsRepo.Get(ctx, GitHubSettingPAT)
			if getErr != nil {
				return nil, getErr
			}
			authConfigured = strings.TrimSpace(pat) != ""
		case GitHubAuthModeApp:
			appID, getErr := v.settingsRepo.Get(ctx, GitHubSettingAppID)
			if getErr != nil {
				return nil, getErr
			}
			appSlug, getErr := v.settingsRepo.Get(ctx, GitHubSettingAppSlug)
			if getErr != nil {
				return nil, getErr
			}
			privateKey, getErr := v.settingsRepo.Get(ctx, GitHubSettingAppPrivateKey)
			if getErr != nil {
				return nil, getErr
			}
			installationID, getErr := v.settingsRepo.Get(ctx, githubSettingInstallationID)
			if getErr != nil {
				return nil, getErr
			}
			authConfigured = strings.TrimSpace(appID) != "" && strings.TrimSpace(appSlug) != "" && strings.TrimSpace(privateKey) != "" && strings.TrimSpace(installationID) != ""
		}
	}
	if v.githubConnection != nil {
		status, err := v.githubConnection.GetConnectionStatus(ctx)
		if err != nil {
			return nil, err
		}
		authConfigured = status.Configured && status.Connected
	}
	if !authConfigured {
		issues = append(issues, models.AutomationValidationIssue{Code: "github_auth_unavailable", Message: "Configure the selected GitHub authentication mode before saving this Automation."})
	}

	inboxReady, err := automationGitHubAuthorizedInboxReady(ctx, v.githubAuthRepo)
	if err != nil {
		return nil, err
	}
	if !inboxReady {
		issues = append(issues, models.AutomationValidationIssue{Code: "github_approval_inbox_unavailable", Message: "Add at least one GitHub Authorized User before saving this Automation."})
	}
	if _, err := resolveAutomationProjectGitHubRepository(ctx, v.githubConnection, project); err != nil {
		issues = append(issues, models.AutomationValidationIssue{Code: "github_repository_unavailable", Message: "Configure a project GitHub repository URL or a GitHub remote in the project's local checkout before saving this Automation."})
	}
	return issues, nil
}

func customAutomationUsesGitHub(candidate models.AutomationDraftCandidate) bool {
	if candidate.AdapterKey != AutomationAdapterCustom {
		return false
	}
	for _, node := range candidate.Nodes {
		switch node.Role {
		case "create_github_issue", "github_assignment", "github_inbox", "open_pull_request", "pull_request_review":
			return true
		}
	}
	return false
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
	if node.Type == models.AutomationNodeTrigger {
		if _, child := customAutomationTaskNeighbors(candidate, node.Key); child != nil {
			prompt += "\n\nConnected Task handoff:\nDo not create or schedule the connected downstream Task yourself. OpenVibely activates it automatically after this task completes successfully."
		}
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
		if issueTask := customAutomationGitHubIssueTaskTarget(candidate, node.Key); issueTask != nil {
			issueTaskPrompt := automationCompiledGitHubIssueTaskPrompt(candidate, *issueTask)
			category, _ := issueTask.Config["category"].(string)
			priority, _ := draftInt(issueTask.Config["priority"])
			prompt += "\n\nGitHub assignment handoff:\nCall github_get_project_inbox, then github_list_assigned_issues for an authorized configured inbox login. Reconcile existing work with list_tasks before calling create_task. Create at most one visible task per actionable assigned issue and include source_github_issue_number so existing GitHub/Automation provenance is preserved. Do not set source_github_repo_url; the server restricts Automation provenance to this project's explicit repository URL. Use category " + category + " and priority " + fmt.Sprintf("%d", priority) + ". The task prompt must include:\n" + issueTaskPrompt +
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

func automationCompiledGitHubIssueTaskPrompt(candidate models.AutomationDraftCandidate, node models.AutomationDraftNode) string {
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

func findDraftNode(candidate models.AutomationDraftCandidate, key string) (models.AutomationDraftNode, bool) {
	for _, node := range candidate.Nodes {
		if node.Key == strings.TrimSpace(key) {
			return node, true
		}
	}
	return models.AutomationDraftNode{}, false
}
