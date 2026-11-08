package handler

import (
	"context"
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

func malformedAttachmentSessions() []string {
	return []string{"../../outside", "../pending/../../outside", "/absolute", ".", "..", "short", strings.Repeat("A", 32), strings.Repeat("g", 32), strings.Repeat("a", 31), strings.Repeat("a", 33), " " + strings.Repeat("a", 32)}
}

func attachmentSentinel(t *testing.T) string {
	t.Helper()
	useTempUploadsDir(t)
	path := filepath.Join(uploadsDir, "outside", "sentinel.txt")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0755))
	require.NoError(t, os.WriteFile(path, []byte("private sentinel"), 0600))
	return path
}

func assertAttachmentSentinelUntouched(t *testing.T, path string) {
	t.Helper()
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "private sentinel", string(content))
	// No published copy or other filesystem side effect is permitted.
	require.NoDirExists(t, filepath.Join(uploadsDir, "chat"))
}

func TestAttachmentSessionSubmissionRejectsMalformedIDs(t *testing.T) {
	for _, thread := range []bool{false, true} {
		for _, running := range []bool{false, true} {
			name := "chat"
			if thread {
				name = "task-thread"
			}
			if running {
				name += "/queued"
			} else {
				name += "/idle"
			}
			t.Run(name, func(t *testing.T) {
				h, e, configs := setupTestHandler(t)
				ctx := context.Background()
				sentinel := attachmentSentinel(t)
				agent := createAgent(t, configs)
				project := createProject(t, h, "Session validation")
				task := createTask(t, h, project.ID, "Existing conversation", func(task *models.Task) {
					task.Category = models.CategoryChat
					if thread {
						task.Category = models.CategoryCompleted
					}
					task.Status = models.StatusCompleted
					task.AgentID = &agent.ID
					if running {
						task.Status = models.StatusRunning
						if thread {
							task.Category = models.CategoryActive
						}
					}
				})
				createExec(t, h, task.ID, agent.ID, func(ex *models.Execution) {
					ex.Status = models.ExecCompleted
					if running {
						ex.Status = models.ExecRunning
					}
				})
				tasksBefore, err := h.taskRepo.ListByProject(ctx, project.ID, "")
				require.NoError(t, err)
				execsBefore, err := h.execRepo.ListByTask(ctx, task.ID)
				require.NoError(t, err)
				endpoint := "/chat/send?project_id=" + project.ID
				if thread {
					endpoint = "/tasks/" + task.ID + "/thread"
				}
				for _, session := range malformedAttachmentSessions() {
					form := url.Values{"message": {"Read attached files"}, "agent_id": {agent.ID}, "attachment_session_id": {session}}
					rec := htmxPost(e, endpoint, form)
					require.Equal(t, http.StatusBadRequest, rec.Code, "session %q: %s", session, rec.Body.String())
					require.Contains(t, rec.Body.String(), "invalid attachment session ID")
					tasks, err := h.taskRepo.ListByProject(ctx, project.ID, "")
					require.NoError(t, err)
					require.Equal(t, tasksBefore, tasks)
					execs, err := h.execRepo.ListByTask(ctx, task.ID)
					require.NoError(t, err)
					require.Equal(t, execsBefore, execs)
					chatInputs, err := h.threadInputRepo.ListPendingForChat(ctx, project.ID)
					require.NoError(t, err)
					require.Empty(t, chatInputs)
					taskInputs, err := h.threadInputRepo.ListPendingForTask(ctx, task.ID)
					require.NoError(t, err)
					require.Empty(t, taskInputs)
					assertAttachmentSentinelUntouched(t, sentinel)
				}
			})
		}
	}
}

func TestAttachmentSessionStagingRejectsMalformedIDsBeforeSideEffects(t *testing.T) {
	sentinel := attachmentSentinel(t)
	// Missing repositories also ensure validation precedes ownership/metadata access.
	h := &Handler{}
	for _, steering := range []bool{false, true} {
		for _, session := range append(malformedAttachmentSessions(), "") {
			text, files, rows, finish, err := h.stageMessageAttachmentsForDelivery(context.Background(), session, "execution", steering)
			require.EqualError(t, err, "invalid attachment session ID")
			require.Empty(t, text)
			require.Empty(t, files)
			require.Empty(t, rows)
			require.Nil(t, finish)
			assertAttachmentSentinelUntouched(t, sentinel)
		}
	}
}

func TestGeneratedAttachmentSessionUploadPreviewAndPublication(t *testing.T) {
	h, e, configs := setupTestHandler(t)
	useTempUploadsDir(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Valid attachments")
	task := createTask(t, h, project.ID, "Chat", func(task *models.Task) { task.Category = models.CategoryChat })
	exec := createExec(t, h, task.ID, agent.ID)
	session := uploadChatAttachmentForTest(t, e, h, "note.txt", []byte("attachment contents"), "text/plain", "")
	require.True(t, isValidPendingAttachmentSessionID(session))
	rec := httptest.NewRecorder()
	c := e.NewContext(httptest.NewRequest(http.MethodGet, "/preview", nil), rec)
	c.SetParamNames("session", "name")
	c.SetParamValues(session, "note.txt")
	require.NoError(t, h.PreviewPendingAttachment(c))
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, "attachment contents", rec.Body.String())
	_, _, rows, err := h.processAttachmentsWithReturn(ctx, session, exec.ID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	content, err := os.ReadFile(rows[0].FilePath)
	require.NoError(t, err)
	require.Equal(t, "attachment contents", string(content))
	stored, err := h.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.NoDirExists(t, filepath.Join(uploadsDir, "chat", "pending", session))
}
