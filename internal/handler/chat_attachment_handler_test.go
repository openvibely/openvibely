package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func useTempUploadsDir(t *testing.T) {
	t.Helper()
	originalUploadsDir := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() {
		uploadsDir = originalUploadsDir
	})
}

func uploadChatAttachmentForTest(t *testing.T, e *echo.Echo, h *Handler, filename string, content []byte, contentType string, sessionID string) string {
	t.Helper()
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	partHeader := make(textproto.MIMEHeader)
	partHeader.Set("Content-Disposition", `form-data; name="files"; filename="`+filename+`"`)
	partHeader.Set("Content-Type", contentType)
	part, err := writer.CreatePart(partHeader)
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader(content))
	require.NoError(t, err)
	if sessionID != "" {
		require.NoError(t, writer.WriteField("attachment_session_id", sessionID))
	}
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	require.NoError(t, h.UploadChatAttachment(c))
	require.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		SessionID   string `json:"session_id"`
		Attachments []struct {
			Filename  string `json:"filename"`
			MediaType string `json:"media_type"`
		} `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.NotEmpty(t, response.SessionID)
	require.Len(t, response.Attachments, 1)
	assert.Equal(t, contentType, response.Attachments[0].MediaType)
	return response.SessionID
}

func TestUploadChatAttachment_AllFileTypes(t *testing.T) {
	useTempUploadsDir(t)

	// Setup
	db := testutil.NewTestDB(t)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}

	e := echo.New()

	testCases := []struct {
		name        string
		filename    string
		contentType string
		content     []byte
		expectError bool
	}{
		{
			name:        "Text file",
			filename:    "test.txt",
			contentType: "text/plain",
			content:     []byte("Hello world"),
			expectError: false,
		},
		{
			name:        "JSON file",
			filename:    "data.json",
			contentType: "application/json",
			content:     []byte(`{"key": "value"}`),
			expectError: false,
		},
		{
			name:        "Image file (PNG)",
			filename:    "image.png",
			contentType: "image/png",
			content:     []byte{0x89, 0x50, 0x4E, 0x47}, // PNG header
			expectError: false,
		},
		{
			name:        "PDF file",
			filename:    "document.pdf",
			contentType: "application/pdf",
			content:     []byte("%PDF-1.4"),
			expectError: false,
		},
		{
			name:        "Binary file",
			filename:    "binary.bin",
			contentType: "application/octet-stream",
			content:     []byte{0x00, 0x01, 0x02, 0x03},
			expectError: false,
		},
		{
			name:        "ZIP archive",
			filename:    "archive.zip",
			contentType: "application/zip",
			content:     []byte("PK\x03\x04"), // ZIP header
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create multipart form
			body := &bytes.Buffer{}
			writer := multipart.NewWriter(body)

			part, err := writer.CreateFormFile("files", tc.filename)
			require.NoError(t, err)

			_, err = io.Copy(part, bytes.NewReader(tc.content))
			require.NoError(t, err)

			err = writer.Close()
			require.NoError(t, err)

			// Create request
			req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			// Execute handler
			err = h.UploadChatAttachment(c)

			if tc.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, http.StatusOK, rec.Code)

				// Parse response
				var response map[string]interface{}
				err = json.Unmarshal(rec.Body.Bytes(), &response)
				require.NoError(t, err)

				assert.NotEmpty(t, response["session_id"])
				attachments := response["attachments"].([]interface{})
				assert.Len(t, attachments, 1)

				attachment := attachments[0].(map[string]interface{})
				assert.Equal(t, tc.filename, attachment["filename"])
			}
		})
	}
}

func TestUploadChatAttachment_AppendsToExistingPendingSession(t *testing.T) {
	useTempUploadsDir(t)

	db := testutil.NewTestDB(t)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}
	e := echo.New()

	firstSessionID := uploadChatAttachmentForTest(t, e, h, "screenshot.png", []byte("first"), "image/png", "")
	secondSessionID := uploadChatAttachmentForTest(t, e, h, "second.png", []byte("second"), "image/png", firstSessionID)

	require.Equal(t, firstSessionID, secondSessionID)
	entries, err := os.ReadDir(filepath.Join(uploadsDir, "chat", "pending", firstSessionID))
	require.NoError(t, err)
	require.Len(t, entries, 2)
	assert.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", firstSessionID, "screenshot.png"))
	assert.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", firstSessionID, "second.png"))
}

func TestUploadChatAttachment_RejectsRetiredSessionWithoutRecreatingDirectory(t *testing.T) {
	useTempUploadsDir(t)

	db := testutil.NewTestDB(t)
	threadInputRepo := repository.NewThreadInputRepo(db)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    threadInputRepo,
	}
	e := echo.New()
	const sessionID = "dddddddddddddddddddddddddddddddd"
	_, err := db.Exec(`INSERT INTO retired_attachment_sessions(session_id) VALUES (?)`, sessionID)
	require.NoError(t, err)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("files", "late.txt")
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader([]byte("late")))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("attachment_session_id", sessionID))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	err = h.UploadChatAttachment(e.NewContext(req, rec))

	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusConflict, httpErr.Code)
	require.NoDirExists(t, filepath.Join(uploadsDir, "chat", "pending", sessionID))
}

func TestUploadChatAttachment_SerializesPublicationWithConcurrentRetirement(t *testing.T) {
	useTempUploadsDir(t)

	db := testutil.NewTestDB(t)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}
	e := echo.New()
	const sessionID = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"

	publicationPaused := make(chan struct{})
	continuePublication := make(chan struct{})
	h.pendingPublicationHook = func(gotSessionID string) {
		assert.Equal(t, sessionID, gotSessionID)
		close(publicationPaused)
		<-continuePublication
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("files", "racing.txt")
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader([]byte("racing")))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("attachment_session_id", sessionID))
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()

	uploadResult := make(chan error, 1)
	go func() {
		uploadResult <- h.UploadChatAttachment(e.NewContext(req, rec))
	}()
	<-publicationPaused

	cleanupStarted := make(chan struct{})
	cleanupResult := make(chan error, 1)
	go func() {
		close(cleanupStarted)
		cleanupResult <- h.cleanupUnpublishedPendingAttachmentSession(context.Background(), sessionID)
	}()
	<-cleanupStarted
	select {
	case err := <-cleanupResult:
		t.Fatalf("cleanup completed while same-session publication was paused: %v", err)
	case <-time.After(100 * time.Millisecond):
	}

	close(continuePublication)
	require.NoError(t, <-uploadResult)
	require.NoError(t, <-cleanupResult)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NoDirExists(t, filepath.Join(uploadsDir, "chat", "pending", sessionID))
}

func TestUploadChatAttachment_AppendsDuplicateNamesWithoutOverwriting(t *testing.T) {
	useTempUploadsDir(t)

	db := testutil.NewTestDB(t)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}
	e := echo.New()

	firstSessionID := uploadChatAttachmentForTest(t, e, h, "screenshot.png", []byte("first"), "image/png", "")
	secondSessionID := uploadChatAttachmentForTest(t, e, h, "screenshot.png", []byte("second"), "image/png", firstSessionID)

	require.Equal(t, firstSessionID, secondSessionID)
	pendingDir := filepath.Join(uploadsDir, "chat", "pending", firstSessionID)
	firstContent, err := os.ReadFile(filepath.Join(pendingDir, "screenshot.png"))
	require.NoError(t, err)
	secondContent, err := os.ReadFile(filepath.Join(pendingDir, "screenshot-1.png"))
	require.NoError(t, err)
	assert.Equal(t, []byte("first"), firstContent)
	assert.Equal(t, []byte("second"), secondContent)
}

func TestUploadChatAttachment_ExistingPendingSessionMaxFilesLimit(t *testing.T) {
	useTempUploadsDir(t)

	db := testutil.NewTestDB(t)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}
	e := echo.New()

	sessionID := uploadChatAttachmentForTest(t, e, h, "one.png", []byte("one"), "image/png", "")
	uploadChatAttachmentForTest(t, e, h, "two.png", []byte("two"), "image/png", sessionID)
	uploadChatAttachmentForTest(t, e, h, "three.png", []byte("three"), "image/png", sessionID)

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("files", "four.png")
	require.NoError(t, err)
	_, err = io.Copy(part, bytes.NewReader([]byte("four")))
	require.NoError(t, err)
	require.NoError(t, writer.WriteField("attachment_session_id", sessionID))
	require.NoError(t, writer.Close())

	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	err = h.UploadChatAttachment(e.NewContext(req, rec))

	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestUploadChatAttachment_AllowsValidMultiFileUploadAboveSingleFileRequestCap(t *testing.T) {
	useTempUploadsDir(t)

	db := testutil.NewTestDB(t)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}
	e := echo.New()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, filename := range []string{"first.bin", "second.bin"} {
		part, err := writer.CreateFormFile("files", filename)
		require.NoError(t, err)
		_, err = io.Copy(part, bytes.NewReader(bytes.Repeat([]byte("x"), 6<<20)))
		require.NoError(t, err)
	}
	require.NoError(t, writer.Close())
	require.Greater(t, int64(body.Len()), browserAttachmentRequestLimit(maxChatUploadSize))
	require.LessOrEqual(t, int64(body.Len()), attachmentRequestLimit(maxChatUploadSize, maxFilesPerUpload))

	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	require.NoError(t, h.UploadChatAttachment(e.NewContext(req, rec)))
	require.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		SessionID   string `json:"session_id"`
		Attachments []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		} `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.NotEmpty(t, response.SessionID)
	require.Len(t, response.Attachments, 2)
	for _, attachment := range response.Attachments {
		assert.Equal(t, int64(6<<20), attachment.Size)
		assert.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", response.SessionID, attachment.Filename))
	}
}

func TestUploadChatAttachment_AllowsPaddedBoundaryDelimiterWhitespace(t *testing.T) {
	useTempUploadsDir(t)

	db := testutil.NewTestDB(t)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}
	e := echo.New()
	files := []taskAttachmentTestFile{
		{name: "padded-first.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("a"), 6<<20)},
		{name: "padded-second.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("b"), 6<<20)},
	}
	body, contentType := newPaddedDelimiterMultipartBody(t, nil, "files", files)
	require.Greater(t, int64(body.Len()), browserAttachmentRequestLimit(maxChatUploadSize))
	require.LessOrEqual(t, int64(body.Len()), attachmentRequestLimit(maxChatUploadSize, maxFilesPerUpload))

	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", contentType)
	rec := httptest.NewRecorder()
	require.NoError(t, h.UploadChatAttachment(e.NewContext(req, rec)))
	require.Equal(t, http.StatusOK, rec.Code)

	var response struct {
		SessionID   string `json:"session_id"`
		Attachments []struct {
			Filename string `json:"filename"`
			Size     int64  `json:"size"`
		} `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &response))
	require.NotEmpty(t, response.SessionID)
	require.Len(t, response.Attachments, 2)
	for _, attachment := range response.Attachments {
		assert.Equal(t, int64(6<<20), attachment.Size)
		assert.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", response.SessionID, attachment.Filename))
	}
}

func TestUploadChatAttachment_OversizedMultipartRequestIsBoundedBeforePendingSession(t *testing.T) {
	useTempUploadsDir(t)
	tmpMultipartDir := t.TempDir()
	t.Setenv("TMPDIR", tmpMultipartDir)

	db := testutil.NewTestDB(t)
	h := &Handler{
		chatAttachmentRepo: repository.NewChatAttachmentRepo(db),
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}
	e := echo.New()
	const sessionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	req, body, totalSize := newSizedMultipartUploadRequestWithFilePrefix(t, http.MethodPost, "/chat/attachments", map[string]string{"attachment_session_id": sessionID}, "files", "too-large.bin", "application/octet-stream", 25<<20, invalidMultipartBoundaryPayloadPrefix(), false)
	body.limitReadChunkSize(1)
	rec := httptest.NewRecorder()
	err := h.UploadChatAttachment(e.NewContext(req, rec))

	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusRequestEntityTooLarge, httpErr.Code)
	maxRead := browserAttachmentRequestLimit(maxChatUploadSize) + 1
	assert.LessOrEqual(t, body.bytesRead, maxRead)
	assert.Less(t, body.bytesRead, totalSize)
	require.NoDirExists(t, filepath.Join(uploadsDir, "chat", "pending", sessionID))
	assertDirEmpty(t, tmpMultipartDir)
}

func TestUploadChatAttachment_FileSizeLimit(t *testing.T) {
	useTempUploadsDir(t)

	// Setup
	db := testutil.NewTestDB(t)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}

	e := echo.New()

	// Create a file larger than 10MB
	largeContent := make([]byte, 11*1024*1024) // 11MB

	// Create multipart form
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile("files", "large.bin")
	require.NoError(t, err)

	_, err = io.Copy(part, bytes.NewReader(largeContent))
	require.NoError(t, err)

	err = writer.Close()
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute handler - should fail due to size limit
	err = h.UploadChatAttachment(c)

	assert.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	assert.True(t, ok)
	assert.Equal(t, http.StatusRequestEntityTooLarge, httpErr.Code)
}

func TestUploadChatAttachment_MaxFilesLimit(t *testing.T) {
	useTempUploadsDir(t)

	// Setup
	db := testutil.NewTestDB(t)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
		threadInputRepo:    repository.NewThreadInputRepo(db),
	}

	e := echo.New()

	// Create multipart form with 4 files (exceeds limit of 3)
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	for i := 0; i < 4; i++ {
		part, err := writer.CreateFormFile("files", "file"+string(rune('0'+i))+".txt")
		require.NoError(t, err)

		_, err = io.Copy(part, bytes.NewReader([]byte("content")))
		require.NoError(t, err)
	}

	err := writer.Close()
	require.NoError(t, err)

	// Create request
	req := httptest.NewRequest(http.MethodPost, "/chat/attachments", body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	// Execute handler - should fail due to file count limit
	err = h.UploadChatAttachment(c)

	assert.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	assert.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, httpErr.Code)
}
