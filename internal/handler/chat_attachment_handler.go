package handler

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/attachmentsession"
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
		applog.Infof("[chat-attachment] Failed to create chat uploads directory: %v", err)
	}
}

// UploadChatAttachment handles file uploads for chat messages
func (h *Handler) UploadChatAttachment(c echo.Context) error {
	applog.Infof("[handler] UploadChatAttachment")

	// Parse multipart form
	form, err := c.MultipartForm()
	if err != nil {
		applog.Infof("[handler] UploadChatAttachment error parsing form: %v", err)
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}

	// Get the files from request
	files := form.File["files"]
	if len(files) == 0 {
		applog.Infof("[handler] UploadChatAttachment no files provided")
		return echo.NewHTTPError(http.StatusBadRequest, "no files provided")
	}

	// Check max files limit
	if len(files) > maxFilesPerUpload {
		applog.Infof("[handler] UploadChatAttachment too many files: %d (max %d)", len(files), maxFilesPerUpload)
		return echo.NewHTTPError(http.StatusBadRequest, "maximum 3 files per upload")
	}

	// Create or reuse a temporary directory for this upload session.
	// We'll associate these with an execution ID when the message is sent.
	sessionID := strings.TrimSpace(c.FormValue("attachment_session_id"))
	if !isValidPendingAttachmentSessionID(sessionID) {
		sessionID = generateSessionID()
	}
	if h.threadInputRepo == nil {
		return echo.NewHTTPError(http.StatusInternalServerError, "attachment session storage is unavailable")
	}

	// Retirement and pending-file publication are coordinated per session. This
	// prevents a stale upload from recreating a directory after cleanup commits,
	// without holding SQLite or blocking unrelated attachment sessions during I/O.
	unlockSession := attachmentsession.Lock(sessionID)
	defer unlockSession()
	retired, err := h.threadInputRepo.IsAttachmentSessionRetired(c.Request().Context(), sessionID)
	if err != nil {
		applog.Infof("[handler] UploadChatAttachment error checking session retirement: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to validate attachment session")
	}
	if retired {
		return echo.NewHTTPError(http.StatusConflict, "attachment upload session has expired")
	}
	if h.pendingPublicationHook != nil {
		h.pendingPublicationHook(sessionID)
	}

	sessionDir := filepath.Join(uploadsDir, "chat", "pending", sessionID)
	if err := os.MkdirAll(sessionDir, 0755); err != nil {
		applog.Infof("[handler] UploadChatAttachment error creating directory: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to create directory")
	}
	existingCount, err := countPendingAttachmentFiles(sessionDir)
	if err != nil {
		applog.Infof("[handler] UploadChatAttachment error reading pending session: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to read pending attachments")
	}
	if existingCount+len(files) > maxFilesPerUpload {
		applog.Infof("[handler] UploadChatAttachment too many files in session: existing=%d new=%d max=%d", existingCount, len(files), maxFilesPerUpload)
		return echo.NewHTTPError(http.StatusBadRequest, "maximum 3 files per message")
	}

	// Process each file
	var attachmentInfos []map[string]interface{}
	for _, file := range files {
		// Check file size
		if file.Size > maxChatUploadSize {
			applog.Infof("[handler] UploadChatAttachment file %s too large (%d bytes)", file.Filename, file.Size)
			return echo.NewHTTPError(http.StatusBadRequest, "file size exceeds 10MB limit")
		}

		// Open the uploaded file
		src, err := file.Open()
		if err != nil {
			applog.Infof("[handler] UploadChatAttachment error opening file %s: %v", file.Filename, err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to process file")
		}
		defer src.Close()

		// Save file without overwriting an earlier drop in the same pending session.
		filename := uniquePendingAttachmentFilename(sessionDir, filepath.Base(file.Filename))
		destPath := filepath.Join(sessionDir, filename)
		dest, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			applog.Infof("[handler] UploadChatAttachment error creating file %s: %v", filename, err)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save file")
		}
		defer dest.Close()

		if _, err := io.Copy(dest, src); err != nil {
			applog.Infof("[handler] UploadChatAttachment error copying file %s: %v", filename, err)
			os.Remove(destPath)
			return echo.NewHTTPError(http.StatusInternalServerError, "failed to save file")
		}

		// Detect media type
		mediaType := file.Header.Get("Content-Type")
		if mediaType == "" {
			mediaType = "application/octet-stream"
		}

		applog.Infof("[handler] UploadChatAttachment success file=%s size=%d session=%s", filename, file.Size, sessionID)

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
	applog.Infof("[handler] DownloadChatAttachment id=%s", attachmentID)

	currentProjectID, err := h.getCurrentProjectID(c)
	if err != nil || currentProjectID == "" {
		applog.Infof("[handler] DownloadChatAttachment project resolution failed: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	// Get attachment scoped to the current project.
	attachment, err := h.chatAttachmentRepo.GetByIDForProject(c.Request().Context(), attachmentID, currentProjectID)
	if err != nil || attachment == nil {
		applog.Infof("[handler] DownloadChatAttachment not found or forbidden project=%s: %v", currentProjectID, err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	// Check if file exists
	if _, err := os.Stat(attachment.FilePath); os.IsNotExist(err) {
		applog.Infof("[handler] DownloadChatAttachment file not found: %s", attachment.FilePath)
		return echo.NewHTTPError(http.StatusNotFound, "file not found")
	}

	applog.Infof("[handler] DownloadChatAttachment serving file=%s", attachment.FileName)

	// Serve the file
	return c.File(attachment.FilePath)
}

// DeleteChatAttachment handles deleting a chat attachment
func (h *Handler) DeleteChatAttachment(c echo.Context) error {
	attachmentID := c.Param("id")
	applog.Infof("[handler] DeleteChatAttachment id=%s", attachmentID)

	currentProjectID, err := h.getCurrentProjectID(c)
	if err != nil || currentProjectID == "" {
		applog.Infof("[handler] DeleteChatAttachment project resolution failed: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	// Get attachment to find the file path and execution ID, scoped to the current project.
	attachment, err := h.chatAttachmentRepo.GetByIDForProject(c.Request().Context(), attachmentID, currentProjectID)
	if err != nil || attachment == nil {
		applog.Infof("[handler] DeleteChatAttachment not found or forbidden project=%s: %v", currentProjectID, err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	executionID := attachment.ExecutionID

	// Delete from database with the same project guard used for lookup.
	if err := h.chatAttachmentRepo.DeleteByIDForProject(c.Request().Context(), attachmentID, currentProjectID); err != nil {
		applog.Infof("[handler] DeleteChatAttachment error deleting from db: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete attachment")
	}

	// Delete file from disk
	if err := os.Remove(attachment.FilePath); err != nil {
		applog.Infof("[handler] DeleteChatAttachment error deleting file: %v (continuing)", err)
	}

	applog.Infof("[handler] DeleteChatAttachment success id=%s", attachmentID)

	// Return updated attachments list for this execution
	attachments, _ := h.chatAttachmentRepo.ListByExecution(c.Request().Context(), executionID)
	return render(c, http.StatusOK, components.ChatAttachmentListOnly(attachments, currentProjectID))
}

func (h *Handler) cleanupUnpublishedPendingAttachmentSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if !isValidPendingAttachmentSessionID(sessionID) {
		return fmt.Errorf("invalid pending attachment session: %q", sessionID)
	}
	if h.threadInputRepo == nil {
		return errors.New("thread input repository is unavailable")
	}
	unlockSession := attachmentsession.Lock(sessionID)
	defer unlockSession()
	retired, err := h.threadInputRepo.RetireAttachmentSessionIfUnowned(context.WithoutCancel(ctx), sessionID)
	if err != nil {
		return err
	}
	if !retired {
		return nil
	}
	if h.pendingRemovalHook != nil {
		h.pendingRemovalHook(sessionID)
	}
	if err := os.RemoveAll(filepath.Join(uploadsDir, "chat", "pending", sessionID)); err != nil {
		return fmt.Errorf("removing unpublished pending attachment session %s: %w", sessionID, err)
	}
	return nil
}

func isValidPendingAttachmentSessionID(sessionID string) bool {
	if len(sessionID) != 32 {
		return false
	}
	for _, ch := range sessionID {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return true
}

func uniquePendingAttachmentFilename(sessionDir, filename string) string {
	if filename == "" || filename == "." || filename == string(filepath.Separator) {
		filename = "attachment"
	}
	candidate := filename
	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	if base == "" {
		base = "attachment"
	}
	for i := 1; ; i++ {
		if _, err := os.Stat(filepath.Join(sessionDir, candidate)); os.IsNotExist(err) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d%s", base, i, ext)
	}
}

func countPendingAttachmentFiles(sessionDir string) (int, error) {
	entries, err := os.ReadDir(sessionDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			count++
		}
	}
	return count, nil
}

// generateSessionID generates a session ID for temporary file storage
func generateSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		applog.Infof("[chat-attachment] error generating session ID: %v", err)
		return "fallback"
	}
	return fmt.Sprintf("%x", b)
}
