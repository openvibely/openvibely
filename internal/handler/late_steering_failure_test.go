package handler

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil"
	"github.com/openvibely/openvibely/pkg/attachmentcontext"
	"github.com/stretchr/testify/require"
)

func TestLateSteeringCommitSurvivesModelFailureCleanup(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Late steering")
	task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
		task.Category, task.Status, task.AgentID = models.CategoryActive, models.StatusRunning, &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: "late steer"}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
	params := streamingResponseParams{ExecID: exec.ID}
	batch, err := h.claimPendingSteeringInputs(ctx, &params)
	require.NoError(t, err)
	require.Equal(t, 1, batch.count())
	commit := h.preparedSteeringCommit(params, batch)
	history := []any{map[string]any{"type": "message", "role": "user", "content": "late steer"}}
	require.NoError(t, commit(ctx, history))
	h.requeuePendingSteeringForExecution(ctx, exec.ID)
	stored, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
	require.Equal(t, models.ThreadInputModeSteering, stored.InputMode)
	replay, err := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
	require.NoError(t, err)
	require.Contains(t, replay[exec.ID][0].TranscriptJSON, "late steer")
	require.NoError(t, commit(ctx, append(history, map[string]any{"type": "message", "role": "assistant", "content": "answer"})))
}

func TestLateSteeringPreparationPropagatesCheckpointFailure(t *testing.T) {
	h, _, configs, env := setupTestHandlerWithStores(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	agent.Provider = models.ProviderOpenAI
	project := createProject(t, h, "Preparation failure")
	task := createTask(t, h, project.ID, "steering")
	exec := createExec(t, h, task.ID, agent.ID)
	checkpoint := []models.ExecutionReplayMessage{{TranscriptJSON: `{"responses_input":[{"type":"message","role":"user","content":"original"}]}`}}
	require.NoError(t, h.execRepo.ReplaceReasoningReplay(ctx, exec.ID, "", checkpoint))
	err := env.Fixtures.FailExecutionReplayWrites(ctx)
	require.NoError(t, err)
	params := streamingResponseParams{ExecID: exec.ID, Agent: *agent, Message: "previous steer", ChatHistory: []models.Execution{{ID: exec.ID + "-steering-context-1", ReplayMessages: checkpoint}}}
	batch := preparedSteeringBatch{inputs: []models.ThreadInput{{Content: "unsent steer"}}}
	_, err = h.prepareClaimedSteeringInputs(ctx, &params, "previous answer", batch, nil)
	require.ErrorContains(t, err, "injected replay failure")
	require.Equal(t, "previous steer", params.Message)
}

func TestSteeringPreparationFailureStopsHandler(t *testing.T) {
	h, _, configs, env := setupTestHandlerWithStores(t)
	h.workerSvc = nil
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Stop on preparation failure")
	task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
		task.Category, task.Status, task.AgentID = models.CategoryActive, models.StatusRunning, &agent.ID
	})
	exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	mock := testutil.NewMockLLMCaller()
	mock.Response = "answer"
	h.llmSvc.SetLLMCaller(mock)
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: "unsent steer"}
	mock.OnCall = func(context.Context, testutil.MockLLMCall) {
		require.Equal(t, 1, mock.CallCount())
		require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
		err := env.Fixtures.FailSteeringPreparation(ctx)
		require.NoError(t, err)
	}
	h.processStreamingResponse(streamingResponseParams{ExecID: exec.ID, TaskID: task.ID, ProjectID: project.ID, Agent: *agent, Message: "original", IsTaskFollowup: true, suppressQueuedTurnPromotion: true})
	require.Equal(t, 1, mock.CallCount())
	stored, err := h.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Equal(t, models.ExecFailed, stored.Status)
	require.Contains(t, stored.ErrorMessage, "injected preparation failure")
	queued, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, queued.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, queued.InputMode)
}

func TestSteeringAttachmentCommitRetriesAfterFailure(t *testing.T) {
	for _, failure := range []string{"delivery", "second-upload"} {
		t.Run(failure, func(t *testing.T) {
			h, _, configs := setupTestHandler(t)
			useTempUploadsDir(t)
			ctx := context.Background()
			agent := createAgent(t, configs)
			project := createProject(t, h, "Attachment retry")
			task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
				task.Category, task.Status, task.AgentID = models.CategoryActive, models.StatusRunning, &agent.ID
			})
			exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
			writeUpload := func(session string) {
				dir := filepath.Join(uploadsDir, "chat", "pending", session)
				require.NoError(t, os.MkdirAll(dir, 0o755))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "doc.pdf"), []byte(session), 0o644))
			}
			for _, session := range []string{"first", "second"} {
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: session, AttachmentSessionID: session}
				require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
			}
			writeUpload("first")
			if failure == "delivery" {
				writeUpload("second")
			}
			params := streamingResponseParams{ExecID: exec.ID}
			batch, err := h.claimPendingSteeringInputs(ctx, &params)
			require.NoError(t, err)
			require.Equal(t, 2, batch.count())
			commit := h.preparedSteeringCommit(params, batch)
			// An unserializable checkpoint fails delivery after attachments are staged.
			require.Error(t, commit(ctx, []any{make(chan int)}))
			require.FileExists(t, filepath.Join(uploadsDir, "chat", "pending", "first", "doc.pdf"))
			rows, err := h.attachmentRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Empty(t, rows)
			require.NoError(t, filepath.WalkDir(filepath.Join(uploadsDir, "chat", exec.ID), func(path string, entry os.DirEntry, err error) error {
				require.NoError(t, err)
				require.True(t, entry.IsDir(), "rollback left a file: %s", path)
				return nil
			}))
			writeUpload("second")
			require.NoError(t, commit(ctx, nil))
			require.NoError(t, commit(ctx, nil), "successful commits may be called again")
			rows, err = h.attachmentRepo.ListByTask(ctx, task.ID)
			require.NoError(t, err)
			require.Len(t, rows, 2)
			require.NotEqual(t, rows[0].FilePath, rows[1].FilePath)
			for _, session := range []string{"first", "second"} {
				require.NoDirExists(t, filepath.Join(uploadsDir, "chat", "pending", session))
			}
		})
	}
}

func TestSteeringDuplicateFilenamesKeepPreviewPaths(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryActive, models.CategoryChat} {
		t.Run(string(category), func(t *testing.T) {
			h, _, configs := setupTestHandler(t)
			useTempUploadsDir(t)
			ctx := context.Background()
			agent := createAgent(t, configs)
			agent.Provider = models.ProviderOpenAI
			project := createProject(t, h, "Steering paths")
			task := createTask(t, h, project.ID, "steering", func(task *models.Task) {
				task.Category, task.Status, task.AgentID = category, models.StatusRunning, &agent.ID
			})
			exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
			// A prior upload already occupies the original filename.
			original := filepath.Join(uploadsDir, "chat", exec.ID, "report.pdf")
			require.NoError(t, os.MkdirAll(filepath.Dir(original), 0o755))
			require.NoError(t, os.WriteFile(original, []byte("original"), 0o644))
			paths := make(map[string]string)
			for _, session := range []string{"first", "second"} {
				source := filepath.Join(uploadsDir, "chat", "pending", session, "report.pdf")
				require.NoError(t, os.MkdirAll(filepath.Dir(source), 0o755))
				require.NoError(t, os.WriteFile(source, []byte(session), 0o644))
				text, files, err := h.previewSteeringAttachments(session, exec.ID, agent.Provider)
				require.NoError(t, err)
				require.Len(t, files, 1)
				paths[session] = files[0].ReferencePath
				require.Contains(t, text, paths[session])
				require.NotEqual(t, original, paths[session])
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: "read this", AttachmentSessionID: session}
				if category == models.CategoryChat {
					input.Scope = models.ThreadInputScopeChat
				}
				require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
			}
			require.NotEqual(t, paths["first"], paths["second"])
			params := streamingResponseParams{ExecID: exec.ID, Agent: *agent}
			batch, err := h.claimPendingSteeringInputs(ctx, &params)
			require.NoError(t, err)
			require.Len(t, batch.inputs, 2)
			for _, input := range batch.inputs {
				require.Contains(t, input.Content, paths[input.AttachmentSessionID])
			}
			commit := h.preparedSteeringCommit(params, batch)
			require.Error(t, commit(ctx, []any{make(chan int)}))
			for session, path := range paths {
				_, files, err := h.previewSteeringAttachments(session, exec.ID, agent.Provider)
				require.NoError(t, err)
				require.Len(t, files, 1)
				require.Equal(t, path, files[0].ReferencePath, "retry must retain the original reference")
			}
			require.NoError(t, commit(ctx, nil))
			require.NoError(t, commit(ctx, nil))
			for session, path := range paths {
				data, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, session, string(data))
			}
			data, err := os.ReadFile(original)
			require.NoError(t, err)
			require.Equal(t, "original", string(data))
			var savedPaths []string
			if category == models.CategoryChat {
				rows, err := h.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
				require.NoError(t, err)
				for _, row := range rows {
					savedPaths = append(savedPaths, row.FilePath)
				}
			} else {
				rows, err := h.attachmentRepo.ListByTask(ctx, task.ID)
				require.NoError(t, err)
				for _, row := range rows {
					savedPaths = append(savedPaths, row.FilePath)
				}
			}
			require.ElementsMatch(t, []string{paths["first"], paths["second"]}, savedPaths)
		})
	}
}

func TestSteeringAttachmentRefreshFailureKeepsUploadForRetry(t *testing.T) {
	for _, category := range []models.TaskCategory{models.CategoryChat, models.CategoryActive} {
		for _, native := range []bool{true, false} {
			name := string(category) + "/continuation"
			if native {
				name = string(category) + "/native"
			}
			t.Run(name, func(t *testing.T) {
				h, _, configs := setupTestHandler(t)
				useTempUploadsDir(t)
				ctx := context.Background()
				agent := createAgent(t, configs)
				project := createProject(t, h, "Refresh failure")
				task := createTask(t, h, project.ID, "steering", func(task *models.Task) { task.Category = category; task.Status = models.StatusRunning })
				exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
				pending := filepath.Join(uploadsDir, "chat", "pending", "refresh-test")
				require.NoError(t, os.MkdirAll(pending, 0755))
				require.NoError(t, os.WriteFile(filepath.Join(pending, "report.pdf"), []byte("report"), 0600))
				input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: "read this", AttachmentSessionID: "refresh-test"}
				if category == models.CategoryChat {
					input.Scope = models.ThreadInputScopeChat
				}
				require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
				params := streamingResponseParams{ExecID: exec.ID, TaskID: task.ID, ProjectID: project.ID, Agent: *agent}
				key := task.ID
				if category == models.CategoryChat {
					key = attachmentcontext.ChatProjectKey(project.ID)
				}
				failRead := false
				loads := 0
				params.attachmentSource = attachmentcontext.NewSource(func(context.Context) (string, error) {
					loads++
					if failRead {
						return "", errors.New("injected database read failure")
					}
					return "references", nil
				}, key)
				require.NoError(t, params.attachmentSource.Load(ctx))
				batch, err := h.claimPendingSteeringInputs(ctx, &params)
				require.NoError(t, err)
				require.Len(t, batch.inputs, 1)
				commit := h.preparedSteeringCommit(params, batch)
				failRead = true
				if native {
					err = commit(ctx, nil)
				} else {
					err = h.prepareSteeringAttachmentDelivery(ctx, params, &batch)
				}
				require.ErrorContains(t, err, "couldn't refresh steering attachments; message was not sent")
				require.Equal(t, 2, loads)
				require.FileExists(t, filepath.Join(pending, "report.pdf"))
				taskFiles, err := h.attachmentRepo.ListByTask(ctx, task.ID)
				require.NoError(t, err)
				require.Empty(t, taskFiles)
				chatFiles, err := h.chatAttachmentRepo.ListByExecution(ctx, exec.ID)
				require.NoError(t, err)
				require.Empty(t, chatFiles)
				stored, err := h.threadInputRepo.GetByID(ctx, input.ID)
				require.NoError(t, err)
				require.Equal(t, models.ThreadInputPending, stored.InputStatus)
				replay, err := h.execRepo.ReplayMessagesByExecutionIDs(ctx, []string{exec.ID})
				require.NoError(t, err)
				require.Empty(t, replay[exec.ID])
				failRead = false
				if native {
					require.NoError(t, commit(ctx, nil))
					require.NoError(t, commit(ctx, nil))
				} else {
					require.NoError(t, h.prepareSteeringAttachmentDelivery(ctx, params, &batch))
					require.NoError(t, h.commitPreparedSteeringInputs(ctx, params, batch))
				}
				require.Equal(t, 3, loads)
				require.NoDirExists(t, pending)
				stored, err = h.threadInputRepo.GetByID(ctx, input.ID)
				require.NoError(t, err)
				require.Equal(t, models.ThreadInputApplied, stored.InputStatus)
			})
		}
	}
}

type failSteeringAttachmentRefreshStore struct{ persistence.AttachmentStore }

func (s failSteeringAttachmentRefreshStore) ListByTask(ctx context.Context, taskID string) ([]models.Attachment, error) {
	files, err := s.AttachmentStore.ListByTask(ctx, taskID)
	if err == nil && len(files) > 0 {
		return nil, errors.New("injected attachment refresh failure")
	}
	return files, err
}

func TestSteeringAttachmentRefreshFailurePreventsModelCall(t *testing.T) {
	h, _, configs := setupTestHandler(t)
	h.workerSvc = nil
	useTempUploadsDir(t)
	ctx := context.Background()
	agent := createAgent(t, configs)
	project := createProject(t, h, "Before send")
	task := createTask(t, h, project.ID, "steering", func(task *models.Task) { task.Status = models.StatusRunning })
	exec := createExec(t, h, task.ID, agent.ID, func(exec *models.Execution) { exec.Status = models.ExecRunning })
	pending := filepath.Join(uploadsDir, "chat", "pending", "before-send")
	require.NoError(t, os.MkdirAll(pending, 0755))
	require.NoError(t, os.WriteFile(filepath.Join(pending, "report.pdf"), []byte("report"), 0600))
	input := &models.ThreadInput{Scope: models.ThreadInputScopeTask, ProjectID: project.ID, TaskID: task.ID, AgentConfigID: agent.ID, ExpectedTurnID: exec.ID, Content: "read this", AttachmentSessionID: "before-send"}
	require.NoError(t, h.threadInputRepo.CreateSteeringForActiveExecution(ctx, input, exec.ID))
	originalStore := h.attachmentRepo
	h.attachmentRepo = failSteeringAttachmentRefreshStore{originalStore}
	mock := testutil.NewMockLLMCaller()
	h.llmSvc.SetLLMCaller(mock)
	h.processStreamingResponse(streamingResponseParams{ExecID: exec.ID, TaskID: task.ID, Task: task, ProjectID: project.ID, Agent: *agent, Message: "go", IsTaskFollowup: true, suppressQueuedTurnPromotion: true})
	require.Zero(t, mock.CallCount(), "do not send the steer if its attachment list cannot be loaded")
	stored, err := h.threadInputRepo.GetByID(ctx, input.ID)
	require.NoError(t, err)
	require.Equal(t, models.ThreadInputPending, stored.InputStatus)
	require.Equal(t, models.ThreadInputModeQueued, stored.InputMode)
	savedExec, err := h.execRepo.GetByID(ctx, exec.ID)
	require.NoError(t, err)
	require.Contains(t, savedExec.ErrorMessage, "couldn't refresh steering attachments")
	require.FileExists(t, filepath.Join(pending, "report.pdf"))
	files, err := originalStore.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, files)
}
