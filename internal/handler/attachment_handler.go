package handler

import (
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

const (
	maxUploadSize = 10 << 20 // 10 MB
)

var uploadsDir = "uploads"

func init() {
	// Convert uploadsDir to absolute path so file paths stored in the DB work
	// regardless of the working directory. This is critical for task execution:
	// the Claude CLI agent runs with cmd.Dir set to the project's repo path,
	// so relative paths like "uploads/tasks/{id}/file.png" would be unresolvable.
	if abs, err := filepath.Abs(uploadsDir); err == nil {
		uploadsDir = abs
	}
	// Ensure uploads directory exists
	if err := os.MkdirAll(uploadsDir, 0755); err != nil {
		log.Printf("[attachment] Failed to create uploads directory: %v", err)
	}
}

func (h *Handler) UploadAttachment(c echo.Context) error {
	taskID := c.Param("taskId")
	log.Printf("[handler] UploadAttachment task=%s", taskID)

	// Verify task exists
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		log.Printf("[handler] UploadAttachment task not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	// Parse multipart form
	form, err := c.MultipartForm()
	if err != nil {
		log.Printf("[handler] UploadAttachment error parsing form: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}

	// Get the files from request
	files := form.File["files"]
	if len(files) == 0 {
		log.Printf("[handler] UploadAttachment no files provided")
		return echo.NewHTTPError(http.StatusBadRequest, "no files provided")
	}

	// Create task-specific directory
	taskDir := filepath.Join(uploadsDir, taskID)
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		log.Printf("[handler] UploadAttachment error creating directory: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create directory")
	}

	// Process each file
	uploadedCount := 0
	for _, file := range files {
		// Check file size
		if file.Size > maxUploadSize {
			log.Printf("[handler] UploadAttachment file %s too large (%d bytes)", file.Filename, file.Size)
			continue // Skip this file but continue with others
		}

		// Open the uploaded file
		src, err := file.Open()
		if err != nil {
			log.Printf("[handler] UploadAttachment error opening file %s: %v", file.Filename, err)
			continue
		}

		// Save file
		filename := filepath.Base(file.Filename)
		destPath := filepath.Join(taskDir, filename)
		dest, err := os.Create(destPath)
		if err != nil {
			log.Printf("[handler] UploadAttachment error creating file %s: %v", filename, err)
			src.Close()
			continue
		}

		if _, err := io.Copy(dest, src); err != nil {
			log.Printf("[handler] UploadAttachment error copying file %s: %v", filename, err)
			src.Close()
			dest.Close()
			os.Remove(destPath)
			continue
		}
		src.Close()
		dest.Close()

		// Detect media type from file header
		mediaType := file.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		// Create attachment record
		attachment := &models.Attachment{
			TaskID:    taskID,
			FileName:  filename,
			FilePath:  destPath,
			MediaType: mediaType,
			FileSize:  file.Size,
		}

		if err := h.attachmentRepo.Create(c.Request().Context(), attachment); err != nil {
			log.Printf("[handler] UploadAttachment error creating attachment for %s: %v", filename, err)
			os.Remove(destPath)
			continue
		}

		log.Printf("[handler] UploadAttachment success id=%s file=%s size=%d", attachment.ID, filename, file.Size)
		uploadedCount++
	}

	if uploadedCount == 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "no files could be uploaded")
	}

	log.Printf("[handler] UploadAttachment completed: %d/%d files uploaded", uploadedCount, len(files))

	// Return updated attachments list
	attachments, _ := h.attachmentRepo.ListByTask(c.Request().Context(), taskID)
	return render(c, http.StatusOK, components.AttachmentListOnly(attachments))
}

func (h *Handler) DeleteAttachment(c echo.Context) error {
	attachmentID := c.Param("id")
	log.Printf("[handler] DeleteAttachment id=%s", attachmentID)

	// Get attachment to find the file path
	attachment, err := h.attachmentRepo.GetByID(c.Request().Context(), attachmentID)
	if err != nil || attachment == nil {
		log.Printf("[handler] DeleteAttachment not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	taskID := attachment.TaskID

	// Delete from database
	if err := h.attachmentRepo.Delete(c.Request().Context(), attachmentID); err != nil {
		log.Printf("[handler] DeleteAttachment error deleting from db: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete attachment")
	}

	// Delete file from disk
	if err := os.Remove(attachment.FilePath); err != nil {
		log.Printf("[handler] DeleteAttachment error deleting file: %v (continuing)", err)
	}

	log.Printf("[handler] DeleteAttachment success id=%s", attachmentID)

	// Return updated attachments list
	attachments, _ := h.attachmentRepo.ListByTask(c.Request().Context(), taskID)
	return render(c, http.StatusOK, components.AttachmentListOnly(attachments))
}
