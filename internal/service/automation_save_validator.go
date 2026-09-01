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

// GitHubToolRepositoryResolver resolves repository identity and exposes the
// project-scoped GitHub API endpoint used by runtime GitHub tools.
type GitHubToolRepositoryResolver interface {
	ResolveRepo(context.Context, string, string) (*GitHubRepoRef, error)
	GlobalAPIEndpoint(context.Context) string
}

// ResolveGitHubToolRepository resolves the repository and API endpoint for GitHub
// runtime tools. Explicit repo_url inputs are honored outside the selected
// project's Automation context; Automation-bound calls are pinned to the project
// repository and only fall back to RepoPath when RepoURL is empty.
func ResolveGitHubToolRepository(ctx context.Context, provider GitHubToolRepositoryResolver, projectID, repoURL string, project *models.Project) (*GitHubRepoRef, error) {
	if project == nil {
		return nil, errors.New("project not found")
	}
	automationContext, automationBound := AutomationContextFromContext(ctx)
	if strings.TrimSpace(repoURL) != "" && (!automationBound || automationContext.ProjectID != projectID) {
		repo, err := provider.ResolveRepo(ctx, repoURL, "")
		if err != nil {
			return nil, err
		}
		if err := ConfigureGitHubRepoEndpointForProject(repo, repoURL, project.RepoURL, provider.GlobalAPIEndpoint(ctx)); err != nil {
			return nil, err
		}
		return repo, nil
	}
	repoPath := strings.TrimSpace(project.RepoPath)
	if automationBound && automationContext.ProjectID == projectID && strings.TrimSpace(project.RepoURL) != "" {
		repoPath = ""
	}
	repo, err := provider.ResolveRepo(ctx, project.RepoURL, repoPath)
	if err != nil {
		return nil, err
	}
	if err := ConfigureGitHubRepoEndpoint(repo, provider.GlobalAPIEndpoint(ctx)); err != nil {
		return nil, err
	}
	return repo, nil
}

func resolveAutomationProjectGitHubRepository(ctx context.Context, provider any, project *models.Project) (*GitHubRepoRef, error) {
	if project == nil {
		return nil, errors.New("project not found")
	}
	if resolver, ok := provider.(GitHubToolRepositoryResolver); ok {
		return ResolveGitHubToolRepository(ctx, resolver, project.ID, "", project)
	}
	repoURL := strings.TrimSpace(project.RepoURL)
	if repoURL != "" {
		repo, err := ParseGitHubRepoURL(repoURL)
		if err != nil {
			return nil, err
		}
		if err := ConfigureGitHubRepoEndpoint(&repo, ""); err != nil {
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
	type agentReference struct {
		nodeKey string
		ref     string
	}

	references := make([]agentReference, 0)
	for _, node := range candidate.Nodes {
		if node.Type != models.AutomationNodeAgentTask && node.Type != models.AutomationNodeTrigger {
			continue
		}
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

	available := make(map[string]bool)
	if v.agentRepo != nil {
		agents, err := v.agentRepo.ListSelectableReferencesForProject(ctx, projectID, automationCapabilityLimit)
		if err != nil {
			return nil, err
		}
		available = make(map[string]bool, len(agents))
		for _, agent := range agents {
			key := strings.TrimSpace(agent.Key)
			if key == "" {
				key = agent.ID
			}
			if agent.ProjectID == "" || agent.ProjectID == projectID {
				available[key] = true
			}
		}
	}

	var issues []models.AutomationValidationIssue
	for _, reference := range references {
		if !available[reference.ref] {
			issues = append(issues, models.AutomationValidationIssue{NodeKey: reference.nodeKey, Code: "agent_ref", Message: "Agent selection is unavailable in this project."})
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
	if !automationUsesGitHub(candidate) {
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

func automationUsesGitHub(candidate models.AutomationDraftCandidate) bool {
	for _, node := range candidate.Nodes {
		switch node.Role {
		case "create_github_issue", "github_assignment", "github_inbox", "open_pull_request", "pull_request_review":
			return true
		case "implementation":
			if candidate.AdapterKey == AutomationAdapterGitHubSDLC {
				return true
			}
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
		if !isTaskHandoff || customAutomationGitHubIssueTask(candidate, target.Key) {
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
	if node.Role == "native_inbox" {
		if implementation := automationTargetByRole(candidate, node.Key, "implementation"); implementation != nil {
			if goal := automationDraftNodeGoal(*implementation); goal != "" {
				prompt += "\n\nImplementation task goal:\nWhen calling create_alert_implementation_task, set goal to exactly:\n" + goal
			}
		}
	}
	if automationCandidateNodeUsesCustomTopology(candidate, node.Key) && node.Role == "native_inbox" {
		producerScope := customAutomationProducerScope(candidate, node.Key, "native_approval", "create_notification")
		prompt += "\n\nNative approved-notification handoff:\nOnly process approved notifications owned by this same Automation in the current project. Eligible producer stages for this inbox, as source context rather than a graph-branch eligibility limit:\n- " + producerScope + "\nThe runtime returns only notifications with durable project + Automation + notification ownership for this current Native inbox execution; model-supplied metadata, content similarity, graph versions, and stable node-key chains cannot establish ownership. Confirm each returned notification is still actionable for an eligible producer purpose, and skip unrelated content.\nCall list_alerts without project_id, using decision_state=approved, implementation_task_linked=false, a bounded limit, and stable pagination. Do not pass the read filter: both read and unread approved notifications are eligible. The runtime automatically uses this scheduled Task's persisted project. Never search for or reuse a project ID from prior messages, examples, memory, tool output, the project snapshot, or the user description. Before calling claim_alert, collect every eligible result from all pages by following the returned pagination offsets. Do not claim, link, or process any notification while paginating because linkage removes rows from this filtered result set and advancing an offset after mutation can skip notifications. Only after the complete paginated snapshot is collected, call get_alert for each collected notification and inspect the full body and metadata before claiming it.\nCall claim_alert for each notification you can process. Only continue when the claim succeeds. Then call create_alert_implementation_task with a focused Backlog Task title and prompt. The created task is the implementation task. Its prompt must include the notification ID, reviewed context, and acceptance criteria, and directly instruct it to implement the reviewed change, add or update tests, and run required validation; state that it is already the linked implementation task, must not create or look for another implementation task, and must not run notification intake or call get_alert. Human approval authorizes creating and starting that task, but not merge, release, deployment, destructive remediation, or credential changes. Do not say that the created task lacks authorization to implement. The operation atomically links at most one Task and is safe to retry after a crash. After create_alert_implementation_task links the Backlog Task, call execute_tasks with the exact returned implementation_task_id. Do not leave the created Task waiting in Backlog.\nOnly call complete_alert_processing after execute_tasks succeeds. If creation, linkage, or Task execution fails, call fail_alert_processing with a concise error; do not report processing complete. Call release_alert_claim only when no task was linked and immediate retry by another scan is appropriate."
	}
	if node.Role == "github_inbox" {
		issueTask := customAutomationGitHubIssueTaskTarget(candidate, node.Key)
		if !automationCandidateNodeUsesCustomTopology(candidate, node.Key) {
			issueTask = automationTargetByRole(candidate, node.Key, "implementation")
		}
		if issueTask != nil {
			if goal := automationDraftNodeGoal(*issueTask); goal != "" {
				prompt += "\n\nImplementation task goal:\nWhen calling create_task, set goal to exactly:\n" + goal
			}
		}
	}
	if automationCandidateNodeUsesCustomTopology(candidate, node.Key) && node.Role == "github_inbox" {
		if issueTask := customAutomationGitHubIssueTaskTarget(candidate, node.Key); issueTask != nil {
			issueTaskPrompt := automationCompiledGitHubIssueTaskPrompt(candidate, *issueTask)
			category, _ := issueTask.Config["category"].(string)
			priority, _ := draftInt(issueTask.Config["priority"])
			producerScope := customAutomationProducerScope(candidate, node.Key, "github_assignment", "create_github_issue")
			prompt += "\n\nGitHub assignment handoff:\nProcess open issues assigned to the PAT owner or configured GitHub Authorized Users for this inbox. Assignment is the approval signal, whether the issue was created by this Automation or manually in GitHub. Connected upstream producer stages for this inbox can create assignable issues but do not limit eligibility:\n- " + producerScope + "\nAlways call github_get_project_inbox and call github_list_assigned_issues for every returned Authorized User. When PAT authentication is available, also call github_list_my_assigned_issues. Use the listed assigned issues as compact body-free discovery data containing issue numbers, URLs, titles, labels, assignees, and state; do not call github_get_issue for every listed issue as a default scan step. After repository/issue deduplication and existing-task reconciliation, call github_get_issue only for the specific listed issue that needs body or acceptance-note details for accurate task creation. Use the provided GitHub runtime tools as the only source for inbox discovery; do not use local shell commands, gh, Python scripts, curl, or direct GitHub API calls to list or reconstruct assigned issues. If a runtime GitHub tool response is incomplete or too large to inspect safely, use github_get_issue only for the specific issue numbers already returned by that runtime tool, or report the limitation. Deduplicate issues by repository plus issue number before processing them. Reconcile existing work before calling create_task. For each GitHub issue, perform one existing-task lookup with list_tasks using the issue number or URL as the query. Treat that single lookup result as the reconciliation result for that issue. Do not retry the same issue lookup, do not vary task lifecycle filters to search again, and do not run title or fragment searches for the same issue. Create at most one visible task per actionable assigned issue. Set source_github_issue_number to the exact issue number returned by this inbox execution so GitHub/Automation provenance is preserved. Do not set source_github_repo_url; the server resolves Automation provenance from this project's configured repository URL, or from a GitHub remote in its local checkout when that URL is blank. Create each new issue task with category " + category + "; Active creation submits it automatically. Do not call execute_tasks for a newly created Active task. After create_task succeeds for a newly created Active task, do not call list_tasks again for that issue in the same inbox turn just to verify start, labels, or existence; the successful create_task response is the confirmation to proceed with the goal refresh and summary/labels. Use priority " + fmt.Sprintf("%d", priority) + ". For a reconciled existing task, call execute_tasks only when list_tasks shows category Backlog or status failed/cancelled, and pass that exact existing task ID. Never call execute_tasks for an Active pending, queued, running, or completed task. Do not leave approved implementation work in Backlog or merely reconcile a task without starting it when it still needs execution. The task prompt must include the GitHub issue number, URL, title, body or acceptance notes, relevant labels, assignment context, and:\n" + issueTaskPrompt + "\nAssignment is a human approval signal only. You must not approve an issue, approve a PR, merge, release, or deploy on the human's behalf."
		}
	}
	return prompt
}

func customAutomationProducerScope(candidate models.AutomationDraftCandidate, inboxKey, gateRole, actionRole string) string {
	nodes := make(map[string]models.AutomationDraftNode, len(candidate.Nodes))
	for _, node := range candidate.Nodes {
		nodes[node.Key] = node
	}
	gateKeys := make(map[string]struct{})
	for _, edge := range candidate.Edges {
		source, ok := nodes[edge.From]
		if edge.To == inboxKey && ok && source.Type == models.AutomationNodeHumanGate && source.Role == gateRole {
			gateKeys[source.Key] = struct{}{}
		}
	}
	actionKeys := make(map[string]struct{})
	for _, edge := range candidate.Edges {
		source, sourceOK := nodes[edge.From]
		if _, targetOK := gateKeys[edge.To]; targetOK && sourceOK && source.Type == models.AutomationNodeAction && source.Role == actionRole {
			actionKeys[source.Key] = struct{}{}
		}
	}
	producerKeys := make(map[string]struct{})
	for _, edge := range candidate.Edges {
		source, sourceOK := nodes[edge.From]
		if _, targetOK := actionKeys[edge.To]; targetOK && sourceOK &&
			(source.Type == models.AutomationNodeTrigger || source.Type == models.AutomationNodeAgentTask) {
			producerKeys[source.Key] = struct{}{}
		}
	}
	var producers []string
	for _, node := range candidate.Nodes {
		if _, ok := producerKeys[node.Key]; !ok {
			continue
		}
		name := strings.TrimSpace(node.Name)
		purpose, _ := node.Config["prompt"].(string)
		purpose = strings.TrimSpace(purpose)
		producers = append(producers, fmt.Sprintf("Producer: %q. Purpose: %q.", name, purpose))
	}
	if len(producers) == 0 {
		return "No producer is eligible; do not process any notification or issue."
	}
	return strings.Join(producers, "\n- ")
}

func customAutomationNotificationTarget(candidate models.AutomationDraftCandidate, taskNodeKey string) *models.AutomationDraftNode {
	if !automationCandidateNodeUsesCustomTopology(candidate, taskNodeKey) {
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

func automationTargetByRole(candidate models.AutomationDraftCandidate, sourceNodeKey, role string) *models.AutomationDraftNode {
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

func customAutomationTargetByRole(candidate models.AutomationDraftCandidate, sourceNodeKey, role string) *models.AutomationDraftNode {
	if !automationCandidateNodeUsesCustomTopology(candidate, sourceNodeKey) {
		return nil
	}
	return automationTargetByRole(candidate, sourceNodeKey, role)
}

func automationDraftNodeGoal(node models.AutomationDraftNode) string {
	goal, _ := node.Config["goal"].(string)
	return strings.TrimSpace(goal)
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
