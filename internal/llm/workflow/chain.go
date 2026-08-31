package workflow

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/openvibely/openvibely/internal/applog"
	llmoutput "github.com/openvibely/openvibely/internal/llm/output"
	llmtranscript "github.com/openvibely/openvibely/internal/llm/transcript"
	"github.com/openvibely/openvibely/internal/models"
)

// TaskCreator is the minimal dependency needed to create follow-up tasks.
type TaskCreator interface {
	Create(ctx context.Context, task *models.Task) error
}

// LineageResolver resolves Git lineage (branch + commit SHA) for a parent task.
type LineageResolver interface {
	ResolveParentLineage(ctx context.Context, parentTask models.Task) (branch string, commitSHA string, err error)
}

// Service contains workflow chain behavior with narrow dependencies.
type Service struct {
	taskCreator     TaskCreator
	lineageResolver LineageResolver
}

func NewService(taskCreator TaskCreator) *Service {
	return &Service{taskCreator: taskCreator}
}

// SetLineageResolver sets the lineage resolver for capturing parent Git state.
func (s *Service) SetLineageResolver(lr LineageResolver) {
	s.lineageResolver = lr
}

// CleanOutputForChain strips internal markers from task output so the child task
// receives only meaningful response text.
func CleanOutputForChain(output string) string {
	output = llmtranscript.NormalizeMarkers(output)
	output = llmoutput.StripFinalStatusControl(output)
	output = llmoutput.ReplaceOutsideMarkdownCode(output, reThinkingBlock, "")
	output = llmoutput.ReplaceOutsideMarkdownCode(output, reToolMarker, "")
	output = llmoutput.ReplaceOutsideMarkdownCode(output, reToolResultBlock, "")
	output = llmoutput.ReplaceOutsideMarkdownCode(output, reToolResultLegacy, "")
	return strings.TrimSpace(output)
}

// TriggerTaskChain checks if a task has chaining configured and creates a child task.
func (s *Service) TriggerTaskChain(ctx context.Context, parentTask models.Task, parentOutput string) error {
	config, err := parentTask.ParseChainConfig()
	if err != nil {
		applog.Infof("[agent-svc] triggerTaskChain error parsing chain config task=%s: %v", parentTask.ID, err)
		return fmt.Errorf("parsing chain config: %w", err)
	}

	if !config.Enabled {
		return nil
	}

	applog.Infof("[agent-svc] triggerTaskChain task=%s trigger=%s child_agent=%s child_model=%s",
		parentTask.ID, config.Trigger, config.ChildAgentID, config.ChildModel)

	if config.Trigger != "on_completion" && config.Trigger != "on_planning_complete" {
		applog.Infof("[agent-svc] triggerTaskChain unknown trigger=%s, skipping", config.Trigger)
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
			applog.Infof("[agent-svc] triggerTaskChain error marshaling child chain config: %v", err)
		}
	}

	// Resolve parent Git lineage for child task
	var baseBranch, baseCommitSHA string
	parentLineageDepth := parentTask.LineageDepth
	if s.lineageResolver != nil {
		branch, sha, lineageErr := s.lineageResolver.ResolveParentLineage(ctx, parentTask)
		if lineageErr != nil {
			applog.Infof("[agent-svc] triggerTaskChain lineage resolution failed task=%s: %v (child will use default branch)", parentTask.ID, lineageErr)
		} else {
			baseBranch = branch
			baseCommitSHA = sha
			applog.Infof("[agent-svc] triggerTaskChain resolved parent lineage task=%s branch=%s sha=%s", parentTask.ID, baseBranch, baseCommitSHA)
		}
	}

	resolvedChildCategory := parentTask.Category
	categorySource := "parent"
	if config.ChildCategory != "" {
		resolvedChildCategory = models.TaskCategory(config.ChildCategory)
		categorySource = "config"
	} else {
		// Parent may already be moved to completed before chain trigger runs.
		// Default to a runnable category for sequential execution.
		switch parentTask.Category {
		case models.CategoryActive, models.CategoryBacklog:
			resolvedChildCategory = parentTask.Category
		default:
			resolvedChildCategory = models.CategoryActive
			categorySource = "default_active"
		}
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

	applog.Infof("[agent-svc] triggerTaskChain creating child task title=%q category=%s category_source=%s parent=%s lineage_depth=%d base_branch=%s base_sha=%s",
		childTask.Title, childTask.Category, categorySource, parentTask.ID, childTask.LineageDepth, childTask.BaseBranch, childTask.BaseCommitSHA)

	if err := s.taskCreator.Create(ctx, childTask); err != nil {
		applog.Infof("[agent-svc] triggerTaskChain error creating child task: %v", err)
		return fmt.Errorf("creating child task: %w", err)
	}

	applog.Infof("[agent-svc] triggerTaskChain created child task id=%s parent=%s", childTask.ID, parentTask.ID)

	// Pre-create blocked grandchild for visibility if child has its own chain config
	if config.ChildChainConfig != nil && config.ChildChainConfig.Enabled {
		blockedGrandchild := BuildBlockedChild(*childTask, config.ChildChainConfig)
		if gcErr := s.taskCreator.Create(ctx, blockedGrandchild); gcErr != nil {
			applog.Infof("[agent-svc] triggerTaskChain error pre-creating blocked grandchild: %v", gcErr)
		} else {
			applog.Infof("[agent-svc] triggerTaskChain pre-created blocked grandchild id=%s for child=%s", blockedGrandchild.ID, childTask.ID)
		}
	}

	return nil
}

// BuildBlockedChild creates a blocked child task from a parent's chain config.
// The child has StatusBlocked and a placeholder prompt (the real prompt comes
// from the parent's output when TriggerTaskChain runs at completion).
func BuildBlockedChild(parentTask models.Task, config *models.ChainConfiguration) *models.Task {
	childTitle := fmt.Sprintf("%s (Implementation)", parentTask.Title)
	if config.ChildTitle != "" {
		childTitle = config.ChildTitle
	}

	childPrompt := "Waiting for parent task to complete..."
	if config.ChildPromptPrefix != "" {
		childPrompt = config.ChildPromptPrefix
	}

	childChainConfig := "{}"
	if config.ChildChainConfig != nil && config.ChildChainConfig.Enabled {
		if data, err := json.Marshal(config.ChildChainConfig); err == nil {
			childChainConfig = string(data)
		}
	}

	// Blocked children always go to backlog for visibility while waiting.
	// The real category (from config or parent) is applied at activation time
	// when triggerTaskChain runs on parent completion.
	childTask := &models.Task{
		ProjectID:    parentTask.ProjectID,
		Title:        childTitle,
		Category:     models.CategoryBacklog,
		Priority:     parentTask.Priority,
		Status:       models.StatusBlocked,
		Prompt:       childPrompt,
		ParentTaskID: &parentTask.ID,
		Tag:          parentTask.Tag,
		ChainConfig:  childChainConfig,
		LineageDepth: parentTask.LineageDepth + 1,
	}
	if config.ChildAgentID != "" {
		childTask.AgentID = &config.ChildAgentID
	}
	return childTask
}

var reThinkingBlock = regexp.MustCompile(`(?s)\[Thinking\](?:\r\n|\r|\n).*?\[/Thinking\](?:\r\n|\r|\n)?`)
var reToolMarker = regexp.MustCompile(`\[Using tool: [^\]]+\](?:\r\n|\r|\n)?`)
var reToolResultBlock = regexp.MustCompile(`(?s)\[Tool\s+\S+\s+(?:done|error)\](?:\r\n|\r|\n)?.*?(?:\r\n|\r|\n)?\[/Tool\](?:\r\n|\r|\n)?`)
var reToolResultLegacy = regexp.MustCompile(`\[Tool\s+\S+\s+(?:done|error):[^\]\r\n]*\](?:\r\n|\r|\n)?`)
