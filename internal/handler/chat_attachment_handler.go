package handler

import (
	"crypto/rand"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/web/templates/components"
)

const (
	maxChatUploadSize     = 10 << 20   // 10 MB
	maxFilesPerUpload     = 3          // Max 3 files per upload
	maxTextAttachmentSize = 100 * 1024 // 100KB max for text file content injection into prompt
)

func init() {
	// Ensure chat uploads directory exists
	chatUploadsDir := filepath.Join(uploadsDir, "chat")
	if err := os.MkdirAll(chatUploadsDir, 0755); err != nil {
		log.Printf("[chat-attachment] Failed to create chat uploads directory: %v", err)
	}
}

// UploadChatAttachment handles file uploads for chat messages
func (h *Handler) UploadChatAttachment(c echo.Context) error {
	log.Printf("[handler] UploadChatAttachment")

	// Parse multipart form
	form, err := c.MultipartForm()
	if err != nil {
		log.Printf("[handler] UploadChatAttachment error parsing form: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}

	// Get the files from request
	files := form.File["files"]
	if len(files) == 0 {
		log.Printf("[handler] UploadChatAttachment no files provided")
		return echo.NewHTTPError(http.StatusBadRequest, "no files provided")
	}

	// Check max files limit
	if len(files) > maxFilesPerUpload {
		log.Printf("[handler] UploadChatAttachment too many files: %d (max %d)", len(files), maxFilesPerUpload)
		return echo.NewHTTPError(http.StatusBadRequest, "maximum 3 files per upload")
	}

	// Create temporary directory for this upload session
	// We'll associate these with an execution ID when the message is sent
	sessionID := generateSessionID()
	sessionDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		log.Printf("[handler] UploadChatAttachment error creating directory: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create directory")
	}

	// Process each file
	var attachmentInfos []map[string]interface{}
	for _, file := range files {
		// Check file size
		if file.Size > maxChatUploadSize {
			log.Printf("[handler] UploadChatAttachment file %s too large (%d bytes)", file.Filename, file.Size)
			return echo.NewHTTPError(http.StatusBadRequest, "file size exceeds 10MB limit")
		}

		// Open the uploaded file
		src, err := file.Open()
		if err != nil {
			log.Printf("[handler] UploadChatAttachment error opening file %s: %v", file.Filename, err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to process file")
		}
		defer src.Close()

		// Save file
		filename := filepath.Base(file.Filename)
		destPath := filepath.Join(sessionDir, filename)
		dest, err := os.Create(destPath)
		if err != nil {
			log.Printf("[handler] UploadChatAttachment error creating file %s: %v", filename, err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save file")
		}
		defer dest.Close()

		if _, err := io.Copy(dest, src); err != nil {
			log.Printf("[handler] UploadChatAttachment error copying file %s: %v", filename, err)
			os.Remove(destPath)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save file")
		}

		// Detect media type
		mediaType := file.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		log.Printf("[handler] UploadChatAttachment success file=%s size=%d session=%s", filename, file.Size, sessionID)

		attachmentInfos = append(attachmentInfos, map[string]interface{}{
			"filename":   filename,
			"size":       file.Size,
			"media_type": mediaType,
			"session_id": sessionID,
		})
	}

	// Return attachment info as JSON
	return c.JSON(http.StatusOK, map[string]interface{}{
		"session_id":  sessionID,
		"attachments": attachmentInfos,
	})
}

// DownloadChatAttachment handles downloading a chat attachment
func (h *Handler) DownloadChatAttachment(c echo.Context) error {
	attachmentID := c.Param("id")
	log.Printf("[handler] DownloadChatAttachment id=%s", attachmentID)

	// Get attachment
	attachment, err := h.chatAttachmentRepo.GetByID(c.Request().Context(), attachmentID)
	if err != nil || attachment == nil {
		log.Printf("[handler] DownloadChatAttachment not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	// Check if file exists
	if _, err := os.Stat(attachment.FilePath); os.IsNotExist(err) {
		log.Printf("[handler] DownloadChatAttachment file not found: %s", attachment.FilePath)
		return echo.NewHTTPError(http.StatusNotFound, "file not found")
	}

	log.Printf("[handler] DownloadChatAttachment serving file=%s", attachment.FileName)

	// Serve the file
	return c.File(attachment.FilePath)
}

// DeleteChatAttachment handles deleting a chat attachment
func (h *Handler) DeleteChatAttachment(c echo.Context) error {
	attachmentID := c.Param("id")
	log.Printf("[handler] DeleteChatAttachment id=%s", attachmentID)

	// Get attachment to find the file path and execution ID
	attachment, err := h.chatAttachmentRepo.GetByID(c.Request().Context(), attachmentID)
	if err != nil || attachment == nil {
		log.Printf("[handler] DeleteChatAttachment not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	executionID := attachment.ExecutionID

	// Delete from database
	if err := h.chatAttachmentRepo.Delete(c.Request().Context(), attachmentID); err != nil {
		log.Printf("[handler] DeleteChatAttachment error deleting from db: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete attachment")
	}

	// Delete file from disk
	if err := os.Remove(attachment.FilePath); err != nil {
		log.Printf("[handler] DeleteChatAttachment error deleting file: %v (continuing)", err)
	}

	log.Printf("[handler] DeleteChatAttachment success id=%s", attachmentID)

	// Return updated attachments list for this execution
	attachments, _ := h.chatAttachmentRepo.ListByExecution(c.Request().Context(), executionID)
	return render(c, http.StatusOK, components.ChatAttachmentListOnly(attachments))
}

// generateSessionID generates a session ID for temporary file storage
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Printf("[chat-attachment] error generating session ID: %v", err)
		return "fallback"
	}
	return fmt.Sprintf("%x", b)
}
