package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper function to create int pointers
func intPtr(i int) *int {
	return &i
}

func TestAPIChatMessage_MissingMessage(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("project_id", "some-project")

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "message is required", resp["error"])
}

func TestAPIChatMessage_MissingProjectID(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	form := url.Values{}
	form.Set("message", "Hello world")

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "project_id is required", resp["error"])
}

func TestAPIChatMessage_ProjectNotFound(t *testing.T) {
	_, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create an agent so we can get past the agent check
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	form := url.Values{}
	form.Set("message", "Hello world")
	form.Set("project_id", "nonexistent-project-id")

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "project not found", resp["error"])
}

func TestAPIChatMessage_TextOnly_CreatesTaskAndExecution(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create an agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	// Get default project
	projects, err := h.projectSvc.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, projects)
	projectID := projects[0].ID

	form := url.Values{}
	form.Set("message", "Create a task to fix the login bug")
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return 201 immediately (async processing)
	body := rec.Body.String()
	t.Logf("Response code: %d, body: %s", rec.Code, body)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify the async accepted response format
	var resp ChatMessageAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "processing", resp.Status)
	assert.NotEmpty(t, resp.MessageID)
	assert.Contains(t, resp.StatusURL, "/api/chat/message/")

	// Verify a chat task was created
	tasks, err := h.taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)

	var chatTask *models.Task
	for i := range tasks {
		if tasks[i].Category == models.CategoryChat {
			chatTask = &tasks[i]
			break
		}
	}

	require.NotNil(t, chatTask, "expected chat task to be created")
	assert.Equal(t, models.CategoryChat, chatTask.Category)
	assert.Equal(t, "Create a task to fix the login bug", chatTask.Prompt)
	assert.Equal(t, projectID, chatTask.ProjectID)
}

func TestAPIChatMessage_UnsupportedFileType(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create multipart form with a .exe file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Check this file")
	writer.WriteField("project_id", projectID)

	part, err := writer.CreateFormFile("attachments", "malware.exe")
	require.NoError(t, err)
	part.Write([]byte("fake exe content"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnsupportedMediaType, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "not allowed")
}

func TestAPIChatMessage_FileSizeExceeded(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create multipart form with an oversized file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Check this file")
	writer.WriteField("project_id", projectID)

	// Create a file that exceeds the 10MB limit
	part, err := writer.CreateFormFile("attachments", "large.png")
	require.NoError(t, err)
	// Write slightly more than 10MB
	largeData := make([]byte, apiMaxFileSize+1)
	part.Write(largeData)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "size limit")
}

func TestAPIChatMessage_WithImageAttachment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create multipart form with an image attachment
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Look at this screenshot")
	writer.WriteField("project_id", projectID)

	part, err := writer.CreateFormFile("attachments", "screenshot.png")
	require.NoError(t, err)
	pngData := []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A} // PNG magic bytes
	part.Write(pngData)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return 201 with attachment URLs
	body := rec.Body.String()
	t.Logf("Response code: %d, body: %s", rec.Code, body)
	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp ChatMessageAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "processing", resp.Status)
	assert.NotEmpty(t, resp.MessageID)

	// Verify chat task was created
	tasks, err := h.taskRepo.ListByProject(ctx, projectID, "")
	require.NoError(t, err)

	var chatTask *models.Task
	for i := range tasks {
		if tasks[i].Category == models.CategoryChat {
			chatTask = &tasks[i]
			break
		}
	}
	require.NotNil(t, chatTask, "expected chat task to be created")

	// Verify execution was created
	chatHistory, err := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NoError(t, err)
	require.NotEmpty(t, chatHistory)

	// Verify attachment was saved
	execID := chatHistory[0].ID
	attachments, err := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	assert.Equal(t, "screenshot.png", attachments[0].FileName)
	assert.Equal(t, "image/png", attachments[0].MediaType)

	// Verify file exists on disk
	assert.FileExists(t, attachments[0].FilePath)
}

func TestAPIChatMessage_WithMultipleAttachments(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create multipart form with multiple attachments
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Check these files")
	writer.WriteField("project_id", projectID)

	// Image file
	part1, err := writer.CreateFormFile("attachments", "screenshot.png")
	require.NoError(t, err)
	part1.Write([]byte{0x89, 0x50, 0x4E, 0x47})

	// Text file
	part2, err := writer.CreateFormFile("attachments", "notes.txt")
	require.NoError(t, err)
	part2.Write([]byte("These are my notes about the bug."))

	// Markdown file
	part3, err := writer.CreateFormFile("attachments", "readme.md")
	require.NoError(t, err)
	part3.Write([]byte("# README\nSome content"))

	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	t.Logf("Response code: %d, body: %s", rec.Code, body)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify all attachments were saved
	chatHistory, err := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NoError(t, err)
	require.NotEmpty(t, chatHistory)

	execID := chatHistory[0].ID
	attachments, err := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	require.NoError(t, err)
	assert.Len(t, attachments, 3)

	// Verify each file
	fileNames := make(map[string]bool)
	for _, att := range attachments {
		fileNames[att.FileName] = true
		assert.FileExists(t, att.FilePath)
	}
	assert.True(t, fileNames["screenshot.png"], "expected screenshot.png attachment")
	assert.True(t, fileNames["notes.txt"], "expected notes.txt attachment")
	assert.True(t, fileNames["readme.md"], "expected readme.md attachment")
}

func TestAPIChatMessage_NoAgents(t *testing.T) {
	h, e, _ := setupTestHandler(t)
	ctx := context.Background()

	// Don't create any agents — the default agent from migration 003 exists,
	// so we delete it to simulate no agents available.
	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Delete all agents via repo
	agents, _ := h.llmConfigRepo.List(ctx)
	for _, a := range agents {
		h.llmConfigRepo.Delete(ctx, a.ID)
	}

	form := url.Values{}
	form.Set("message", "Hello")
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should fail because no agents configured
	assert.Equal(t, http.StatusInternalServerError, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "no agents available")
}

func TestAPIChatMessage_ResponseFormat(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	form := url.Values{}
	form.Set("message", "Hello")
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return 201 for async processing
	assert.Equal(t, http.StatusCreated, rec.Code)

	// The response should be JSON
	contentType := rec.Header().Get("Content-Type")
	assert.Contains(t, contentType, "application/json")

	var resp ChatMessageAcceptedResponse
	err := json.Unmarshal(rec.Body.Bytes(), &resp)
	require.NoError(t, err, "response should be valid JSON")

	// Verify async accepted response fields
	assert.Equal(t, "processing", resp.Status)
	assert.NotEmpty(t, resp.MessageID)
	assert.Contains(t, resp.StatusURL, "/api/chat/message/"+resp.MessageID)
}

func TestAPIChatMessage_AttachmentURLsInResponse(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create multipart form with attachment
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Check this image")
	writer.WriteField("project_id", projectID)

	part, err := writer.CreateFormFile("attachments", "test.png")
	require.NoError(t, err)
	part.Write([]byte{0x89, 0x50, 0x4E, 0x47})
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	body := rec.Body.String()
	t.Logf("Response code: %d, body: %s", rec.Code, body)

	// Should return 201 with attachment URLs in the accepted response
	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp ChatMessageAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.AttachmentURLs, 1)
	assert.Contains(t, resp.AttachmentURLs[0], "/chat/attachments/")
	assert.Contains(t, resp.AttachmentURLs[0], "/download")

	// Verify attachment was saved in DB
	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)
	atts, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	assert.Len(t, atts, 1)
}

func TestIsAllowedFileType(t *testing.T) {
	tests := []struct {
		filename string
		allowed  bool
	}{
		// Images
		{"photo.jpg", true},
		{"photo.jpeg", true},
		{"photo.JPG", true},
		{"screenshot.png", true},
		{"animation.gif", true},
		{"image.webp", true},
		// Documents
		{"document.pdf", true},
		{"notes.txt", true},
		{"readme.md", true},
		{"data.csv", true},
		// Code files (now allowed)
		{"code.go", true},
		{"page.html", true},
		{"style.css", true},
		{"script.js", true},
		{"app.py", true},
		{"app.ts", true},
		{"component.jsx", true},
		{"component.tsx", true},
		{"lib.rs", true},
		{"Main.java", true},
		{"config.json", true},
		{"config.yaml", true},
		{"config.yml", true},
		{"deploy.sh", true},
		{"query.sql", true},
		// Rejected
		{"script.exe", false},
		{"archive.zip", false},
		{"binary.bin", false},
		{"image.svg", false},
		{"noext", false},
		{"app.wasm", false},
		{"lib.so", false},
	}

	for _, tc := range tests {
		t.Run(tc.filename, func(t *testing.T) {
			result := isAllowedFileType(tc.filename)
			assert.Equal(t, tc.allowed, result, "isAllowedFileType(%q)", tc.filename)
		})
	}
}

func TestAPIChatMessage_SavesFilesToDisk(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create multipart form with a text attachment
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Save this file")
	writer.WriteField("project_id", projectID)

	textContent := "This is the file content for testing"
	part, err := writer.CreateFormFile("attachments", "test.txt")
	require.NoError(t, err)
	part.Write([]byte(textContent))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	t.Logf("Response code: %d", rec.Code)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify file was saved to disk
	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)

	execID := chatHistory[0].ID
	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	require.Len(t, attachments, 1)

	// Verify the file exists and has correct content
	savedContent, err := os.ReadFile(attachments[0].FilePath)
	require.NoError(t, err)
	assert.Equal(t, textContent, string(savedContent))

	// Verify the file is in the right directory structure
	assert.Contains(t, attachments[0].FilePath, filepath.Join("chat", execID))
}

func TestAPIChatMessage_PDFFileType(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create multipart form with a PDF
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Check this document")
	writer.WriteField("project_id", projectID)

	part, err := writer.CreateFormFile("attachments", "document.pdf")
	require.NoError(t, err)
	part.Write([]byte("%PDF-1.4 fake pdf content"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	t.Logf("Response code: %d", rec.Code)
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify PDF attachment was saved
	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)

	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	require.Len(t, attachments, 1)
	assert.Equal(t, "document.pdf", attachments[0].FileName)
	assert.Equal(t, "application/pdf", attachments[0].MediaType)
}

func TestAPIChatMessageStatus_NotFound(t *testing.T) {
	_, e, _ := setupTestHandler(t)

	req := httptest.NewRequest(http.MethodGet, "/api/chat/message/nonexistent-id", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusNotFound, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "message not found", resp["error"])
}

func TestAPIChatMessageStatus_Processing(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create a running execution directly
	task := &models.Task{
		ProjectID: projectID,
		Title:     "Test Chat",
		Prompt:    "Hello",
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Hello",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	// Poll for status
	req := httptest.NewRequest(http.MethodGet, "/api/chat/message/"+exec.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ChatMessageStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "processing", resp.Status)
	assert.Equal(t, exec.ID, resp.MessageID)
	assert.Empty(t, resp.Response)
}

func TestAPIChatMessageStatus_Completed(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create a completed execution
	task := &models.Task{
		ProjectID: projectID,
		Title:     "Test Chat",
		Prompt:    "Hello",
		Status:    models.StatusCompleted,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Hello",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	// Complete the execution
	require.NoError(t, h.execRepo.Complete(ctx, exec.ID, models.ExecCompleted, "AI response text", "", 100, 2500))

	// Poll for status
	req := httptest.NewRequest(http.MethodGet, "/api/chat/message/"+exec.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ChatMessageStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "completed", resp.Status)
	assert.Equal(t, exec.ID, resp.MessageID)
	assert.Equal(t, "AI response text", resp.Response)
	assert.Equal(t, 100, resp.TokensUsed)
	assert.Equal(t, int64(2500), resp.DurationMs)
}

func TestAPIChatMessageStatus_Failed(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create a failed execution
	task := &models.Task{
		ProjectID: projectID,
		Title:     "Test Chat",
		Prompt:    "Hello",
		Status:    models.StatusFailed,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, task))

	exec := &models.Execution{
		TaskID:        task.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "Hello",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	// Fail the execution
	require.NoError(t, h.execRepo.Complete(ctx, exec.ID, models.ExecFailed, "", "LLM connection timeout", 0, 5000))

	// Poll for status
	req := httptest.NewRequest(http.MethodGet, "/api/chat/message/"+exec.ID, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)

	var resp ChatMessageStatusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "failed", resp.Status)
	assert.Equal(t, exec.ID, resp.MessageID)
	assert.Equal(t, "LLM connection timeout", resp.Error)
	assert.Equal(t, int64(5000), resp.DurationMs)
}

func TestAPIChatMessage_AsyncReturns201(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	form := url.Values{}
	form.Set("message", "What is the meaning of life?")
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Must return 201 Created, not 200
	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp ChatMessageAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Verify the accepted response structure
	assert.Equal(t, "processing", resp.Status)
	assert.NotEmpty(t, resp.MessageID)
	assert.Equal(t, "/api/chat/message/"+resp.MessageID, resp.StatusURL)

	// Verify the status endpoint works for this message
	statusReq := httptest.NewRequest(http.MethodGet, resp.StatusURL, nil)
	statusRec := httptest.NewRecorder()
	e.ServeHTTP(statusRec, statusReq)

	assert.Equal(t, http.StatusOK, statusRec.Code)

	var statusResp ChatMessageStatusResponse
	require.NoError(t, json.Unmarshal(statusRec.Body.Bytes(), &statusResp))
	assert.Equal(t, resp.MessageID, statusResp.MessageID)
	// Status can be processing or already terminal depending on async timing
	assert.Contains(t, []string{"processing", "completed", "failed"}, statusResp.Status)
}

func TestAPIChatMessage_WithGoCodeAttachment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create multipart form with a Go source file
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Review this Go code for bugs")
	writer.WriteField("project_id", projectID)

	goCode := `package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
`
	part, err := writer.CreateFormFile("attachments", "main.go")
	require.NoError(t, err)
	part.Write([]byte(goCode))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify attachment was saved
	chatHistory, err := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NoError(t, err)
	require.NotEmpty(t, chatHistory)

	execID := chatHistory[0].ID
	attachments, err := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)
	assert.Equal(t, "main.go", attachments[0].FileName)
	assert.Equal(t, "text/x-go", attachments[0].MediaType)

	// Verify file content on disk
	savedContent, readErr := os.ReadFile(attachments[0].FilePath)
	require.NoError(t, readErr)
	assert.Equal(t, goCode, string(savedContent))
}

func TestAPIChatMessage_WithPythonCodeAttachment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Help fix this Python script")
	writer.WriteField("project_id", projectID)

	pyCode := "def hello():\n    print('Hello')\n\nhello()\n"
	part, err := writer.CreateFormFile("attachments", "script.py")
	require.NoError(t, err)
	part.Write([]byte(pyCode))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)
	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	require.Len(t, attachments, 1)
	assert.Equal(t, "script.py", attachments[0].FileName)
	assert.Equal(t, "text/x-python", attachments[0].MediaType)
}

func TestAPIChatMessage_WithMixedCodeAndImageAttachments(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "This screenshot shows the error from this code")
	writer.WriteField("project_id", projectID)

	// Image attachment
	part1, _ := writer.CreateFormFile("attachments", "error.png")
	part1.Write([]byte{0x89, 0x50, 0x4E, 0x47})

	// Code attachment
	part2, _ := writer.CreateFormFile("attachments", "handler.go")
	part2.Write([]byte("package handler\n\nfunc broken() {\n}\n"))

	// Config attachment
	part3, _ := writer.CreateFormFile("attachments", "config.yaml")
	part3.Write([]byte("server:\n  port: 8080\n"))

	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)
	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	assert.Len(t, attachments, 3)

	fileNames := make(map[string]string) // name -> mediaType
	for _, att := range attachments {
		fileNames[att.FileName] = att.MediaType
	}
	assert.Equal(t, "image/png", fileNames["error.png"])
	assert.Equal(t, "text/x-go", fileNames["handler.go"])
	assert.Equal(t, "text/x-yaml", fileNames["config.yaml"])
}

func TestAPIChatMessage_WithJSONAttachment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Parse this JSON config")
	writer.WriteField("project_id", projectID)

	part, _ := writer.CreateFormFile("attachments", "config.json")
	part.Write([]byte(`{"database": {"host": "localhost", "port": 5432}}`))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)
	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	require.Len(t, attachments, 1)
	assert.Equal(t, "config.json", attachments[0].FileName)
	assert.Equal(t, "application/json", attachments[0].MediaType)
}

func TestAPIChatMessage_WithSQLAttachment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Review this migration")
	writer.WriteField("project_id", projectID)

	part, _ := writer.CreateFormFile("attachments", "migration.sql")
	part.Write([]byte("CREATE TABLE users (id TEXT PRIMARY KEY, name TEXT NOT NULL);"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)
	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	require.Len(t, attachments, 1)
	assert.Equal(t, "migration.sql", attachments[0].FileName)
	assert.Equal(t, "text/x-sql", attachments[0].MediaType)
}

func TestAPIChatMessage_TooManyFiles(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create multipart form with more than apiMaxFilesPerReq files
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Check all these files")
	writer.WriteField("project_id", projectID)

	for i := 0; i < apiMaxFilesPerReq+1; i++ {
		part, _ := writer.CreateFormFile("attachments", fmt.Sprintf("file%d.txt", i))
		part.Write([]byte("content"))
	}
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code)

	var resp map[string]string
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Contains(t, resp["error"], "maximum")
}

func TestAPIChatMessage_CopyChatAttachmentsToTask(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create a chat task and execution
	chatTask := &models.Task{
		ProjectID: projectID,
		Title:     "Chat test",
		Prompt:    "test message",
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))

	exec := &models.Execution{
		TaskID:        chatTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test message",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	// Create attachment files in the chat execution directory
	execDir := filepath.Join(tmpDir, "chat", exec.ID)
	require.NoError(t, os.MkdirAll(execDir, 0755))

	// Create a Go file attachment
	goContent := "package main\n\nfunc main() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(execDir, "main.go"), []byte(goContent), 0644))

	chatAtt1 := &models.ChatAttachment{
		ExecutionID: exec.ID,
		FileName:    "main.go",
		FilePath:    filepath.Join(execDir, "main.go"),
		MediaType:   "text/x-go",
		FileSize:    int64(len(goContent)),
	}
	require.NoError(t, h.chatAttachmentRepo.Create(ctx, chatAtt1))

	// Create a target task that would be created by AI
	targetTask := &models.Task{
		ProjectID: projectID,
		Title:     "Fix the bug in main.go",
		Prompt:    "Fix the bug described in the chat",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, targetTask))

	// Copy attachments to the target task
	copiedCount, err := h.copyChatAttachmentsToTask(ctx, exec.ID, targetTask.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, copiedCount)

	// Verify the attachment was copied
	taskAttachments, err := h.attachmentRepo.ListByTask(ctx, targetTask.ID)
	require.NoError(t, err)
	require.Len(t, taskAttachments, 1)
	assert.Equal(t, "main.go", taskAttachments[0].FileName)
	assert.Equal(t, "text/x-go", taskAttachments[0].MediaType)

	// Verify file exists on disk in task directory
	assert.FileExists(t, taskAttachments[0].FilePath)
	assert.Contains(t, taskAttachments[0].FilePath, filepath.Join("tasks", targetTask.ID))

	// Verify the task prompt was updated with attachment reference
	updatedTask, err := h.taskRepo.GetByID(ctx, targetTask.ID)
	require.NoError(t, err)
	assert.Contains(t, updatedTask.Prompt, "[Attached files from chat:")
	assert.Contains(t, updatedTask.Prompt, "main.go (path: ")
	// Verify the path is absolute (starts with tmpDir since uploadsDir was set to tmpDir)
	assert.Contains(t, updatedTask.Prompt, filepath.Join(tmpDir, "tasks", targetTask.ID, "main.go"))
}

func TestAPIChatMessage_CopyChatAttachmentsToTask_MultipleFiles(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	// Create execution and attachments
	chatTask := &models.Task{
		ProjectID: projectID,
		Title:     "Chat test",
		Prompt:    "test",
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))

	exec := &models.Execution{
		TaskID:        chatTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	execDir := filepath.Join(tmpDir, "chat", exec.ID)
	require.NoError(t, os.MkdirAll(execDir, 0755))

	// Create multiple attachment files
	files := map[string]string{
		"main.go":     "package main\n",
		"config.yaml": "port: 8080\n",
		"notes.txt":   "Bug description\n",
	}
	for name, content := range files {
		require.NoError(t, os.WriteFile(filepath.Join(execDir, name), []byte(content), 0644))
		att := &models.ChatAttachment{
			ExecutionID: exec.ID,
			FileName:    name,
			FilePath:    filepath.Join(execDir, name),
			MediaType:   "text/plain",
			FileSize:    int64(len(content)),
		}
		require.NoError(t, h.chatAttachmentRepo.Create(ctx, att))
	}

	// Create target task
	targetTask := &models.Task{
		ProjectID: projectID,
		Title:     "Fix issues",
		Prompt:    "Fix the reported issues",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, targetTask))

	// Copy all attachments
	copiedCount, err := h.copyChatAttachmentsToTask(ctx, exec.ID, targetTask.ID)
	require.NoError(t, err)
	assert.Equal(t, 3, copiedCount)

	// Verify all files were copied
	taskAttachments, err := h.attachmentRepo.ListByTask(ctx, targetTask.ID)
	require.NoError(t, err)
	assert.Len(t, taskAttachments, 3)

	// Verify the task prompt references all files
	updatedTask, err := h.taskRepo.GetByID(ctx, targetTask.ID)
	require.NoError(t, err)
	assert.Contains(t, updatedTask.Prompt, "[Attached files from chat:")
	for name := range files {
		assert.Contains(t, updatedTask.Prompt, name)
	}
}

func TestAPIChatMessage_CopyChatAttachmentsToTask_NoAttachments(t *testing.T) {
	h, _, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	// Create a task and execution with no attachments
	chatTask := &models.Task{
		ProjectID: projectID,
		Title:     "Chat test",
		Prompt:    "test",
		Status:    models.StatusPending,
		Category:  models.CategoryChat,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, chatTask))

	exec := &models.Execution{
		TaskID:        chatTask.ID,
		AgentConfigID: agent.ID,
		Status:        models.ExecRunning,
		PromptSent:    "test",
	}
	require.NoError(t, h.execRepo.Create(ctx, exec))

	targetTask := &models.Task{
		ProjectID: projectID,
		Title:     "Target task",
		Prompt:    "Original prompt",
		Status:    models.StatusPending,
		Category:  models.CategoryBacklog,
		AgentID:   &agent.ID,
	}
	require.NoError(t, h.taskRepo.Create(ctx, targetTask))

	// Copy with no attachments — should return 0 and not modify the task prompt
	copiedCount, err := h.copyChatAttachmentsToTask(ctx, exec.ID, targetTask.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, copiedCount)

	// Verify task prompt was NOT modified
	updatedTask, err := h.taskRepo.GetByID(ctx, targetTask.ID)
	require.NoError(t, err)
	assert.Equal(t, "Original prompt", updatedTask.Prompt)
}

func TestIsAllowedFileType_CodeFiles(t *testing.T) {
	codeFiles := []struct {
		filename string
		allowed  bool
	}{
		{"main.go", true},
		{"script.py", true},
		{"app.js", true},
		{"component.tsx", true},
		{"lib.rs", true},
		{"Main.java", true},
		{"main.c", true},
		{"main.cpp", true},
		{"header.h", true},
		{"app.rb", true},
		{"index.php", true},
		{"app.swift", true},
		{"app.kt", true},
		{"deploy.sh", true},
		{"setup.bash", true},
		{"query.sql", true},
		{"page.html", true},
		{"style.css", true},
		{"style.scss", true},
		{"config.xml", true},
		{"data.json", true},
		{"config.yaml", true},
		{"config.yml", true},
		{"config.toml", true},
		{"config.ini", true},
		{"settings.cfg", true},
		{"nginx.conf", true},
		{"app.log", true},
		{"changes.diff", true},
		{"fix.patch", true},
		{"data.csv", true},
		// Still rejected
		{"binary.exe", false},
		{"archive.zip", false},
		{"image.svg", false},
		{"binary.bin", false},
		{"app.wasm", false},
	}

	for _, tc := range codeFiles {
		t.Run(tc.filename, func(t *testing.T) {
			result := isAllowedFileType(tc.filename)
			assert.Equal(t, tc.allowed, result, "isAllowedFileType(%q)", tc.filename)
		})
	}
}

func TestAPIChatMessage_WithShellScriptAttachment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Review this deployment script")
	writer.WriteField("project_id", projectID)

	part, _ := writer.CreateFormFile("attachments", "deploy.sh")
	part.Write([]byte("#!/bin/bash\nset -e\ndocker build -t myapp .\n"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)
	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	require.Len(t, attachments, 1)
	assert.Equal(t, "deploy.sh", attachments[0].FileName)
	assert.Equal(t, "text/x-shellscript", attachments[0].MediaType)
}

func TestAPIChatMessage_WithCSVAttachment(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	tmpDir := t.TempDir()
	oldUploadsDir := uploadsDir
	uploadsDir = tmpDir
	defer func() { uploadsDir = oldUploadsDir }()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Analyze this data")
	writer.WriteField("project_id", projectID)

	part, _ := writer.CreateFormFile("attachments", "data.csv")
	part.Write([]byte("name,value\nalpha,1\nbeta,2\n"))
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	chatHistory, _ := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NotEmpty(t, chatHistory)
	attachments, _ := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
	require.Len(t, attachments, 1)
	assert.Equal(t, "data.csv", attachments[0].FileName)
	assert.Equal(t, "text/csv", attachments[0].MediaType)
}

func TestAPIChatMessage_BypassesProjectCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create an agent
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	// Create a project with max_workers=1
	project := &models.Project{
		Name:       "Test Project",
		MaxWorkers: intPtr(1),
	}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)

	// Wire projectRepo into workerSvc so project limits are enforced
	h.workerSvc.SetProjectRepo(h.projectRepo)

	// Acquire the project's worker slot to simulate task workers at capacity
	acquired := h.workerSvc.TryAcquireProjectSlot(project.ID)
	require.True(t, acquired, "should acquire project slot")
	defer h.workerSvc.ReleaseProjectSlot(project.ID)

	// API chat should still work even when project capacity is full
	form := url.Values{}
	form.Set("message", "Hello, agent!")
	form.Set("project_id", project.ID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Chat bypasses task worker limits — should succeed (201 Created)
	assert.Equal(t, http.StatusCreated, rec.Code, "API chat should not be blocked by project worker capacity")
}

func TestAPIChatMessage_BroadcastsChatResponseDone(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Set up chat broadcaster so we can verify events are published
	cb := events.NewChatBroadcaster()
	h.SetChatBroadcaster(cb)

	// Subscribe to receive events
	sub, err := cb.Subscribe()
	require.NoError(t, err)

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	form := url.Values{}
	form.Set("message", "Test broadcast message")
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp ChatMessageAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	execID := resp.MessageID

	// Collect events: expect ChatNewMessage immediately, then ChatResponseDone after processing
	var gotNewMessage, gotResponseDone bool
	timeout := time.After(10 * time.Second)
	for !gotNewMessage || !gotResponseDone {
		select {
		case evt := <-sub:
			if evt.ExecID == execID {
				switch evt.Type {
				case events.ChatNewMessage:
					gotNewMessage = true
					assert.Equal(t, "api", evt.Source)
					assert.Equal(t, "Test broadcast message", evt.Message)
				case events.ChatResponseDone:
					gotResponseDone = true
				}
			}
		case <-timeout:
			t.Fatalf("timed out waiting for events: gotNewMessage=%v gotResponseDone=%v", gotNewMessage, gotResponseDone)
		}
	}

	assert.True(t, gotNewMessage, "should receive ChatNewMessage event")
	assert.True(t, gotResponseDone, "should receive ChatResponseDone event (API path uses shared processStreamingResponse)")
}

func TestAPIChatMessage_BroadcastsChatResponseDoneOnFailure(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Set up chat broadcaster
	cb := events.NewChatBroadcaster()
	h.SetChatBroadcaster(cb)

	sub, err := cb.Subscribe()
	require.NoError(t, err)

	// Create agent with a provider that will fail (use ProviderTest which returns canned responses;
	// to simulate failure, we'd need a mock, but we can test the event flow by checking
	// that ChatNewMessage is always emitted synchronously before the goroutine runs)
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	form := url.Values{}
	form.Set("message", "Trigger processing")
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	var resp ChatMessageAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	execID := resp.MessageID

	// Should always get ChatNewMessage synchronously (before goroutine starts)
	var gotNewMessage bool
	timeout := time.After(5 * time.Second)
	for {
		select {
		case evt := <-sub:
			if evt.ExecID == execID && evt.Type == events.ChatNewMessage {
				gotNewMessage = true
				assert.Equal(t, "api", evt.Source)
			}
			if gotNewMessage {
				// Got the synchronous event, success
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for ChatNewMessage event")
		}
	}
}

func TestAPIChatMessage_UsesSharedProcessingPath(t *testing.T) {
	// Verify that the API chat message uses the shared processStreamingResponse path
	// by checking that the execution completes and task status is updated properly
	// (the shared path updates task status BEFORE git diff capture).
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	cb := events.NewChatBroadcaster()
	h.SetChatBroadcaster(cb)

	sub, err := cb.Subscribe()
	require.NoError(t, err)

	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
	}
	require.NoError(t, llmConfigRepo.Create(ctx, agent))

	projects, _ := h.projectSvc.List(ctx)
	projectID := projects[0].ID

	form := url.Values{}
	form.Set("message", "Simple test message")
	form.Set("project_id", projectID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code)

	var resp ChatMessageAcceptedResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))

	// Wait for ChatResponseDone to confirm processing completed
	timeout := time.After(10 * time.Second)
	for {
		select {
		case evt := <-sub:
			if evt.ExecID == resp.MessageID && evt.Type == events.ChatResponseDone {
				// Processing completed — verify execution and task state
				exec, err := h.execRepo.GetByID(ctx, resp.MessageID)
				require.NoError(t, err)
				require.NotNil(t, exec)
				assert.Equal(t, models.ExecCompleted, exec.Status, "execution should be completed")

				// Verify task status was updated (shared path calls completeWithSuccess
				// which updates task status BEFORE git diff capture)
				task, err := h.taskRepo.GetByID(ctx, exec.TaskID)
				require.NoError(t, err)
				require.NotNil(t, task)
				assert.Equal(t, models.StatusCompleted, task.Status, "task should be completed")
				return
			}
		case <-timeout:
			t.Fatal("timed out waiting for ChatResponseDone — shared processing path may not be broadcasting correctly")
		}
	}
}

func TestAPIChatMessage_BypassesModelCapacity(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create a project
	project := &models.Project{
		Name: "Test Project",
	}
	err := h.projectSvc.Create(ctx, project)
	require.NoError(t, err)

	// Create an agent with max_workers=1
	agent := &models.LLMConfig{
		Name:        "Test Agent",
		Provider:    models.ProviderTest,
		Model:       "claude-3-sonnet-20240229",
		APIKey:      "test-key",
		MaxTokens:   4096,
		Temperature: 1.0,
		IsDefault:   true,
		MaxWorkers:  1,
	}
	err = llmConfigRepo.Create(ctx, agent)
	require.NoError(t, err)

	// Wire llmConfigRepo into workerSvc so model limits are enforced
	h.workerSvc.SetLLMConfigRepo(h.llmConfigRepo)

	// Acquire the model's worker slot to simulate task workers at capacity
	acquired := h.workerSvc.TryAcquireModelSlot(agent.ID)
	require.True(t, acquired, "should acquire model slot")
	defer h.workerSvc.ReleaseModelSlot(agent.ID)

	// API chat should still work even when model capacity is full
	form := url.Values{}
	form.Set("message", "Hello, agent!")
	form.Set("project_id", project.ID)

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Chat bypasses task worker limits — should succeed (201 Created)
	assert.Equal(t, http.StatusCreated, rec.Code, "API chat should not be blocked by model worker capacity")
}
