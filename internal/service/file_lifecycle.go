package service

import (
	"context"

	"github.com/openvibely/openvibely/internal/agentskills"
	"github.com/openvibely/openvibely/internal/lifecycle"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
)

type attachedFilesKey struct{}

// WithAttachedFiles exposes only references to lifecycle selection; it does not
// give curator calls the files' contents or change the user's stored prompt.
func WithAttachedFiles(ctx context.Context, files []models.Attachment) context.Context {
	return context.WithValue(ctx, attachedFilesKey{}, append([]models.Attachment(nil), files...))
}

func attachedFileReferences(ctx context.Context) string {
	files, _ := ctx.Value(attachedFilesKey{}).([]models.Attachment)
	return llmprompt.BuildAttachmentInstructions(files)
}

// Chat file turns use the existing skill selector without enabling memory writes
// or other task lifecycle hooks in ordinary chat.
func (w *WorkerService) prepareChatFileSkills(ctx context.Context, task models.Task) context.Context {
	if attachedFileReferences(ctx) == "" || w.lifecycleRunner == nil {
		return ctx
	}
	catalog := w.buildSkillCatalog(ctx, task)
	hookCtx := ctx
	if tools := w.buildLifecycleReadRuntimeTools(task, catalog); tools != nil {
		hookCtx = llmcontracts.WithRuntimeTools(hookCtx, tools)
	}
	route := w.runLifecycleSlotFiltered(hookCtx, models.LifecycleRouteTask, task, newLifecycleTaskRunID(task.ID), nil, llmcontracts.ChatContext{}, func(h models.AgentLifecycleHook) bool {
		return h.OutputContract == models.OutputContractSelectedSkills
	})
	var handles []string
	confidence := -1.0
	for _, out := range route.Outputs {
		if out.Error != "" || out.OutputContract != models.OutputContractSelectedSkills {
			continue
		}
		selected, err := lifecycle.ValidateSelectedSkills(out.Payload)
		if err == nil && !selected.NeedsClarification && selected.Confidence > confidence {
			handles = filterCatalogHandles(catalog, selected.Skills)
			confidence = selected.Confidence
		}
	}
	selectedCatalog := catalog.Filter("chat-files:selected", handles)
	ctx = withAdditionalProjectInstructions(ctx, agentskills.RenderSelectedSkillsMarkdown(selectedCatalog, handles))
	return llmcontracts.WithRuntimeTools(ctx, llmcontracts.CompositeRuntimeTools(llmcontracts.RuntimeToolsFromContext(ctx), w.buildTaskSkillRuntimeTools(ctx, task, selectedCatalog)))
}
