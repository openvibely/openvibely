package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

func TestComputerRecordingPersistsAndQueuesOnce(t *testing.T) {
	old := uploadsDir
	uploadsDir = t.TempDir()
	defer func() { uploadsDir = old }()
	db := teststores.New(t)
	config := &models.LLMConfig{Name: "Teach", Provider: models.ProviderTest, Model: "test", AuthMethod: models.AuthMethodAPIKey}
	require.NoError(t, db.LLMConfigs.Create(t.Context(), config))
	p := &models.Project{Name: "Teach", DefaultAgentConfigID: &config.ID}
	require.NoError(t, db.Projects.Create(t.Context(), p))
	task := &models.Task{ProjectID: p.ID, Title: "Teach", Prompt: "Test", Category: models.CategoryActive, Status: models.StatusRunning, AgentID: &config.ID}
	require.NoError(t, db.Tasks.Create(t.Context(), task))
	active := &models.Execution{TaskID: task.ID, AgentConfigID: config.ID, Status: models.ExecRunning}
	require.NoError(t, db.Executions.Create(t.Context(), active))
	svc := service.NewProjectComputerService(db.Projects, liveComputerFixture{service.ComputerConnection{Endpoint: "http://fixture.invalid"}})
	scoped, err := svc.ForTask(t.Context(), p.ID, task.ID)
	require.NoError(t, err)
	require.NoError(t, scoped.SetEnabled(t.Context(), p.ID, true))
	require.NoError(t, scoped.SetTaskEnabled(t.Context(), p.ID, true))
	h := &Handler{projectRepo: db.Projects, computerSvc: svc, taskRepo: db.Tasks, execRepo: db.Executions, threadInputRepo: db.ThreadInputs, llmConfigRepo: db.LLMConfigs}
	e := echo.New()
	e.GET("/projects/:id/computer/recording", h.ProjectComputerRecording)
	e.POST("/projects/:id/computer/recording", h.ProjectComputerRecording)
	send := func(method string, body any, origin string) (*httptest.ResponseRecorder, computerTeachRecording) {
		raw, _ := json.Marshal(body)
		req := httptest.NewRequest(method, "/projects/"+p.ID+"/computer/recording?task_id="+task.ID, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		res := httptest.NewRecorder()
		e.ServeHTTP(res, req)
		var out struct{ Recording computerTeachRecording }
		_ = json.Unmarshal(res.Body.Bytes(), &out)
		return res, out.Recording
	}
	res, _ := send("POST", map[string]any{"action": "start"}, "https://evil.invalid")
	require.Equal(t, 403, res.Code)
	res, r := send("POST", map[string]any{"action": "start"}, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	require.Equal(t, "recording", r.State)
	require.WithinDuration(t, r.StartedAt.Add(10*time.Minute), r.Deadline, time.Millisecond)
	var img bytes.Buffer
	require.NoError(t, jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 8, 8)), nil))
	appendBody := map[string]any{"action": "append", "id": r.ID, "requestID": "batch-one", "revision": 0, "steps": []string{"Open reports"}, "images": []string{base64.StdEncoding.EncodeToString(img.Bytes())}}
	res, r = send("POST", appendBody, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	require.Equal(t, 1, r.Revision)
	res, r = send("POST", appendBody, "")
	require.Equal(t, 200, res.Code)
	require.Equal(t, 1, r.Revision)
	res, _ = send("POST", map[string]any{"action": "append", "id": r.ID, "requestID": "stale", "revision": 0, "steps": []string{"stale"}}, "")
	require.Equal(t, 409, res.Code)
	// A new handler reads the same server-side recording after reconnect/restart.
	fresh := *h
	e.GET("/projects/:id/computer/recording", fresh.ProjectComputerRecording)
	res, recovered := send("GET", nil, "")
	require.Equal(t, 200, res.Code)
	require.Equal(t, r.ID, recovered.ID)
	require.Equal(t, 1, recovered.Revision)
	// Invalid image batches must not advance or corrupt the stored recording.
	res, _ = send("POST", map[string]any{"action": "append", "id": r.ID, "requestID": "bad-image", "revision": 1, "images": []string{"not-a-jpeg"}}, "")
	require.Equal(t, 400, res.Code)
	// Recording totals may exceed both former count limits and 28 MiB.
	// Upload bounded batches, then verify every capture survives saving.
	largeJPEG := append(append([]byte(nil), img.Bytes()...), make([]byte, 700<<10)...)
	for batch := 0; batch < 4; batch++ {
		steps := make([]string, 30)
		images := make([]string, 9)
		for i := range steps {
			steps[i] = "Demonstrated action"
		}
		for i := range images {
			images[i] = base64.StdEncoding.EncodeToString(largeJPEG)
		}
		res, r = send("POST", map[string]any{"action": "append", "id": r.ID, "requestID": fmt.Sprintf("large-%d", batch), "revision": r.Revision, "steps": steps, "images": images}, "")
		require.Equal(t, 200, res.Code, res.Body.String())
		require.Equal(t, "recording", r.State)
	}
	// Cross frame 99 with distinguishable captures, then inspect the same
	// alphabetical directory order used by attachment publication.
	moreImages := make([]string, 70)
	for i := range moreImages {
		moreImages[i] = base64.StdEncoding.EncodeToString(append(append([]byte(nil), img.Bytes()...), byte(i)))
	}
	res, r = send("POST", map[string]any{"action": "append", "id": r.ID, "requestID": "past-frame-99", "revision": r.Revision, "images": moreImages}, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	res, _ = send("POST", map[string]any{"action": "save", "id": r.ID}, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	res, _ = send("POST", map[string]any{"action": "save", "id": r.ID}, "")
	require.Equal(t, 200, res.Code, res.Body.String())
	queued, err := db.ThreadInputs.GetByID(t.Context(), "teach-"+r.ID)
	require.NoError(t, err)
	require.NotNil(t, queued)
	require.Equal(t, task.ID, queued.TaskID)
	require.Equal(t, active.ID, queued.RunExecutionID)
	require.Contains(t, queued.Content, "Learn the task")
	require.Equal(t, r.ID, queued.AttachmentSessionID)
	files, err := os.ReadDir(filepath.Join(uploadsDir, "chat", "pending", r.ID))
	require.NoError(t, err)
	require.Len(t, files, 108)
	require.Equal(t, "demonstration-000099.jpg", files[98].Name())
	require.Equal(t, "demonstration-000100.jpg", files[99].Name())
	require.Equal(t, "demonstration-000101.jpg", files[100].Name())
	for i, encoded := range moreImages {
		capture, err := os.ReadFile(filepath.Join(uploadsDir, "chat", "pending", r.ID, files[37+i].Name()))
		require.NoError(t, err)
		expected, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err)
		require.Equal(t, expected, capture, "capture order at frame %d", 38+i)
	}
	manifest, err := os.ReadFile(filepath.Join(uploadsDir, "chat", "pending", r.ID, "demonstration.json"))
	require.NoError(t, err)
	var demo struct{ Steps []string }
	require.NoError(t, json.Unmarshal(manifest, &demo))
	require.Len(t, demo.Steps, 121)
	for i := 2; i <= 37; i++ {
		capture, err := os.ReadFile(filepath.Join(uploadsDir, "chat", "pending", r.ID, fmt.Sprintf("demonstration-%06d.jpg", i)))
		require.NoError(t, err)
		require.Equal(t, largeJPEG, capture)
	}
	res, _ = send("POST", map[string]any{"action": "discard", "id": r.ID}, "")
	require.Equal(t, 409, res.Code)
	res, r = send("POST", map[string]any{"action": "start"}, "")
	require.Equal(t, 200, res.Code)
	res, _ = send("POST", map[string]any{"action": "discard", "id": r.ID}, "")
	require.Equal(t, 200, res.Code)
	absent, err := db.ThreadInputs.GetByID(t.Context(), "teach-"+r.ID)
	require.NoError(t, err)
	require.Nil(t, absent)
	// The server, not a browser timer, enforces the recording duration.
	_, r = send("POST", map[string]any{"action": "start"}, "")
	sum := sha256.Sum256([]byte(p.ID + "/" + task.ID))
	path := filepath.Join(uploadsDir, "computer-teaching", hex.EncodeToString(sum[:])+".json")
	r.Deadline = time.Now().Add(-time.Second)
	require.NoError(t, writeTeachRecording(path, &r))
	res, r = send("GET", nil, "")
	require.Equal(t, 200, res.Code)
	require.Equal(t, "stopped", r.State)
	res, _ = send("POST", map[string]any{"action": "start"}, "")
	require.Equal(t, 409, res.Code)
}

// The recording-to-chat flow replaces the retired standalone workflow API.
func TestComputerRecordingReplacesLegacyWorkflowRoutes(t *testing.T) {
	tc := NewStoreTestContext(t)
	recordingRoutes := map[string]bool{}
	for _, route := range tc.echo.Routes() {
		require.NotEqual(t, "/projects/:id/computer/teach", route.Path)
		require.NotEqual(t, "/projects/:id/computer/workflows", route.Path)
		if route.Path == "/projects/:id/computer/recording" {
			recordingRoutes[route.Method] = true
		}
	}
	require.True(t, recordingRoutes["GET"])
	require.True(t, recordingRoutes["POST"])
}
