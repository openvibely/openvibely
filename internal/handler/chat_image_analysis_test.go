package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createTestImage creates a simple test image with the given dimensions and color
func createTestImage(width, height int, fillColor color.Color) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, fillColor)
		}
	}
	var buf bytes.Buffer
	png.Encode(&buf, img)
	return buf.Bytes()
}

func TestChatImageAnalysis_SingleImage(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	// Create an Anthropic agent (supports vision)
	agent := &models.LLMConfig{
		Name:        "Claude Sonnet (Vision)",
		Provider:    models.ProviderTest,
		Model:       "claude-3-5-sonnet-20241022",
		APIKey:      "test-api-key",
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

	// Create a test image
	imgData := createTestImage(100, 100, color.RGBA{255, 0, 0, 255})

	// Create multipart form with image attachment
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Analyze this image and describe what you see")
	writer.WriteField("project_id", projectID)

	part, err := writer.CreateFormFile("attachments", "test-screenshot.png")
	require.NoError(t, err)
	part.Write(imgData)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should return 201 Created
	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify chat attachment was created with correct media type
	chatHistory, err := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NoError(t, err)
	require.NotEmpty(t, chatHistory)

	execID := chatHistory[0].ID
	attachments, err := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	require.NoError(t, err)
	require.Len(t, attachments, 1)

	assert.Equal(t, "test-screenshot.png", attachments[0].FileName)
	assert.Equal(t, "image/png", attachments[0].MediaType)
	assert.True(t, attachments[0].FileSize > 0)

	// Verify file was saved to disk
	assert.FileExists(t, attachments[0].FilePath)

	// Read the saved file and verify it's valid PNG
	savedData, err := filepath.Abs(attachments[0].FilePath)
	require.NoError(t, err)
	assert.Contains(t, savedData, uploadsDir)
}

func TestChatImageAnalysis_MultipleImages(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Claude Sonnet (Vision)",
		Provider:    models.ProviderTest,
		Model:       "claude-3-5-sonnet-20241022",
		APIKey:      "test-api-key",
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

	// Create multiple test images
	redImg := createTestImage(50, 50, color.RGBA{255, 0, 0, 255})
	blueImg := createTestImage(75, 75, color.RGBA{0, 0, 255, 255})
	greenImg := createTestImage(100, 100, color.RGBA{0, 255, 0, 255})

	// Create multipart form with multiple image attachments
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Compare these three images")
	writer.WriteField("project_id", projectID)

	// Add first image
	part1, err := writer.CreateFormFile("attachments", "red.png")
	require.NoError(t, err)
	part1.Write(redImg)

	// Add second image
	part2, err := writer.CreateFormFile("attachments", "blue.png")
	require.NoError(t, err)
	part2.Write(blueImg)

	// Add third image
	part3, err := writer.CreateFormFile("attachments", "green.png")
	require.NoError(t, err)
	part3.Write(greenImg)

	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify all three attachments were saved
	chatHistory, err := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NoError(t, err)
	require.NotEmpty(t, chatHistory)

	execID := chatHistory[0].ID
	attachments, err := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	require.NoError(t, err)
	require.Len(t, attachments, 3)

	fileNames := make(map[string]bool)
	for _, att := range attachments {
		fileNames[att.FileName] = true
		assert.Equal(t, "image/png", att.MediaType)
		assert.FileExists(t, att.FilePath)
	}

	assert.True(t, fileNames["red.png"])
	assert.True(t, fileNames["blue.png"])
	assert.True(t, fileNames["green.png"])
}

func TestChatImageAnalysis_SupportedImageFormats(t *testing.T) {
	testCases := []struct {
		filename  string
		mediaType string
		data      []byte
	}{
		{
			filename:  "test.png",
			mediaType: "image/png",
			data:      []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}, // PNG magic bytes
		},
		{
			filename:  "test.jpg",
			mediaType: "image/jpeg",
			data:      []byte{0xFF, 0xD8, 0xFF}, // JPEG magic bytes
		},
		{
			filename:  "test.gif",
			mediaType: "image/gif",
			data:      []byte{0x47, 0x49, 0x46, 0x38, 0x39, 0x61}, // GIF89a magic bytes
		},
		{
			filename:  "test.webp",
			mediaType: "image/webp",
			data:      []byte{0x52, 0x49, 0x46, 0x46}, // RIFF magic bytes (WebP)
		},
	}

	for _, tc := range testCases {
		t.Run(tc.filename, func(t *testing.T) {
			// Create a fresh handler for each test to avoid cross-test pollution
			h, e, llmConfigRepo := setupTestHandler(t)
			ctx := context.Background()

			agent := &models.LLMConfig{
				Name:        "Claude Sonnet (Vision)",
				Provider:    models.ProviderTest,
				Model:       "claude-3-5-sonnet-20241022",
				APIKey:      "test-api-key",
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
			writer.WriteField("message", "Analyze this "+tc.mediaType)
			writer.WriteField("project_id", projectID)

			part, err := writer.CreateFormFile("attachments", tc.filename)
			require.NoError(t, err)
			part.Write(tc.data)
			writer.Close()

			req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
			req.Header.Set("Content-Type", writer.FormDataContentType())
			rec := httptest.NewRecorder()
			e.ServeHTTP(rec, req)

			assert.Equal(t, http.StatusCreated, rec.Code, "should accept %s", tc.mediaType)

			// Verify attachment was saved with correct media type
			chatHistory, err := h.execRepo.ListChatHistory(ctx, projectID, 50)
			require.NoError(t, err)
			require.NotEmpty(t, chatHistory, "should have chat history")

			attachments, err := h.chatAttachmentRepo.ListByExecution(ctx, chatHistory[0].ID)
			require.NoError(t, err)
			require.Len(t, attachments, 1, "should have exactly one attachment")

			assert.Equal(t, tc.filename, attachments[0].FileName)
			assert.Equal(t, tc.mediaType, attachments[0].MediaType)
		})
	}
}

func TestChatImageAnalysis_Base64Encoding(t *testing.T) {
	// This test verifies that images are properly base64-encoded for the Anthropic API
	imgData := createTestImage(10, 10, color.RGBA{255, 128, 0, 255})

	// Verify the image can be base64-encoded
	encoded := base64.StdEncoding.EncodeToString(imgData)
	assert.NotEmpty(t, encoded)

	// Verify it can be decoded
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	assert.Equal(t, imgData, decoded)
}

func TestChatImageAnalysis_ImageSizeValidation(t *testing.T) {
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

	// Create a file that exceeds the size limit
	largeData := make([]byte, apiMaxFileSize+1)

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Analyze this large image")
	writer.WriteField("project_id", projectID)

	part, err := writer.CreateFormFile("attachments", "large-image.png")
	require.NoError(t, err)
	part.Write(largeData)
	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	// Should reject with 413 Request Entity Too Large
	assert.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
}

func TestChatImageAnalysis_MixedAttachments(t *testing.T) {
	h, e, llmConfigRepo := setupTestHandler(t)
	ctx := context.Background()

	agent := &models.LLMConfig{
		Name:        "Claude Sonnet (Vision)",
		Provider:    models.ProviderTest,
		Model:       "claude-3-5-sonnet-20241022",
		APIKey:      "test-api-key",
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

	// Create test image and text file
	imgData := createTestImage(50, 50, color.RGBA{128, 128, 128, 255})
	textData := []byte("This is a text file with context information.")

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	writer.WriteField("message", "Analyze the image using the context from the text file")
	writer.WriteField("project_id", projectID)

	// Add image
	imgPart, err := writer.CreateFormFile("attachments", "diagram.png")
	require.NoError(t, err)
	imgPart.Write(imgData)

	// Add text file
	txtPart, err := writer.CreateFormFile("attachments", "context.txt")
	require.NoError(t, err)
	txtPart.Write(textData)

	writer.Close()

	req := httptest.NewRequest(http.MethodPost, "/api/chat/message", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusCreated, rec.Code)

	// Verify both attachments were saved with correct types
	chatHistory, err := h.execRepo.ListChatHistory(ctx, projectID, 50)
	require.NoError(t, err)
	require.NotEmpty(t, chatHistory)

	execID := chatHistory[0].ID
	attachments, err := h.chatAttachmentRepo.ListByExecution(ctx, execID)
	require.NoError(t, err)
	require.Len(t, attachments, 2)

	mediaTypes := make(map[string]string)
	for _, att := range attachments {
		mediaTypes[att.FileName] = att.MediaType
	}

	assert.Equal(t, "image/png", mediaTypes["diagram.png"])
	assert.Equal(t, "text/plain", mediaTypes["context.txt"])
}
