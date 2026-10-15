package service

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/openvibely/openvibely/pkg/attachment"
	"github.com/stretchr/testify/require"
)

type fileRuntimeAttachmentStore struct{ persistence.AttachmentStore }

func TestPublishFileDurabilityOwnershipAndExplicitContext(t *testing.T) {
	ctx := context.Background()
	stores := teststores.New(t)
	projects := stores.Projects
	tasks := stores.Tasks
	executions := stores.Executions
	attachments := stores.Attachments
	project := &models.Project{Name: "Files"}
	require.NoError(t, projects.Create(ctx, project))
	other := &models.Project{Name: "Other"}
	require.NoError(t, projects.Create(ctx, other))
	task := &models.Task{ProjectID: project.ID, Title: "Files", Category: models.CategoryActive, Status: models.StatusPending}
	require.NoError(t, tasks.Create(ctx, task))
	execution := &models.Execution{TaskID: task.ID, Status: models.ExecRunning}
	require.NoError(t, executions.Create(ctx, execution))
	broadcaster := events.NewBroadcaster()
	sub, err := broadcaster.SubscribeProject(project.ID)
	require.NoError(t, err)
	defer broadcaster.Unsubscribe(sub)
	svc := &LLMService{broadcaster: broadcaster, execRepo: executions, attachmentRepo: attachments, taskSvc: &TaskService{uploadsDir: t.TempDir()}}
	work := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(work, "demo.html"), []byte("<button>Demo</button>"), 0600))
	req := llmcontracts.AgentRequest{Agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey}, Ctx: ctx, ExecID: execution.ID, ProjectID: project.ID, WorkDir: work}
	runtime := svc.fileRuntime(req)
	require.NotNil(t, runtime)
	out, handled, failed, err := runtime.Executor(ctx, "publish_file", json.RawMessage(`{"file_path":"demo.html","display":"canvas"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, failed)
	select {
	case event := <-sub:
		require.Equal(t, events.TaskFilesChanged, event.Type)
		require.Equal(t, task.ID, event.TaskID)
		require.Equal(t, project.ID, event.ProjectID)
		require.Equal(t, execution.ID, event.ExecID)
		files, err := attachments.ListGeneratedFiles(ctx, project.ID, task.ID)
		require.NoError(t, err)
		require.Len(t, files, 1, "notification must follow persistence")
	default:
		t.Fatal("published file did not notify the open task panel")
	}
	var result struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &result))
	require.NotEmpty(t, result.ID)
	require.NoError(t, os.RemoveAll(work))
	file, err := attachments.GeneratedFile(ctx, result.ID, project.ID)
	require.NoError(t, err)
	require.Equal(t, "canvas", file.Display)
	orphan := filepath.Join(svc.taskSvc.uploadsDir, "generated", "orphan.txt")
	require.NoError(t, os.WriteFile(orphan, []byte("orphan"), 0600))
	removed, err := attachments.CleanupOrphanedFiles(ctx, svc.taskSvc.uploadsDir)
	require.NoError(t, err)
	require.Equal(t, 1, removed, "only the unregistered file should be removed")
	data, err := os.ReadFile(file.FilePath)
	require.NoError(t, err)
	require.Equal(t, "<button>Demo</button>", string(data))
	hidden, err := attachments.GeneratedFile(ctx, result.ID, other.ID)
	require.NoError(t, err)
	require.Nil(t, hidden)
	inputs, err := attachments.ListByTask(ctx, task.ID)
	require.NoError(t, err)
	require.Empty(t, inputs, "outputs must not become automatic inputs")
	wrong := *file
	wrong.ID = ""
	wrong.ProjectID = other.ID
	require.Error(t, attachments.CreateGeneratedFile(ctx, &wrong))
	req.ChatMode = models.ChatModePlan
	runtime = svc.fileRuntime(req)
	for _, def := range runtime.Definitions {
		require.NotEqual(t, "publish_file", def.Name)
	}
	req.AgentDefinition = &models.Agent{ToolConfig: models.AgentToolConfig{SkipDefaultTools: true}}
	require.Nil(t, svc.fileRuntime(req))
}

func TestFileRuntimeHonorsAgentGrants(t *testing.T) {
	svc := &LLMService{attachmentRepo: &fileRuntimeAttachmentStore{}}
	for _, tc := range []struct {
		tools         []string
		read, publish bool
	}{
		{nil, true, true}, {[]string{"WebSearch"}, false, false},
		{[]string{"Write"}, false, false}, {[]string{"Read"}, true, false},
		{[]string{"Read", "Write"}, true, true},
	} {
		req := llmcontracts.AgentRequest{Agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey}, ExecID: "exec", ProjectID: "project", AgentDefinition: &models.Agent{Tools: tc.tools}}
		rt := svc.fileRuntime(req)
		if !tc.read {
			require.Nil(t, rt)
			continue
		}
		require.NotNil(t, rt)
		require.True(t, rt.HasDefinition("view_image"))
		require.False(t, rt.HasDefinition("read_file_content"))
		out, handled, failed, err := rt.Executor(context.Background(), "read_file_content", json.RawMessage(`{"file_path":"/not-readable"}`))
		require.NoError(t, err)
		require.False(t, handled)
		require.False(t, failed)
		require.Empty(t, out)
		restricted := req
		restricted.Ctx = llmcontracts.WithRuntimeTools(context.Background(), &llmcontracts.RuntimeTools{SkipDefaultTools: true})
		require.Nil(t, svc.fileRuntime(restricted), "request-scoped restrictions must also suppress file tools")
		require.Equal(t, tc.publish, rt.HasDefinition("publish_file"))
		if !tc.publish {
			_, handled, failed, err := rt.Executor(context.Background(), "publish_file", json.RawMessage(`{"file_path":"/not-readable"}`))
			require.True(t, handled)
			require.True(t, failed)
			require.ErrorContains(t, err, "not permitted")
		}
	}
}

func TestViewImageReadsBytesWithoutPublishing(t *testing.T) {
	ctx := attachment.WithToolImages(context.Background())
	work := t.TempDir()
	var pngBytes bytes.Buffer
	require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 4, 4))))
	require.NoError(t, os.WriteFile(filepath.Join(work, "page.png"), pngBytes.Bytes(), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(work, "fake.png"), []byte("not an image"), 0600))
	svc := &LLMService{attachmentRepo: &fileRuntimeAttachmentStore{}}
	req := llmcontracts.AgentRequest{Ctx: ctx, Agent: models.LLMConfig{Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey}, ExecID: "exec", ProjectID: "project", WorkDir: work, AgentDefinition: &models.Agent{Tools: []string{"Read"}}}
	rt := svc.fileRuntime(req)
	require.True(t, rt.HasDefinition("view_image"))
	require.False(t, rt.HasDefinition("publish_file"))
	out, handled, failed, err := rt.Executor(ctx, "view_image", json.RawMessage(`{"file_path":"page.png"}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, failed)
	result, ok := attachment.TakeToolImage(ctx, out)
	require.True(t, ok)
	require.Equal(t, pngBytes.Bytes(), result.Data)
	require.Equal(t, "image/png", result.MediaType)
	_, ok = attachment.TakeToolImage(ctx, out)
	require.False(t, ok)
	_, _, failed, err = rt.Executor(ctx, "view_image", json.RawMessage(`{"file_path":"fake.png"}`))
	require.True(t, failed)
	require.ErrorContains(t, err, "requires a PNG")
}
