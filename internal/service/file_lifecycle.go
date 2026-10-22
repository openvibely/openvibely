package service

import (
	"context"
	"strings"

	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
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
	var refs []string
	for _, file := range files {
		refs = append(refs, llmattachment.Reference(file))
	}
	return strings.Join(refs, "\n")
}
