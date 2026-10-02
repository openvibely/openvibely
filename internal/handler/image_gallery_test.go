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

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
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

func TestDeferredTaskImagesRemainAvailable(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(fmt.Sprint(scheduled), func(t *testing.T) {
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
			form := url.Values{"category": {"backlog"}, "message": {"Inspect screenshot"}, "agent_id": {agent.ID}, "attachment_session_id": {uploaded.SessionID}}
			if scheduled {
				form.Set("add_schedule", "on")
				form.Set("run_at", "2035-01-02T09:30")
				form.Set("repeat_type", "weekly")
				form.Set("repeat_interval", "1")
			}
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

				require.NotContains(t, rec.Body.String(), "No attachments")
			}
			taskFiles, err := h.attachmentRepo.ListByTask(context.Background(), id)
			require.NoError(t, err)
			require.Len(t, taskFiles, 1)
			require.FileExists(t, taskFiles[0].FilePath)
			executions, err := h.execRepo.ListByTask(context.Background(), id)
			require.NoError(t, err)
			require.Empty(t, executions)
			messageFiles, err := h.chatAttachmentRepo.ListByTask(context.Background(), id)
			require.NoError(t, err)
			require.Empty(t, messageFiles)
		})
	}
}

func TestSwarmComposerUploadsSurviveCreationAndFollowup(t *testing.T) {
	for _, category := range []string{"active", "backlog", "scheduled"} {
		t.Run(category, func(t *testing.T) {
			h, e, repo := setupTestHandler(t)
			useTempUploadsDir(t)
			project := createProject(t, h, "Swarm uploads")
			agent := createAgent(t, repo)
			session := "01234567890123456789012345678901"
			dir := filepath.Join(uploadsDir, "chat", "pending", session)
			require.NoError(t, os.MkdirAll(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "image.png"), []byte("first image"), 0600))
			form := url.Values{"category": {category}, "title": {"Swarm images"}, "message": {"Inspect images"}, "agent_id": {agent.ID}, "swarm_mode": {"on"}, "attachment_session_id": {session}}
			if category == "scheduled" {
				form.Set("add_schedule", "on")
				form.Set("run_at", "2035-01-02T09:30")
				form.Set("repeat_type", "weekly")
				form.Set("repeat_interval", "1")
			}
			send := func(path string, values url.Values) *httptest.ResponseRecorder {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(values.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				req.Header.Set("HX-Request", "true")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				return rec
			}
			rec := send("/tasks?project_id="+project.ID+"&from=new&thread=1", form)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			location, err := url.Parse(rec.Header().Get("HX-Location"))
			require.NoError(t, err)
			id := strings.TrimPrefix(location.Path, "/tasks/")
			require.NotEmpty(t, id)
			files, err := h.attachmentRepo.ListByTask(context.Background(), id)
			require.NoError(t, err)
			require.Len(t, files, 1)
			firstPath := files[0].FilePath
			planner, err := h.taskRepo.FindSwarmChildByRole(context.Background(), id, models.SwarmRolePlanner)
			require.NoError(t, err)
			if category == "active" {
				require.NotNil(t, planner)
			} else {
				require.Nil(t, planner)
			}
			require.NoError(t, os.WriteFile(filepath.Join(dir, "image.png"), []byte("second image"), 0600))
			nextModel := createAgent(t, repo, func(a *models.LLMConfig) { a.Name = "Follow-up model" })
			rec = send("/tasks/"+id+"/thread?project_id="+project.ID, url.Values{"message": {"Inspect another image"}, "attachment_session_id": {session}, "agent_id": {nextModel.ID}})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			parent, err := h.taskRepo.GetByID(context.Background(), id)
			require.NoError(t, err)
			require.NotNil(t, parent.AgentID)
			require.Equal(t, nextModel.ID, *parent.AgentID)
			planner, err = h.taskRepo.FindSwarmChildByRole(context.Background(), id, models.SwarmRolePlanner)
			require.NoError(t, err)
			require.NotNil(t, planner)
			require.Equal(t, nextModel.ID, *planner.AgentID)
			files, err = h.attachmentRepo.ListByTask(context.Background(), id)
			require.NoError(t, err)
			require.Len(t, files, 2)
			data, err := os.ReadFile(firstPath)
			require.NoError(t, err)
			require.Equal(t, "first image", string(data))
		})
	}
}

func TestSwarmFollowupAutoSelectsPlannerModel(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryActive, models.CategoryBacklog} {
		for _, selection := range []string{"auto", ""} {
			t.Run(string(category)+"/"+selection, func(t *testing.T) {
				h, e, repo := setupTestHandler(t)
				ctx := context.Background()
				project := createProject(t, h, "Auto swarm")
				first := createAgent(t, repo)
				second := createAgent(t, repo, func(a *models.LLMConfig) { a.Name = "Other model" })
				message := "Explain the next step"
				expected, err := h.selectTaskAgent(ctx, project.ID, "auto", message, false)
				require.NoError(t, err)
				old := first
				if expected.ID == first.ID {
					old = second
				}
				parent, err := h.swarmSvc.CreateSwarmTask(ctx, service.CreateSwarmTaskRequest{ProjectID: project.ID, Title: "Auto planner", Prompt: "Build it", Category: category, AgentID: &old.ID})
				require.NoError(t, err)
				values := url.Values{"message": {message}, "agent_id": {selection}}
				req := httptest.NewRequest(http.MethodPost, "/tasks/"+parent.ID+"/thread?project_id="+project.ID, strings.NewReader(values.Encode()))
				req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, req)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
				planner, err := h.taskRepo.FindSwarmChildByRole(ctx, parent.ID, models.SwarmRolePlanner)
				require.NoError(t, err)
				require.NotNil(t, planner)
				require.NotNil(t, planner.AgentID)
				require.Equal(t, expected.ID, *planner.AgentID)
				saved, err := h.taskRepo.GetByID(ctx, parent.ID)
				require.NoError(t, err)
				require.Nil(t, saved.AgentID, "Auto must not retain the previous fixed model")
			})
		}
	}
}
