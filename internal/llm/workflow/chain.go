package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// AgentCaller is the minimal dependency needed to run a direct agent call.
type AgentCaller interface {
	CallAgentDirect(ctx context.Context, message string, agent models.LLMConfig, workDir string) (string, error)
}

// TaskCreator is the minimal dependency needed to create follow-up tasks.
type TaskCreator interface {
	Create(ctx context.Context, task *models.Task) error
}

// ProjectResolver resolves repo working directory for a project id.
type ProjectResolver interface {
	ResolveWorkDir(ctx context.Context, projectID string) string
}

// LineageResolver resolves Git lineage (branch + commit SHA) for a parent task.
type LineageResolver interface {
	ResolveParentLineage(ctx context.Context, parentTask models.Task) (branch string, commitSHA string, err error)
}

// Service contains workflow chain behavior with narrow dependencies.
type Service struct {
	projectResolver ProjectResolver
	taskCreator     TaskCreator
	agentCaller     AgentCaller
	lineageResolver LineageResolver
}

func NewService(projectResolver ProjectResolver, taskCreator TaskCreator, agentCaller AgentCaller) *Service {
	return &Service{projectResolver: projectResolver, taskCreator: taskCreator, agentCaller: agentCaller}
}

// SetLineageResolver sets the lineage resolver for capturing parent Git state.
func (s *Service) SetLineageResolver(lr LineageResolver) {
	s.lineageResolver = lr
}

// CallAgentForWorkflow calls an LLM agent for workflow step execution.
// It resolves the project working directory and delegates to CallAgentDirect.
func (s *Service) CallAgentForWorkflow(ctx context.Context, prompt string, agent *models.LLMConfig, projectID string) (string, error) {
	workDir := ""
	if s.projectResolver != nil {
		workDir = s.projectResolver.ResolveWorkDir(ctx, projectID)
	}
	return s.agentCaller.CallAgentDirect(ctx, prompt, *agent, workDir)
}

// CleanOutputForChain strips internal markers from task output so the child task
// receives only meaningful response text.
func CleanOutputForChain(output string) string {
	output = reThinkingBlock.ReplaceAllString(output, "")
	output = reToolMarker.ReplaceAllString(output, "")
	output = reToolResultBlock.ReplaceAllString(output, "")
	output = reToolResultLegacy.ReplaceAllString(output, "")
	if idx := strings.Index(output, "[STATUS:"); idx != -1 {
		output = output[:idx]
	}
	return strings.TrimSpace(output)
}

// TriggerTaskChain checks if a task has chaining configured and creates a child task.
func (s *Service) TriggerTaskChain(ctx context.Context, parentTask models.Task, parentOutput string) error {
	config, err := parentTask.ParseChainConfig()
	if err != nil {
		log.Printf("[agent-svc] triggerTaskChain error parsing chain config task=%s: %v", parentTask.ID, err)
		return fmt.Errorf("parsing chain config: %w", err)
	}

	if !config.Enabled {
		return nil
	}

	log.Printf("[agent-svc] triggerTaskChain task=%s trigger=%s child_agent=%s child_model=%s",
		parentTask.ID, config.Trigger, config.ChildAgentID, config.ChildModel)

	if config.Trigger != "on_completion" && config.Trigger != "on_planning_complete" {
		log.Printf("[agent-svc] triggerTaskChain unknown trigger=%s, skipping", config.Trigger)
		return nil
	}

	childPrompt := CleanOutputForChain(parentOutput)
	if config.ChildPromptPrefix != "" {
		childPrompt = config.ChildPromptPrefix + "\n\n" + childPrompt
	}

	childTitle := fmt.Sprintf("%s (Implementation)", parentTask.Title)
	if config.ChildTitle != "" {
		childTitle = config.ChildTitle
	}

	childChainConfig := "{}"
	if config.ChildChainConfig != nil && config.ChildChainConfig.Enabled {
		if data, err := json.Marshal(config.ChildChainConfig); err == nil {
			childChainConfig = string(data)
		} else {
			log.Printf("[agent-svc] triggerTaskChain error marshaling child chain config: %v", err)
		}
	}

	// Resolve parent Git lineage for child task
	var baseBranch, baseCommitSHA string
	parentLineageDepth := parentTask.LineageDepth
	if s.lineageResolver != nil {
		branch, sha, lineageErr := s.lineageResolver.ResolveParentLineage(ctx, parentTask)
		if lineageErr != nil {
			log.Printf("[agent-svc] triggerTaskChain lineage resolution failed task=%s: %v (child will use default branch)", parentTask.ID, lineageErr)
		} else {
			baseBranch = branch
			baseCommitSHA = sha
			log.Printf("[agent-svc] triggerTaskChain resolved parent lineage task=%s branch=%s sha=%s", parentTask.ID, baseBranch, baseCommitSHA)
		}
	}

	resolvedChildCategory := parentTask.Category
	categorySource := "parent"
	if config.ChildCategory != "" {
		resolvedChildCategory = models.TaskCategory(config.ChildCategory)
		categorySource = "config"
	}

	childTask := &models.Task{
		ProjectID:     parentTask.ProjectID,
		Title:         childTitle,
		Category:      resolvedChildCategory,
		Priority:      parentTask.Priority,
		Status:        models.StatusPending,
		Prompt:        childPrompt,
		ParentTaskID:  &parentTask.ID,
		Tag:           parentTask.Tag,
		ChainConfig:   childChainConfig,
		BaseBranch:    baseBranch,
		BaseCommitSHA: baseCommitSHA,
		LineageDepth:  parentLineageDepth + 1,
	}
	if config.ChildAgentID != "" {
		childTask.AgentID = &config.ChildAgentID
	}

	log.Printf("[agent-svc] triggerTaskChain creating child task title=%q category=%s category_source=%s parent=%s lineage_depth=%d base_branch=%s base_sha=%s",
		childTask.Title, childTask.Category, categorySource, parentTask.ID, childTask.LineageDepth, childTask.BaseBranch, childTask.BaseCommitSHA)

	if err := s.taskCreator.Create(ctx, childTask); err != nil {
		log.Printf("[agent-svc] triggerTaskChain error creating child task: %v", err)
		return fmt.Errorf("creating child task: %w", err)
	}

	log.Printf("[agent-svc] triggerTaskChain created child task id=%s parent=%s", childTask.ID, parentTask.ID)
	return nil
}

var reThinkingBlock = regexp.MustCompile(`(?s)\[Thinking\]\n.*?\[/Thinking\]\n?`)
var reToolMarker = regexp.MustCompile(`\[Using tool: [^\]]+\]\n?`)
var reToolResultBlock = regexp.MustCompile(`(?s)\[Tool\s+\S+\s+(?:done|error)\]\n.*?\[/Tool\]\n?`)
var reToolResultLegacy = regexp.MustCompile(`\[Tool\s+\S+\s+(?:done|error):[^\n]*\]\n?`)
