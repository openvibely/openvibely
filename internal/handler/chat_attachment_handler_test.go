package handler

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestUploadChatAttachment_AllFileTypes(t *testing.T) {
	useTempUploadsDir(t)

	// Setup
	db := testutil.NewTestDB(t)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
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

func TestUploadChatAttachment_FileSizeLimit(t *testing.T) {
	useTempUploadsDir(t)

	// Setup
	db := testutil.NewTestDB(t)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
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
	assert.Equal(t, http.StatusBadRequest, httpErr.Code)
}

func TestUploadChatAttachment_MaxFilesLimit(t *testing.T) {
	useTempUploadsDir(t)

	// Setup
	db := testutil.NewTestDB(t)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
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
