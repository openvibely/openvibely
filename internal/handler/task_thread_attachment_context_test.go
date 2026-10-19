package handler

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/stretchr/testify/require"
)

func TestTaskThreadFollowupSeesPanelUploadAndAttachmentsBeyondHistoryLimit(t *testing.T) {
	h, e, configs := setupTestHandler(t)
	withTaskAttachmentUploadsDir(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Panel uploads")
	task := createTask(t, h, project.ID, "Thread", func(tk *models.Task) {
		tk.Category = models.CategoryCompleted
		tk.Status = models.StatusCompleted
		tk.AgentID = &agent.ID
	})
	first := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
		ex.Status = models.ExecCompleted
		ex.PromptSent = "first"
	})
	require.NoError(t, h.attachmentRepo.Create(ctx, &models.Attachment{TaskID: task.ID, ExecutionID: first.ID, FileName: "ancient.png", FilePath: "/missing/ancient.png", MediaType: "image/png", FileSize: 1}))
	for i := 0; i < taskThreadHistoryLimit+3; i++ {
		createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecCompleted; ex.PromptSent = "turn" })
	}

	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	fw, err := w.CreateFormFile("files", "spec.pdf")
	require.NoError(t, err)
	_, _ = fw.Write([]byte("%PDF-1.4"))
	require.NoError(t, w.Close())
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+task.ID+"/attachments?project_id="+url.QueryEscape(project.ID), &body)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	chatRows, err := h.chatAttachmentRepo.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, chatRows)

	current := createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) { ex.Status = models.ExecRunning; ex.IsFollowup = true })
	mock := testutil.NewMockLLMCaller()
	mock.Response, mock.TextOnly = "ok", "ok"
	h.llmSvc.SetLLMCaller(mock)
	h.processStreamingResponse(streamingResponseParams{ExecID: current.ID, TaskID: task.ID, Task: task, Message: "Read the spec", Agent: *agent, ProjectID: project.ID, IsTaskFollowup: true, DeferHistoryLoad: true, suppressQueuedTurnPromotion: true})

	require.Len(t, mock.AgentRequests, 1)
	request := mock.AgentRequests[0]
	text := attachmentcontext.Text(request.Ctx)
	require.Contains(t, text, "spec.pdf")
	require.Contains(t, text, "ancient.png")
	require.NotContains(t, text, "[message ")
	require.NotContains(t, text, "[task]")
	require.Empty(t, request.Attachments, "historical files are references, not re-sent inputs")
	require.NotContains(t, request.Message, "spec.pdf")
	for _, hist := range request.ChatHistory {
		require.NotEqual(t, first.ID, hist.ID, "attachment must be included even when its turn is outside history")
	}
}

func TestTaskThreadAttachmentContextRefreshesOnAddAndDelete(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Live attachments")
	task := createTask(t, h, project.ID, "Thread", func(tk *models.Task) { tk.Category = models.CategoryActive })
	exec := createExec(t, h, task.ID, agent.ID)
	params := streamingResponseParams{ExecID: exec.ID, TaskID: task.ID, Task: task, IsTaskFollowup: true, Message: "go"}
	_, err := h.prepareChatAttachmentContext(ctx, &params)
	require.NoError(t, err)
	reqCtx := params.withAttachmentContext(ctx)
	require.Empty(t, attachmentcontext.Text(reqCtx))

	att := &models.Attachment{TaskID: task.ID, FileName: "added.pdf", FilePath: "/missing/added.pdf", MediaType: "application/pdf", FileSize: 1}
	require.NoError(t, h.attachmentRepo.Create(ctx, att))
	require.Contains(t, attachmentcontext.Text(reqCtx), "added.pdf")

	type msg struct{ role, text string }
	read := func(m msg) (string, string) { return m.role, m.text }
	mk := func(s string) msg { return msg{"user", s} }
	history := attachmentcontext.Ensure(reqCtx, []msg{{"user", "go"}}, read, mk)
	require.Len(t, history, 2)

	require.NoError(t, h.attachmentRepo.Delete(ctx, att.ID))
	require.Empty(t, attachmentcontext.Text(reqCtx))
	history = attachmentcontext.Ensure(reqCtx, append(history, msg{"assistant", "done"}), read, mk)
	for _, m := range history {
		require.NotContains(t, m.text, "added.pdf", "deleted attachment context must be removed")
	}
}

func TestMessageAttachmentsRouteByConversationType(t *testing.T) {
	for _, tc := range []struct {
		category models.TaskCategory
		thread   bool
	}{{models.CategoryChat, false}, {models.CategoryActive, true}, {models.CategoryBacklog, true}} {
		t.Run(string(tc.category), func(t *testing.T) {
			h, _, configs := setupTestHandler(t)
			useTempUploadsDir(t)
			ctx := context.Background()
			agent := createAgent(t, configs)
			project := createProject(t, h, "Routing")
			task := createTask(t, h, project.ID, "Conversation", func(tk *models.Task) { tk.Category = tc.category })
			exec := createExec(t, h, task.ID, agent.ID)
			pending := filepath.Join(uploadsDir, "chat", "pending", "s1")
			require.NoError(t, os.MkdirAll(pending, 0o755))
			require.NoError(t, os.WriteFile(filepath.Join(pending, "doc.pdf"), []byte("%PDF"), 0o644))

			_, _, display, err := h.processAttachmentsWithReturn(ctx, "s1", exec.ID)
			require.NoError(t, err)
			require.Len(t, display, 1)
			taskRows, err := h.attachmentRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			chatRows, err := h.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
			require.NoError(t, err)
			if tc.thread {
				require.Len(t, taskRows, 1)
				require.Equal(t, exec.ID, taskRows[0].ExecutionID)
				require.Equal(t, display[0].ID, taskRows[0].ID)
				require.Empty(t, chatRows)
				require.True(t, strings.HasPrefix(taskRows[0].FilePath, filepath.Join(uploadsDir, "chat", exec.ID)))
			} else {
				require.Empty(t, taskRows)
				require.Len(t, chatRows, 1)
			}
		})
	}
}
