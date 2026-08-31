package handler

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/applog"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/templates/components"
)

const (
	maxUploadSize                     = 10 << 20 // 10 MB
	maxTaskAttachmentFilesPerRequest  = 10       // Bound browser task attachment batches while preserving existing multi-file uploads.
	multipartRequestOverheadAllowance = 1 << 20  // 1 MB for boundaries and form fields
)

var uploadsDir = "uploads"

var (
	taskAttachmentMkdirAll = os.MkdirAll
	taskAttachmentCreate   = os.Create
	taskAttachmentCopy     = io.Copy
	taskAttachmentRemove   = os.Remove
)

func attachmentRequestLimit(maxFileSize int64, maxFiles int) int64 {
	if maxFiles < 1 {
		maxFiles = 1
	}
	return maxFileSize*int64(maxFiles) + multipartRequestOverheadAllowance
}

func browserAttachmentRequestLimit(maxFileSize int64) int64 {
	return attachmentRequestLimit(maxFileSize, 1)
}

func apiAttachmentRequestLimit(maxFileSize int64, maxFiles int) int64 {
	return attachmentRequestLimit(maxFileSize, maxFiles)
}

func isMultipartContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	return strings.HasPrefix(contentType, "multipart/")
}

func parseBoundedMultipartForm(c echo.Context, maxFileSize int64, maxFiles int) (*multipart.Form, error) {
	req := c.Request()
	if req.MultipartForm != nil {
		return req.MultipartForm, nil
	}
	contentType := req.Header.Get("Content-Type")
	if !isMultipartContentType(contentType) {
		return nil, nil
	}
	maxRequestBytes := attachmentRequestLimit(maxFileSize, maxFiles)
	if req.ContentLength > maxRequestBytes {
		return nil, echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
	}

	boundedBody := http.MaxBytesReader(c.Response(), req.Body, maxRequestBytes)
	if maxFiles > 1 {
		limitedBody, err := newMultipartFileLimitReadCloser(contentType, boundedBody, maxFileSize)
		if err != nil {
			return nil, err
		}
		req.Body = limitedBody
	} else {
		req.Body = boundedBody
	}

	if err := req.ParseMultipartForm(maxRequestBytes); err != nil {
		if req.MultipartForm != nil {
			_ = req.MultipartForm.RemoveAll()
		}
		if isRequestTooLargeError(err) {
			return nil, echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
		}
		return nil, err
	}
	return req.MultipartForm, nil
}

type multipartFileLimitReadCloser struct {
	src         io.ReadCloser
	boundary    []byte
	maxFileSize int64
	pending     []byte
	inBody      bool
	currentFile bool
	fileBytes   int64
}

func newMultipartFileLimitReadCloser(contentType string, src io.ReadCloser, maxFileSize int64) (io.ReadCloser, error) {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return nil, err
	}
	boundary := params["boundary"]
	if boundary == "" {
		return nil, http.ErrMissingBoundary
	}
	return &multipartFileLimitReadCloser{
		src:         src,
		boundary:    []byte("\r\n--" + boundary),
		maxFileSize: maxFileSize,
	}, nil
}

func (r *multipartFileLimitReadCloser) Read(p []byte) (int, error) {
	n, err := r.src.Read(p)
	if n > 0 {
		if scanErr := r.scan(p[:n]); scanErr != nil {
			return 0, scanErr
		}
	}
	return n, err
}

func (r *multipartFileLimitReadCloser) Close() error {
	return r.src.Close()
}

func (r *multipartFileLimitReadCloser) scan(data []byte) error {
	r.pending = append(r.pending, data...)
	for len(r.pending) > 0 {
		if !r.inBody {
			idx := bytes.Index(r.pending, []byte("\r\n\r\n"))
			if idx < 0 {
				if int64(len(r.pending)) > multipartRequestOverheadAllowance {
					return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
				}
				return nil
			}
			headerBlock := r.pending[:idx+len("\r\n\r\n")]
			r.currentFile = isMultipartFileHeader(headerBlock)
			r.fileBytes = 0
			r.inBody = true
			r.pending = r.pending[idx+len("\r\n\r\n"):]
			continue
		}

		idx := bytes.Index(r.pending, r.boundary)
		if idx >= 0 {
			if err := r.countFileBytes(int64(idx)); err != nil {
				return err
			}
			r.pending = r.pending[idx:]
			if !r.hasCompleteBoundaryDelimiter() {
				return nil
			}
			if !r.hasValidBoundaryDelimiter() {
				if err := r.countFileBytes(1); err != nil {
					return err
				}
				r.pending = r.pending[1:]
				continue
			}
			r.pending = r.pending[len(r.boundary):]
			r.inBody = false
			r.currentFile = false
			r.fileBytes = 0
			continue
		}

		keep := len(r.boundary) + 1
		if len(r.pending) <= keep {
			return nil
		}
		processLen := len(r.pending) - keep
		if err := r.countFileBytes(int64(processLen)); err != nil {
			return err
		}
		r.pending = r.pending[processLen:]
	}
	return nil
}

func (r *multipartFileLimitReadCloser) hasCompleteBoundaryDelimiter() bool {
	suffix := r.pending[len(r.boundary):]
	if len(suffix) == 0 {
		return false
	}
	if bytes.HasPrefix(suffix, []byte("--")) {
		rest := skipMultipartLWSP(suffix[2:])
		if len(rest) == 0 {
			return len(suffix) > multipartRequestOverheadAllowance
		}
		if rest[0] == '\r' {
			return len(rest) >= len("\r\n")
		}
		return true
	}

	rest := skipMultipartLWSP(suffix)
	if len(rest) == 0 {
		return len(suffix) > multipartRequestOverheadAllowance
	}
	if rest[0] == '\r' {
		return len(rest) >= len("\r\n")
	}
	return true
}

func (r *multipartFileLimitReadCloser) hasValidBoundaryDelimiter() bool {
	if !r.hasCompleteBoundaryDelimiter() {
		return false
	}
	suffix := r.pending[len(r.boundary):]
	if bytes.HasPrefix(suffix, []byte("--")) {
		rest := skipMultipartLWSP(suffix[2:])
		return len(rest) == 0 || bytes.HasPrefix(rest, []byte("\r\n")) || bytes.HasPrefix(rest, []byte("\n"))
	}
	rest := skipMultipartLWSP(suffix)
	return bytes.HasPrefix(rest, []byte("\r\n")) || bytes.HasPrefix(rest, []byte("\n"))
}

func skipMultipartLWSP(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	return b
}

func (r *multipartFileLimitReadCloser) countFileBytes(n int64) error {
	if !r.currentFile || n <= 0 {
		return nil
	}
	r.fileBytes += n
	if r.fileBytes > r.maxFileSize {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "request body too large")
	}
	return nil
}

func isMultipartFileHeader(header []byte) bool {
	header = bytes.ToLower(header)
	return bytes.Contains(header, []byte("content-disposition:")) && bytes.Contains(header, []byte("filename="))
}

func isRequestTooLargeError(err error) bool {
	var maxBytesErr *http.MaxBytesError
	return errors.As(err, &maxBytesErr) || strings.Contains(err.Error(), "request body too large")
}

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

func (h *Handler) attachmentMutationProjectID(c echo.Context) string {
	if projectID := strings.TrimSpace(c.QueryParam("project_id")); projectID != "" {
		return projectID
	}
	if h.settingsRepo == nil {
		return ""
	}
	selectedProjectID, err := h.settingsRepo.Get(c.Request().Context(), uiPreferenceSelectedProjectIDKey)
	if err != nil {
		applog.Debugf("[handler] failed to load selected project preference for attachment mutation: %v", err)
		return ""
	}
	return strings.TrimSpace(selectedProjectID)
}

func (h *Handler) UploadAttachment(c echo.Context) error {
	taskID := c.Param("taskId")
	applog.Infof("[handler] UploadAttachment task=%s", taskID)

	// Verify task exists and belongs to the active/requested project before parsing or writing files.
	task, err := h.taskSvc.GetByID(c.Request().Context(), taskID)
	if err != nil || task == nil {
		applog.Infof("[handler] UploadAttachment task not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}
	projectID := h.attachmentMutationProjectID(c)
	if projectID != "" && task.ProjectID != projectID {
		applog.Infof("[handler] UploadAttachment task not found in project=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "task not found")
	}

	form, err := parseBoundedMultipartForm(c, maxUploadSize, maxTaskAttachmentFilesPerRequest)
	if err != nil {
		applog.Infof("[handler] UploadAttachment error parsing form: %v", err)
		if httpErr, ok := err.(*echo.HTTPError); ok {
			return httpErr
		}
		return echo.NewHTTPError(http.StatusBadRequest, "failed to parse form")
	}

	if form == nil {
		applog.Infof("[handler] UploadAttachment no multipart form provided")
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
	return render(c, http.StatusOK, components.AttachmentListOnly(attachments, task.ProjectID))
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

	ctx := c.Request().Context()
	projectID := h.attachmentMutationProjectID(c)

	// Get attachment to find the parent task and file path, scoped to the active/requested project when present.
	var (
		attachment *models.Attachment
		err        error
	)
	if projectID != "" {
		attachment, err = h.attachmentRepo.GetByIDForProject(ctx, attachmentID, projectID)
	} else {
		attachment, err = h.attachmentRepo.GetByID(ctx, attachmentID)
	}
	if err != nil || attachment == nil {
		applog.Infof("[handler] DeleteAttachment not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	task, err := h.taskSvc.GetByID(ctx, attachment.TaskID)
	if err != nil || task == nil {
		applog.Infof("[handler] DeleteAttachment parent task not found: %v", err)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}
	if projectID != "" && task.ProjectID != projectID {
		applog.Infof("[handler] DeleteAttachment attachment not found in project=%s", projectID)
		return echo.NewHTTPError(http.StatusNotFound, "attachment not found")
	}

	// Delete from database with the same project guard used for lookup.
	if err := h.attachmentRepo.DeleteByIDForProject(ctx, attachmentID, task.ProjectID); err != nil {
		applog.Infof("[handler] DeleteAttachment error deleting from db: %v", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "failed to delete attachment")
	}

	// Delete file from disk
	if err := os.Remove(attachment.FilePath); err != nil {
		applog.Infof("[handler] DeleteAttachment error deleting file: %v (continuing)", err)
	}

	applog.Infof("[handler] DeleteAttachment success id=%s", attachmentID)

	// Return updated attachments list
	attachments, _ := h.attachmentRepo.ListByTask(ctx, task.ID)
	return render(c, http.StatusOK, components.AttachmentListOnly(attachments, task.ProjectID))
}
