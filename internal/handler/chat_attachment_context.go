package handler

import (
	"context"
	"fmt"

	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
)

// prepareChatAttachmentContext gathers durable references separately from history.
// The persisted prompt (and therefore the user's chat UI) remains unchanged.
// References belong to the turn that attached the file; historical files are
// not re-uploaded, and historical images must not claim to include image bytes.
// Task threads use the task-wide task_attachments source instead of history.
func (h *Handler) prepareChatAttachmentContext(ctx context.Context, params *streamingResponseParams) ([]models.Attachment, error) {
	files := append([]models.Attachment(nil), params.ImageAttachments...)
	current := params.ImageAttachments
	if params.AttachmentContext != "" {
		params.Message = combineContexts(params.Message, params.AttachmentContext)
		// AttachmentContext already describes non-image files. Add only the
		// direct-image instructions, without duplicating document references.
		current = nil
		for _, file := range params.ImageAttachments {
			if isImageFile(file.FileName) {
				current = append(current, file)
			}
		}
	}
	params.Message = combineContexts(params.Message, llmprompt.BuildAttachmentInstructions(current))
	params.historicalAttachmentContext = ""
	params.attachmentSource = nil
	if task := h.taskThreadForAttachments(ctx, params); task != nil {
		return h.prepareTaskThreadAttachmentContext(ctx, params, *task, files)
	}
	if h.chatAttachmentRepo == nil || len(params.ChatHistory) == 0 {
		return files, nil
	}
	ids := make([]string, 0, len(params.ChatHistory))
	for _, execution := range params.ChatHistory {
		ids = append(ids, execution.ID)
	}
	byExecution, err := h.chatAttachmentRepo.ListByExecutionIDs(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load conversation attachments: %w", err)
	}
	for i := range params.ChatHistory {
		execution := &params.ChatHistory[i]
		var attached []models.Attachment
		for _, file := range byExecution[execution.ID] {
			attached = append(attached, models.Attachment{
				FileName: file.FileName, FilePath: file.FilePath,
				MediaType: file.MediaType, FileSize: file.FileSize,
			})
		}
		if len(attached) > 0 {
			params.historicalAttachmentContext = combineContexts(params.historicalAttachmentContext, fmt.Sprintf("Attachment turn %s\n%s", execution.ID, llmprompt.BuildAttachmentReferences(attached)))
		}
		files = append(files, attached...)
	}
	return files, nil
}

func (h *Handler) taskThreadForAttachments(ctx context.Context, params *streamingResponseParams) *models.Task {
	if h.attachmentRepo == nil || params.TaskID == "" {
		return nil
	}
	task := params.Task
	if task == nil || task.ID != params.TaskID {
		loaded, err := h.taskRepo.GetByID(ctx, params.TaskID)
		if err != nil || loaded == nil {
			return nil
		}
		task = loaded
	}
	if !service.IsTaskThread(task) {
		return nil
	}
	return task
}

// prepareTaskThreadAttachmentContext loads every task attachment once at turn
// start; the source reloads only after task attachments change.
func (h *Handler) prepareTaskThreadAttachmentContext(ctx context.Context, params *streamingResponseParams, task models.Task, files []models.Attachment) ([]models.Attachment, error) {
	source := service.NewTaskAttachmentContextSource(h.attachmentRepo, h.taskRepo, task)
	if err := source.Load(ctx); err != nil {
		return nil, fmt.Errorf("load conversation attachments: %w", err)
	}
	params.attachmentSource = source
	attachments, err := h.attachmentRepo.ListByTask(ctx, task.ID)
	if err != nil {
		return nil, fmt.Errorf("load conversation attachments: %w", err)
	}
	seen := make(map[string]bool, len(files))
	for _, file := range files {
		seen[file.FilePath] = true
	}
	for _, att := range attachments {
		if !seen[att.FilePath] {
			seen[att.FilePath] = true
			files = append(files, att)
		}
	}
	return files, nil
}

func (params *streamingResponseParams) withAttachmentContext(ctx context.Context) context.Context {
	if params.attachmentSource != nil {
		return attachmentcontext.WithSource(ctx, params.attachmentSource)
	}
	return attachmentcontext.With(ctx, params.historicalAttachmentContext)
}
