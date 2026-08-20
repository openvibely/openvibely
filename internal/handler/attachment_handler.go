package handler

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

const (
	maxUploadSize = 10 << 20 // 10 MB
)

var uploadsDir = "uploads"

var (
	taskAttachmentMkdirAll = os.MkdirAll
	taskAttachmentCreate   = os.Create
	taskAttachmentCopy     = io.Copy
	taskAttachmentRemove   = os.Remove
)

func init() {
	SetUploadsDir(uploadsDir)
}

func SetUploadsDir(dir string) {
	if dir == "" {
		return
	}
	// Convert uploadsDir to absolute path so file paths stored in the DB work
	// regardless of the project's configured working directory.
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	uploadsDir = dir
	// Ensure uploads directory exists
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		applog.Infof("[attachment] Failed to create uploads directory: %v", err)
	}
}

func (h *Handler) UploadAttachment(c echo.Context) error {
	taskID := c.Param("taskId")
	applog.Infof("[handler] UploadAttachment task=%s", taskID)

	// Verify task exists
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		applog.Infof("[handler] UploadAttachment task not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Parse multipart form
	form, err := c.MultipartForm()
	if err != nil {
		applog.Infof("[handler] UploadAttachment error parsing form: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}

	// Get the files from request
	files := form.File["files"]
	if len(files) == 0 {
		applog.Infof("[handler] UploadAttachment no files provided")
		return echo.NewHTTPError(http.StatusBadRequest, "no files provided")
	}

	result := h.persistTaskAttachmentFiles(c.Request().Context(), taskID, files, "UploadAttachment")
	if result.directoryError != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create directory")
	}
	if result.uploadedCount == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no files could be uploaded")
	}

	// Return updated attachments list
	attachments, _ := h.attachmentRepo.ListByTask(c.Request().Context(), taskID)
	return render(c, http.StatusOK, components.AttachmentListOnly(attachments))
}

type taskAttachmentUploadResult struct {
	uploadedCount  int
	directoryError error
}

// persistTaskAttachmentFiles saves uploaded files to uploads/{taskID}/ and creates task attachment records.
func (h *Handler) persistTaskAttachmentFiles(ctx context.Context, taskID string, files []*multipart.FileHeader, logScope string) taskAttachmentUploadResult {
	result := taskAttachmentUploadResult{}
	taskDir := filepath.Join(uploadsDir, taskID)
	if err := taskAttachmentMkdirAll(taskDir, 0755); err != nil {
		applog.Infof("[handler] %s error creating directory: %v", logScope, err)
		result.directoryError = err
		return result
	}

	for _, file := range files {
		if file.Size > maxUploadSize {
			applog.Infof("[handler] %s file %s too large (%d bytes)", logScope, file.Filename, file.Size)
			continue
		}

		src, err := file.Open()
		if err != nil {
			applog.Infof("[handler] %s error opening file %s: %v", logScope, file.Filename, err)
			continue
		}

		filename := filepath.Base(file.Filename)
		destPath := filepath.Join(taskDir, filename)
		dest, err := taskAttachmentCreate(destPath)
		if err != nil {
			applog.Infof("[handler] %s error creating file %s: %v", logScope, filename, err)
			src.Close()
			continue
		}

		if _, err := taskAttachmentCopy(dest, src); err != nil {
			applog.Infof("[handler] %s error copying file %s: %v", logScope, filename, err)
			src.Close()
			dest.Close()
			taskAttachmentRemove(destPath)
			continue
		}
		src.Close()
		dest.Close()

		mediaType := file.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		attachment := &models.Attachment{
			TaskID:    taskID,
			FileName:  filename,
			FilePath:  destPath,
			MediaType: mediaType,
			FileSize:  file.Size,
		}

		if err := h.attachmentRepo.Create(ctx, attachment); err != nil {
			applog.Infof("[handler] %s error creating attachment for %s: %v", logScope, filename, err)
			taskAttachmentRemove(destPath)
			continue
		}

		applog.Infof("[handler] %s attachment created id=%s file=%s size=%d", logScope, attachment.ID, filename, file.Size)
		result.uploadedCount++
	}

	if result.uploadedCount > 0 {
		applog.Infof("[handler] %s completed: %d/%d attachments uploaded", logScope, result.uploadedCount, len(files))
	}
	return result
}

func (h *Handler) DeleteAttachment(c echo.Context) error {
	attachmentID := c.Param("id")
	applog.Infof("[handler] DeleteAttachment id=%s", attachmentID)

	// Get attachment to find the file path
	attachment, err := h.attachmentRepo.GetByID(c.Request().Context(), attachmentID)
	if err != nil || attachment == nil {
		applog.Infof("[handler] DeleteAttachment not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	taskID := attachment.TaskID

	// Delete from database
	if err := h.attachmentRepo.Delete(c.Request().Context(), attachmentID); err != nil {
		applog.Infof("[handler] DeleteAttachment error deleting from db: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete attachment")
	}

	// Delete file from disk
	if err := os.Remove(attachment.FilePath); err != nil {
		applog.Infof("[handler] DeleteAttachment error deleting file: %v (continuing)", err)
	}

	applog.Infof("[handler] DeleteAttachment success id=%s", attachmentID)

	// Return updated attachments list
	attachments, _ := h.attachmentRepo.ListByTask(c.Request().Context(), taskID)
	return render(c, http.StatusOK, components.AttachmentListOnly(attachments))
}
