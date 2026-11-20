package attachment

import (
	"context"
	"fmt"
	"mime"
	"path/filepath"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/filecontent"
)

// DirectImage identifies formats we can send as image input without conversion.
func DirectImage(att models.Attachment) bool {
	name := att.FileName
	if name == "" {
		name = att.FilePath
	}
	if filepath.Ext(name) == "" {
		switch att.MediaType {
		case "image/png", "image/jpeg", "image/gif", "image/webp":
			return true
		}
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp":
		return true
	}
	return false
}

func Reference(att models.Attachment) string {
	source := att.FilePath
	if att.ReferencePath != "" {
		source = att.ReferencePath
	}
	path, err := filepath.Abs(source)
	if err != nil {
		path = source
	}
	name := att.FileName
	if name == "" {
		name = filepath.Base(path)
	}
	mediaType := att.MediaType
	if mediaType == "" {
		mediaType = mime.TypeByExtension(filepath.Ext(name))
	}
	return fmt.Sprintf("Attached file: %q (type: %q, path: %q). Use file tools and the relevant skills to inspect it.", name, mediaType, path)
}

type InputFile struct {
	Name, MediaType string
	Data            []byte
}

// ModelInputs sends only supported images inline. Other files remain on disk.
func ModelInputs(ctx context.Context, attachments []models.Attachment) ([]InputFile, error) {
	return ModelInputsWithProfile(ctx, attachments, "")
}

// ModelInputsWithProfile prepares new attachments before the provider records them for replay.
func ModelInputsWithProfile(ctx context.Context, attachments []models.Attachment, profile models.ImageResizeProfile) ([]InputFile, error) {
	var out []InputFile
	for _, att := range attachments {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		name := att.FileName
		if name == "" {
			name = filepath.Base(att.FilePath)
		}
		if !DirectImage(att) {
			out = append(out, InputFile{Name: name, MediaType: "text/plain", Data: []byte(Reference(att))})
			continue
		}
		data, err := filecontent.ReadPath(att.FilePath, 10<<20)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		input := InputFile{Name: name, MediaType: filecontent.FileMediaType(name, data), Data: data}
		if profile != "" {
			input, err = prepareImage(input, profile)
			if err != nil {
				if profile != models.ImageResize6000 {
					return nil, fmt.Errorf("prepare %s: %w", name, err)
				}
				input = InputFile{Name: name, MediaType: "text/plain", Data: []byte("image content omitted because it could not be processed")}
			}
		}
		out = append(out, input)
	}
	return out, nil
}
