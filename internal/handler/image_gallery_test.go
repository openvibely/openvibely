package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"
)

func TestImageGalleryAttachmentAccess(t *testing.T) {
	tc := NewTestContext(t)
	useTempUploadsDir(t)
	p := tc.CreateProject().Build()
	other := tc.CreateProject().Build()
	task := tc.CreateTask(p.ID).Build()
	path := filepath.Join(uploadsDir, "image.png")
	require.NoError(t, os.WriteFile(path, []byte("image"), 0600))
	att := &models.Attachment{TaskID: task.ID, FileName: "image.png", FilePath: path, MediaType: "image/png"}
	require.NoError(t, tc.handler.attachmentRepo.Create(context.Background(), att))
	rec := tc.HTTP().Get("/attachments/" + att.ID + "/download?project_id=" + p.ID).Execute()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "image", rec.Body.String())
	rec = tc.HTTP().Get("/attachments/" + att.ID + "/download?project_id=" + other.ID).Execute()
	require.Equal(t, http.StatusNotFound, rec.Code)
	session := "01234567890123456789012345678901"
	dir := filepath.Join(uploadsDir, "chat", "pending", session)
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "draft.png"), []byte("draft"), 0600))
	rec = tc.HTTP().Get("/chat/attachments/pending/" + session + "/draft.png").Execute()
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "draft", rec.Body.String())
	for _, url := range []string{"/chat/attachments/pending/invalid/draft.png", "/chat/attachments/pending/" + session + "/%2e%2e%2fimage.png"} {
		rec = tc.HTTP().Get(url).Execute()
		require.Equal(t, http.StatusNotFound, rec.Code)
	}
}

func TestNewTaskMessageImagesRemainInAttachmentPanel(t *testing.T) {
	h, e, modelsRepo := setupTestHandler(t)
	useTempUploadsDir(t)
	project := createProject(t, h, "Gallery first send")
	other := createProject(t, h, "Other gallery")
	agent := createAgent(t, modelsRepo)
	var upload bytes.Buffer
	writer := multipart.NewWriter(&upload)
	file, err := writer.CreateFormFile("files", "screenshot.png")
	require.NoError(t, err)
	_, err = file.Write([]byte("\x89PNG\r\n\x1a\nimage"))
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	req := httptest.NewRequest(http.MethodPost, "/chat/attachments?project_id="+project.ID, &upload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var uploaded struct {
		SessionID string `json:"session_id"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &uploaded))
	require.NotEmpty(t, uploaded.SessionID)
	form := url.Values{"message": {"Inspect screenshot"}, "agent_id": {agent.ID}, "attachment_session_id": {uploaded.SessionID}}
	req = httptest.NewRequest(http.MethodPost, "/tasks?project_id="+project.ID+"&from=new&thread=1", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	rec = httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	id := rec.Header().Get("X-Created-Task-ID")
	require.NotEmpty(t, id)
	require.Contains(t, rec.Body.String(), `/tasks/`+id+`/attachments?project_id=`+project.ID)
	for _, projectID := range []string{project.ID, other.ID} {
		req = httptest.NewRequest(http.MethodGet, "/tasks/"+id+"/attachments?project_id="+projectID, nil)
		rec = httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if projectID == other.ID {
			require.Equal(t, http.StatusNotFound, rec.Code)
			continue
		}
		require.Equal(t, http.StatusOK, rec.Code)
		require.Contains(t, rec.Body.String(), "screenshot.png")
		require.Contains(t, rec.Body.String(), "data-image-gallery-item")
		require.Contains(t, rec.Body.String(), "/chat/attachments/")
		require.NotContains(t, rec.Body.String(), "No attachments")
	}
	taskFiles, err := h.attachmentRepo.ListByTask(context.Background(), id)
	require.NoError(t, err)
	require.Empty(t, taskFiles, "message files must not become automatic inputs to every task execution")
	messageFiles, err := h.chatAttachmentRepo.ListByTask(context.Background(), id)
	require.NoError(t, err)
	require.Len(t, messageFiles, 1)
}
