package handler

import (
	"fmt"
	"io"
	"mime/multipart"
	"path/filepath"
	"strings"

	llmattachment "github.com/openvibely/openvibely/internal/llm/attachment"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/pkg/filecontent"
)

func validateFileUpload(header *multipart.FileHeader) error {
	if llmattachment.DirectImage(models.Attachment{FileName: header.Filename}) && header.Size > llmattachment.DefaultMaxImageBytes {
		return fmt.Errorf("%s exceeds the 10 MiB image upload limit", header.Filename)
	}
	if header.Size > filecontent.UploadLimit(header.Filename) {
		return fmt.Errorf("%s exceeds the upload size limit", header.Filename)
	}
	kind := componentsFileBinary(header.Filename)
	if kind {
		return nil
	}
	f, err := header.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, filecontent.MaxFileBytes+1))
	if err != nil {
		return err
	}
	if _, ok := filecontent.DecodeAttachmentText(data); ok && header.Size > filecontent.MaxTextBytes {
		return fmt.Errorf("%s exceeds the 256 KiB text upload limit", header.Filename)
	}
	return nil
}
func componentsFileBinary(name string) bool {
	media := filecontent.FileMediaType(name, nil)
	if strings.HasPrefix(media, "image/") || strings.HasPrefix(media, "video/") || strings.HasPrefix(media, "audio/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pdf", ".docx", ".doc", ".pptx", ".ppt", ".xls", ".xlsx", ".xlsb", ".ods", ".bin", ".zip":
		return true
	}
	return false
}
