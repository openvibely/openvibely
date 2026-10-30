package handler

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/spreadsheetpreviewindex"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tinySharedStringXLSX(t *testing.T) []byte {
	t.Helper()
	var workbook bytes.Buffer
	archive := zip.NewWriter(&workbook)
	worksheet, err := archive.Create("xl/worksheets/sheet1.xml")
	require.NoError(t, err)
	_, err = fmt.Fprint(worksheet, `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>1</v></c></row></sheetData></worksheet>`)
	require.NoError(t, err)
	shared, err := archive.Create("xl/sharedStrings.xml")
	require.NoError(t, err)
	_, err = fmt.Fprint(shared, `<sst><si><t>unused</t></si><si><t>indexed upload value</t></si></sst>`)
	require.NoError(t, err)
	require.NoError(t, archive.Close())
	return workbook.Bytes()
}

func useTempUploadsDir(t *testing.T) {
	t.Helper()
	originalUploadsDir := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() {
		uploadsDir = originalUploadsDir
	})
}

type chatAttachmentOwnershipFixture struct {
	e                  *echo.Echo
	h                  *Handler
	chatAttachmentRepo persistence.ChatAttachmentStore
	owningProject      models.Project
	foreignProject     models.Project
	attachment         models.ChatAttachment
	fileContent        []byte
}

func setupChatAttachmentOwnershipFixture(t *testing.T) chatAttachmentOwnershipFixture {
	t.Helper()
	useTempUploadsDir(t)
	ctx := context.Background()
	env := teststores.New(t)
	projectRepo := env.Projects
	projectSvc := service.NewProjectService(projectRepo)
	settingsRepo := env.Settings
	taskRepo := env.Tasks
	execRepo := env.Executions
	chatAttachmentRepo := env.ChatAttachments
	h := &Handler{
		projectSvc:         projectSvc,
		settingsRepo:       settingsRepo,
		chatAttachmentRepo: chatAttachmentRepo,
	}

	owningProject := &models.Project{Name: "Attachment Owner"}
	require.NoError(t, projectSvc.Create(ctx, owningProject))
	foreignProject := &models.Project{Name: "Attachment Foreign"}
	require.NoError(t, projectSvc.Create(ctx, foreignProject))

	task := &models.Task{
		ProjectID: owningProject.ID,
		Title:     "Chat attachment owner task",
		Category:  models.CategoryChat,
		Priority:  1,
		Status:    models.StatusRunning,
		Prompt:    "chat prompt",
	}
	require.NoError(t, taskRepo.Create(ctx, task))
	exec := &models.Execution{TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "chat prompt"}
	require.NoError(t, execRepo.Create(ctx, exec))

	fileContent := []byte("project A secret attachment body")
	filePath := filepath.Join(uploadsDir, "chat", exec.ID, "secret.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(filePath), 0755))
	require.NoError(t, os.WriteFile(filePath, fileContent, 0644))
	attachment := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "secret.txt",
		FilePath:    filePath,
		MediaType:   "text/plain",
		FileSize:    int64(len(fileContent)),
	}
	require.NoError(t, chatAttachmentRepo.Create(ctx, attachment))

	return chatAttachmentOwnershipFixture{
		e:                  echo.New(),
		h:                  h,
		chatAttachmentRepo: chatAttachmentRepo,
		owningProject:      *owningProject,
		foreignProject:     *foreignProject,
		attachment:         *attachment,
		fileContent:        fileContent,
	}
}

func newChatAttachmentContext(t *testing.T, e *echo.Echo, method, attachmentID, projectID string) (echo.Context, *httptest.ResponseRecorder) {
	t.Helper()
	path := "/chat/attachments/" + attachmentID
	if method == http.MethodGet {
		path += "/download"
	}
	if projectID != "" {
		path += "?project_id=" + projectID
	}
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("id")
	c.SetParamValues(attachmentID)
	return c, rec
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

func TestDownloadChatAttachmentRejectsCrossProjectAttachment(t *testing.T) {
	fixture := setupChatAttachmentOwnershipFixture(t)

	c, rec := newChatAttachmentContext(t, fixture.e, http.MethodGet, fixture.attachment.ID, fixture.foreignProject.ID)
	err := fixture.h.DownloadChatAttachment(c)

	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusNotFound, httpErr.Code)
	assert.NotContains(t, rec.Body.String(), string(fixture.fileContent))
	assert.FileExists(t, fixture.attachment.FilePath)
}

func TestDownloadChatAttachmentServesSameProjectAttachment(t *testing.T) {
	fixture := setupChatAttachmentOwnershipFixture(t)

	c, rec := newChatAttachmentContext(t, fixture.e, http.MethodGet, fixture.attachment.ID, fixture.owningProject.ID)
	err := fixture.h.DownloadChatAttachment(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, string(fixture.fileContent), rec.Body.String())
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "sandbox; default-src 'none'; style-src 'unsafe-inline'; img-src data: blob:", rec.Header().Get("Content-Security-Policy"))
}

func TestDownloadChatAttachmentSandboxesActiveContent(t *testing.T) {
	cases := []struct {
		name      string
		fileName  string
		mediaType string
		content   []byte
	}{
		{"html", "payload.html", "text/html", []byte(`<html><body><script>fetch('/api/secret')</script></body></html>`)},
		{"svg", "payload.svg", "image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(document.cookie)</script></svg>`)},
		{"png", "image.png", "image/png", []byte("\x89PNG\r\n\x1a\nfake-image-bytes")},
		{"pdf", "doc.pdf", "application/pdf", []byte("%PDF-1.4 fake document")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := setupChatAttachmentOwnershipFixture(t)
			ctx := context.Background()
			filePath := filepath.Join(filepath.Dir(fixture.attachment.FilePath), tc.fileName)
			require.NoError(t, os.WriteFile(filePath, tc.content, 0644))
			attachment := &models.ChatAttachment{
				ExecutionID: fixture.attachment.ExecutionID,
				FileName:    tc.fileName,
				FilePath:    filePath,
				MediaType:   tc.mediaType,
				FileSize:    int64(len(tc.content)),
			}
			require.NoError(t, fixture.chatAttachmentRepo.Create(ctx, attachment))

			c, rec := newChatAttachmentContext(t, fixture.e, http.MethodGet, attachment.ID, fixture.owningProject.ID)
			require.NoError(t, fixture.h.DownloadChatAttachment(c))
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, string(tc.content), rec.Body.String())
			assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
			policy := rec.Header().Get("Content-Security-Policy")
			assert.Contains(t, policy, "sandbox")
			assert.Contains(t, policy, "default-src 'none'")
			assert.NotContains(t, policy, "allow-same-origin")
			if tc.name == "html" {
				assert.Contains(t, policy, "sandbox allow-scripts;")
				assert.Contains(t, policy, "connect-src 'none'")
			} else {
				assert.Contains(t, policy, "sandbox;")
			}

			c, rec = newChatAttachmentContext(t, fixture.e, http.MethodGet, attachment.ID, fixture.foreignProject.ID)
			err := fixture.h.DownloadChatAttachment(c)
			require.Error(t, err)
			httpErr, ok := err.(*echo.HTTPError)
			require.True(t, ok)
			assert.Equal(t, http.StatusNotFound, httpErr.Code)
			assert.NotContains(t, rec.Body.String(), string(tc.content))
		})
	}
}

func TestDeleteChatAttachmentRejectsCrossProjectAttachment(t *testing.T) {
	fixture := setupChatAttachmentOwnershipFixture(t)

	c, _ := newChatAttachmentContext(t, fixture.e, http.MethodDelete, fixture.attachment.ID, fixture.foreignProject.ID)
	err := fixture.h.DeleteChatAttachment(c)

	require.Error(t, err)
	httpErr, ok := err.(*echo.HTTPError)
	require.True(t, ok)
	assert.Equal(t, http.StatusNotFound, httpErr.Code)
	stored, getErr := fixture.chatAttachmentRepo.GetByIDForProject(context.Background(), fixture.attachment.ID, fixture.owningProject.ID)
	require.NoError(t, getErr)
	require.NotNil(t, stored)
	assert.FileExists(t, fixture.attachment.FilePath)
	contents, readErr := os.ReadFile(fixture.attachment.FilePath)
	require.NoError(t, readErr)
	assert.Equal(t, fixture.fileContent, contents)
}

func TestDeleteChatAttachmentRemovesSameProjectAttachment(t *testing.T) {
	fixture := setupChatAttachmentOwnershipFixture(t)

	c, rec := newChatAttachmentContext(t, fixture.e, http.MethodDelete, fixture.attachment.ID, fixture.owningProject.ID)
	err := fixture.h.DeleteChatAttachment(c)

	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, rec.Code)
	stored, getErr := fixture.chatAttachmentRepo.GetByIDForProject(context.Background(), fixture.attachment.ID, fixture.owningProject.ID)
	require.NoError(t, getErr)
	assert.Nil(t, stored)
	assert.NoFileExists(t, fixture.attachment.FilePath)
	assert.Contains(t, rec.Body.String(), "No attachments")
}

func TestUploadChatAttachment_AllFileTypes(t *testing.T) {
	useTempUploadsDir(t)

	// Setup
	env := teststores.New(t)
	chatAttachmentRepo := env.ChatAttachments
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
		threadInputRepo:    env.ThreadInputs,
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
		{
			name:        "Spreadsheet with shared strings",
			filename:    "indexed.xlsx",
			contentType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
			content:     tinySharedStringXLSX(t),
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
				if tc.filename == "indexed.xlsx" {
					sessionID := response["session_id"].(string)
					indexPath := spreadsheetpreviewindex.SidecarPath(filepath.Join(uploadsDir, "chat", "pending", sessionID, tc.filename))
					values, readErr := spreadsheetpreviewindex.Read(indexPath, []int{1})
					require.NoError(t, readErr)
					require.Equal(t, map[string]string{"1": "indexed upload value"}, values)
				}
			}
		})
	}
}

func TestUploadChatAttachment_AppendsToExistingPendingSession(t *testing.T) {
	useTempUploadsDir(t)

	env := teststores.New(t)
	h := &Handler{
		chatAttachmentRepo: env.ChatAttachments,
		threadInputRepo:    env.ThreadInputs,
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

	env := teststores.New(t)
	threadInputRepo := env.ThreadInputs
	h := &Handler{
		chatAttachmentRepo: env.ChatAttachments,
		threadInputRepo:    threadInputRepo,
	}
	e := echo.New()
	const sessionID = "dddddddddddddddddddddddddddddddd"
	retired, err := threadInputRepo.RetireAttachmentSessionIfUnowned(context.Background(), sessionID)
	require.NoError(t, err)
	require.True(t, retired)

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

	stores := teststores.New(t, teststores.WithProductionTopology())
	h := &Handler{chatAttachmentRepo: stores.ChatAttachments, threadInputRepo: stores.ThreadInputs}

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

	env := teststores.New(t)
	h := &Handler{
		chatAttachmentRepo: env.ChatAttachments,
		threadInputRepo:    env.ThreadInputs,
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

	env := teststores.New(t)
	h := &Handler{
		chatAttachmentRepo: env.ChatAttachments,
		threadInputRepo:    env.ThreadInputs,
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

	env := teststores.New(t)
	h := &Handler{
		chatAttachmentRepo: env.ChatAttachments,
		threadInputRepo:    env.ThreadInputs,
	}
	e := echo.New()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for _, filename := range []string{"first.bin", "second.bin"} {
		part, err := writer.CreateFormFile("files", filename)
		require.NoError(t, err)
		_, err = io.Copy(part, bytes.NewReader(bytes.Repeat([]byte("x"), 14<<20)))
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
		assert.Equal(t, int64(14<<20), attachment.Size)
		assert.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", response.SessionID, attachment.Filename))
	}
}

func TestUploadChatAttachment_AllowsPaddedBoundaryDelimiterWhitespace(t *testing.T) {
	useTempUploadsDir(t)

	env := teststores.New(t)
	h := &Handler{
		chatAttachmentRepo: env.ChatAttachments,
		threadInputRepo:    env.ThreadInputs,
	}
	e := echo.New()
	files := []taskAttachmentTestFile{
		{name: "padded-first.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("a"), 14<<20)},
		{name: "padded-second.bin", contentType: "application/octet-stream", content: bytes.Repeat([]byte("b"), 14<<20)},
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
		assert.Equal(t, int64(14<<20), attachment.Size)
		assert.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", response.SessionID, attachment.Filename))
	}
}

func TestUploadChatAttachment_OversizedMultipartRequestIsBoundedBeforePendingSession(t *testing.T) {
	useTempUploadsDir(t)
	tmpMultipartDir := t.TempDir()
	t.Setenv("TMPDIR", tmpMultipartDir)

	env := teststores.New(t)
	h := &Handler{
		chatAttachmentRepo: env.ChatAttachments,
		threadInputRepo:    env.ThreadInputs,
	}
	e := echo.New()
	const sessionID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	req, body, totalSize := newSizedMultipartUploadRequestWithFilePrefix(t, http.MethodPost, "/chat/attachments", map[string]string{"attachment_session_id": sessionID}, "files", "too-large.bin", "application/octet-stream", 30<<20, invalidMultipartBoundaryPayloadPrefix(), false)
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
	env := teststores.New(t)
	chatAttachmentRepo := env.ChatAttachments
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
		threadInputRepo:    env.ThreadInputs,
	}

	e := echo.New()

	// Create a file larger than 10MB
	largeContent := make([]byte, 26*1024*1024) // 11MB

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
	env := teststores.New(t)
	chatAttachmentRepo := env.ChatAttachments
	h := &Handler{
		chatAttachmentRepo: chatAttachmentRepo,
		threadInputRepo:    env.ThreadInputs,
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
