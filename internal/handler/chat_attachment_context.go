package handler

import (
	"context"
	"fmt"

	llmprompt "github.com/openvibely/openvibely/internal/llm/prompt"
	"github.com/openvibely/openvibely/internal/models"
)

// prepareChatAttachmentContext gathers durable references separately from history.
// The persisted prompt (and therefore the user's chat UI) remains unchanged.
// References belong to the turn that attached the file; historical files are
// not re-uploaded, and historical images must not claim to include image bytes.
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
	params.historicalAttachmentContext = ""
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
