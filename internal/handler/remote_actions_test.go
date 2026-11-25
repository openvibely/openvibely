package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/remote"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/stretchr/testify/require"
)

func runAction[R any](t *testing.T, h *Handler, ctx context.Context, name string, args any) (R, remote.Result) {
	t.Helper()
	encoded, err := remote.Encode(args)
	require.NoError(t, err)
	res := h.runRemoteAction(ctx, remote.Action{Name: name, Args: encoded})
	var out R
	if res.Code == "" && res.Error == "" {
		require.NoError(t, remote.Decode(res.Values, &out))
	}
	return out, res
}

func TestRemoteActionsDenyInaccessibleProjects(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := t.Context()
	h.SetProjectAccessService(service.NewProjectAccessService(tc.stores.ProjectAccess))
	project := createProject(t, h, "Private actions")
	task := createTask(t, h, project.ID, "Secret task")
	policy, err := tc.stores.ProjectAccess.Get(ctx, project.ID)
	require.NoError(t, err)
	policy.Visibility = "private"
	require.NoError(t, tc.stores.ProjectAccess.Update(ctx, models.BootstrapUserID, *policy))
	org := service.NewOrganizationService(tc.stores.Organizations, "secret")
	token, err := org.Invite(ctx, models.BootstrapUserID, "member")
	require.NoError(t, err)
	user, err := org.Accept(ctx, token, "action-user", "long-password-value", "")
	require.NoError(t, err)
	outsider := models.WithPrincipal(ctx, user.Principal)
	alert := &models.Alert{ProjectID: project.ID, Title: "Private alert", Type: models.AlertCustom, Severity: models.SeverityInfo}
	require.NoError(t, tc.stores.Alerts.Create(ctx, alert))
	_, denied := runAction[any](t, h, outsider, "alert.mutate", alertMutationArgs{ProjectID: models.AllProjectsID, AlertID: alert.ID, Operation: "read"})
	require.True(t, denied.Code != "" || denied.Error != "", "inaccessible alert action must fail")
	unchanged, err := tc.stores.Alerts.GetByIDForProject(ctx, project.ID, alert.ID)
	require.NoError(t, err)
	require.False(t, unchanged.IsRead)

	for _, name := range []string{"changes.state", "worktree.info", "task.merge_menu", "changes.settings"} {
		_, res := runAction[any](t, h, outsider, name, task.ID)
		require.NotEmpty(t, res.Code, name)
		require.Empty(t, res.Values, name)
	}
	for _, test := range []struct {
		name string
		args any
	}{
		{"conversation.model", conversationModelArgs{ProjectID: project.ID, Write: true}},
		{"alert.mutate", alertMutationArgs{ProjectID: project.ID, Operation: "delete_all"}},
		{"automation.mutate", automationMutationArgs{ProjectID: project.ID, Operation: "delete_bulk"}},
		{"schedule.create", service.CreateAbsoluteScheduleForTaskRequest{ProjectID: project.ID, TaskID: task.ID}},
		{"schedule.calendar", calendarActionArgs{ProjectID: project.ID}},
		{"project.delete", project.ID},
		{"project.update", projectEditArgs{ProjectID: project.ID}},
		{"project.worker_limit", projectWorkerLimitArgs{ProjectID: project.ID}},
		{"webhook.delete_bulk", webhookMutationArgs{ProjectID: project.ID}},
		{"chat.api.send", apiChatSendArgs{ProjectID: project.ID, Message: "blocked"}},
		{"changes.file", changesFileArgs{TaskID: task.ID}},
		{"changes.branches", changesBranchesArgs{TaskID: task.ID, Page: 1}},
		{"changes.eligibility", changesEligibilityArgs{TaskID: task.ID, Target: "main"}},
		{"computer.enabled", ComputerArgs{ProjectID: project.ID}},
		{"computer.access", ComputerArgs{ProjectID: project.ID}},
		{"computer.lifecycle", computerLifecycleArgs{ComputerArgs: ComputerArgs{ProjectID: project.ID}, Action: "enable"}},
		{"computer.chrome", computerChromeArgs{ComputerArgs: ComputerArgs{ProjectID: project.ID}}},
		{"computer.chrome_import", computerChromeImportArgs{ComputerArgs: ComputerArgs{ProjectID: project.ID}}},
		{"computer.chrome_grants", computerGrantsArgs{ComputerArgs: ComputerArgs{ProjectID: project.ID}}},
	} {
		_, res := runAction[any](t, h, outsider, test.name, test.args)
		require.NotEmpty(t, res.Code, test.name)
		require.Empty(t, res.Values, test.name)
	}
	_, res := runAction[any](t, h, outsider, "file.read", fileReadArgs{Kind: fileKindAttachment, ID: "missing", ProjectID: project.ID})
	require.Equal(t, "status:404", res.Code)
	_, res = runAction[any](t, h, outsider, "no.such.action", task.ID)
	require.NotEmpty(t, res.Error)

	owner := models.WithPrincipal(ctx, models.Principal{UserID: models.BootstrapUserID})
	_, res = runAction[components.TaskCardMergeMenuState](t, h, owner, "task.merge_menu", task.ID)
	require.Empty(t, res.Code+res.Error)
}

func TestRemoteFileReadStreamsOriginalBytes(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	project := createProject(t, h, "Files")
	other := createProject(t, h, "Other")
	task := createTask(t, h, project.ID, "Files task")
	data := bytes.Repeat([]byte("d41d8cd98f00b204e9800998ecf8427e\n"), remoteFileChunk/16)
	path := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(path, data, 0600))
	att := &models.Attachment{TaskID: task.ID, FileName: "out.txt", FilePath: path, FileSize: int64(len(data))}
	require.NoError(t, h.attachmentRepo.Create(ctx, att))

	args := fileReadArgs{Kind: fileKindAttachment, ID: att.ID, ProjectID: other.ID}
	_, res := runAction[fileChunk](t, h, ctx, "file.read", args)
	require.Equal(t, "status:404", res.Code, "a row outside the named project must not be readable")

	var got []byte
	args.ProjectID = project.ID
	for {
		chunk, res := runAction[fileChunk](t, h, ctx, "file.read", args)
		require.Empty(t, res.Code, res.Error)
		require.EqualValues(t, len(data), chunk.Size)
		got = append(got, chunk.Data...)
		args.Offset += int64(len(chunk.Data))
		if args.Offset >= chunk.Size {
			break
		}
	}
	require.Equal(t, data, got)
}

func TestRemoteFileReaderSeeksAcrossChunks(t *testing.T) {
	data := make([]byte, remoteFileChunk*2+10)
	for i := range data {
		data[i] = byte(i % 251)
	}
	r := &remoteFileReader{chunk: fileChunk{Size: int64(len(data)), Data: data[:remoteFileChunk]}}
	buf := make([]byte, 100)
	n, err := io.ReadFull(r, buf)
	require.NoError(t, err)
	require.Equal(t, data[:n], buf)
	pos, err := r.Seek(0, io.SeekEnd)
	require.NoError(t, err)
	require.EqualValues(t, len(data), pos)
	_, err = r.Read(buf)
	require.Equal(t, io.EOF, err)
}

func TestRemoteActionCarriesEmbeddedTaskRef(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	project := createProject(t, h, "Refs")
	other := createProject(t, h, "Other refs")
	task := createTask(t, h, project.ID, "Ref task")

	_, res := runAction[*models.Task](t, h, ctx, "task.property", taskPropertyArgs{TaskRef: TaskRef{TaskID: task.ID, ProjectID: other.ID}, Field: "tag", Value: "bug"})
	require.Equal(t, "status:400", res.Code, "the embedded project must reach the server")

	_, res = runAction[*models.Task](t, h, ctx, "task.property", taskPropertyArgs{TaskRef: TaskRef{TaskID: task.ID, ProjectID: project.ID}, Field: "tag", Value: "bug"})
	require.Empty(t, res.Code, res.Error)
	updated, err := h.taskSvc.GetByID(ctx, task.ID)
	require.NoError(t, err)
	require.Equal(t, models.TaskTag("bug"), updated.Tag)
}

func TestStageUploadWritesInOrderWithinLimits(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	old := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() { uploadsDir = old })
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	project := createProject(t, h, "Staging")
	task := createTask(t, h, project.ID, "Stage task")
	token := generateSessionID()
	stage := func(name string, offset int64, data string) remote.Result {
		_, res := runAction[struct{}](t, h, ctx, "upload.stage", stageUploadArgs{TaskID: task.ID, Token: token, Name: name, Offset: offset, Data: []byte(data)})
		return res
	}

	require.Empty(t, stage("a.txt", 0, "hello ").Code)
	require.Equal(t, "status:409", stage("a.txt", 3, "x").Code, "out-of-order chunk")
	require.Equal(t, "status:409", stage("a.txt", 0, "again").Code, "restart of an existing file")
	require.Empty(t, stage("a.txt", 6, "world").Code)
	files, err := h.stageUploads(ctx, task.ID, "", nil)
	require.NoError(t, err)
	require.Empty(t, files)
	src, size, err := uploadFile{Name: "a.txt", Token: token}.open(task.ID)
	require.NoError(t, err)
	got, _ := io.ReadAll(src)
	src.Close()
	require.EqualValues(t, 11, size)
	require.Equal(t, "hello world", string(got))

	for _, name := range []string{"../escape.txt", "sub/x.txt", ".."} {
		require.Equal(t, "status:400", stage(name, 0, "x").Code, name)
	}
	_, res := runAction[struct{}](t, h, ctx, "upload.stage", stageUploadArgs{TaskID: task.ID, Token: "../bad", Name: "x.txt", Data: []byte("x")})
	require.Equal(t, "status:400", res.Code)

	for i := 1; i < maxTaskAttachmentFilesPerRequest; i++ {
		require.Empty(t, stage(fmt.Sprintf("f%d.txt", i), 0, "x").Code)
	}
	require.Equal(t, "status:400", stage("one-too-many.txt", 0, "x").Code)

	removeStagedUploads(task.ID, []uploadFile{{Name: "a.txt", Token: token}})
	_, err = os.Stat(filepath.Join(uploadsDir, "staged", task.ID, token))
	require.True(t, os.IsNotExist(err))
}

func TestRemotePendingUploadAndPreview(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	old := uploadsDir
	uploadsDir = t.TempDir()
	t.Cleanup(func() { uploadsDir = old })
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	project := createProject(t, h, "Remote uploads")
	session, token := generateSessionID(), generateSessionID()
	data := []byte("remote upload contents")
	_, res := runAction[struct{}](t, h, ctx, "upload.stage", stageUploadArgs{ProjectID: project.ID, Token: token, Name: "file.txt", Data: data})
	require.Empty(t, res.Code, res.Error)
	out, res := runAction[pendingUploadResult](t, h, ctx, "chat.upload", pendingUploadArgs{ProjectID: project.ID, SessionID: session, Files: []uploadFile{{Name: "file.txt", Token: token, Size: int64(len(data))}}})
	require.Empty(t, res.Code, res.Error)
	require.Equal(t, session, out.SessionID)
	require.Len(t, out.Attachments, 1)
	chunk, res := runAction[fileChunk](t, h, ctx, "file.read", fileReadArgs{Kind: fileKindPending, ID: session + "/file.txt"})
	require.Empty(t, res.Code, res.Error)
	require.Equal(t, data, chunk.Data)
	other := createProject(t, h, "Other upload project")
	_, res = runAction[pendingUploadResult](t, h, ctx, "chat.upload", pendingUploadArgs{ProjectID: other.ID, SessionID: session, Files: []uploadFile{{Name: "file.txt", Token: token}}})
	require.Equal(t, "status:404", res.Code)
	_, res = runAction[fileChunk](t, h, ctx, "file.read", fileReadArgs{Kind: fileKindPending, ID: session + "/../file.txt"})
	require.Equal(t, "status:404", res.Code)
}

func TestRemoteScheduleActionsRoundTrip(t *testing.T) {
	tc := NewStoreTestContext(t)
	h := tc.handler
	ctx := models.WithPrincipal(t.Context(), models.Principal{UserID: models.BootstrapUserID})
	project := createProject(t, h, "Remote scheduling")
	task := createTask(t, h, project.ID, "Schedule me")
	out, response := runAction[scheduleSaveResult](t, h, ctx, "schedule.create", service.CreateAbsoluteScheduleForTaskRequest{ProjectID: project.ID, TaskID: task.ID, RunAt: time.Now().Add(time.Hour), RepeatType: models.RepeatOnce})
	require.Empty(t, response.Code, response.Error)
	require.NotNil(t, out.Schedule)
	require.Equal(t, task.ID, out.Schedule.TaskID)
	toggled, response := runAction[scheduleToggleWire](t, h, ctx, "schedule.toggle", scheduleRef{ProjectID: project.ID, ScheduleID: out.Schedule.ID})
	require.Empty(t, response.Code, response.Error)
	require.NotNil(t, toggled.Schedule)
	require.False(t, toggled.Schedule.Enabled)
	deleted, response := runAction[*models.Task](t, h, ctx, "schedule.delete", scheduleRef{ProjectID: project.ID, ScheduleID: out.Schedule.ID})
	require.Empty(t, response.Code, response.Error)
	require.Equal(t, task.ID, deleted.ID)
}
