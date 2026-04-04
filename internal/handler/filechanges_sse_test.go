package handler

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/openvibely/openvibely/internal/testutil"
)

func TestFileChangesSSE_ReceivesEvents(t *testing.T) {
	db := testutil.NewTestDB(t)
	defer db.Close()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, events.NewBroadcaster())
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := service.NewWorkerService(llmSvc, 0, projectRepo)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	broadcaster := events.NewBroadcaster()
	fileChangeBroadcaster := events.NewFileChangeBroadcaster()

	h := New(
		projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, broadcaster, nil,
	)
	h.SetFileChangeBroadcaster(fileChangeBroadcaster)

	e := echo.New()
	e.GET("/events/filechanges", h.FileChangesSSE)

	// Use a test server for real HTTP connections (SSE requires streaming)
	srv := httptest.NewServer(e)
	defer srv.Close()

	// Connect to SSE endpoint
	req, _ := http.NewRequest("GET", srv.URL+"/events/filechanges?task_id=test-task", nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to connect to SSE: %v", err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("expected Content-Type 'text/event-stream', got %q", ct)
	}

	// Publish a file change event
	go func() {
		time.Sleep(100 * time.Millisecond)
		fileChangeBroadcaster.Publish(events.FileChangeEvent{
			Type:       events.FileModified,
			TaskID:     "test-task",
			ExecID:     "test-exec",
			FilePath:   "main.go",
			ToolName:   "write_file",
			Timestamp:  time.Now().UnixMilli(),
		})
	}()

	// Read the SSE event
	scanner := bufio.NewScanner(resp.Body)
	timeout := time.After(3 * time.Second)
	eventReceived := false

	for scanner.Scan() {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for SSE event")
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "event: file_modified") {
			// Next line should be the data
			if scanner.Scan() {
				data := scanner.Text()
				if !strings.Contains(data, "test-task") {
					t.Errorf("expected event data to contain task ID, got %q", data)
				}
				if !strings.Contains(data, "main.go") {
					t.Errorf("expected event data to contain file path, got %q", data)
				}
				eventReceived = true
				break
			}
		}
	}

	if !eventReceived {
		t.Error("did not receive file change event")
	}
}

func TestFileChangesSSE_FiltersTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	defer db.Close()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, events.NewBroadcaster())
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := service.NewWorkerService(llmSvc, 0, projectRepo)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	broadcaster := events.NewBroadcaster()
	fileChangeBroadcaster := events.NewFileChangeBroadcaster()

	h := New(
		projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, broadcaster, nil,
	)
	h.SetFileChangeBroadcaster(fileChangeBroadcaster)

	e := echo.New()
	e.GET("/events/filechanges", h.FileChangesSSE)

	srv := httptest.NewServer(e)
	defer srv.Close()

	req, _ := http.NewRequest("GET", srv.URL+"/events/filechanges?task_id=task-1", nil)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("failed to connect to SSE: %v", err)
	}
	defer resp.Body.Close()

	// Publish events for different tasks
	go func() {
		time.Sleep(100 * time.Millisecond)
		// This event should NOT be received (wrong task)
		fileChangeBroadcaster.Publish(events.FileChangeEvent{
			Type:     events.FileModified,
			TaskID:   "task-2",
			FilePath: "other.go",
		})
		time.Sleep(50 * time.Millisecond)
		// This event SHOULD be received (correct task)
		fileChangeBroadcaster.Publish(events.FileChangeEvent{
			Type:     events.FileModified,
			TaskID:   "task-1",
			FilePath: "main.go",
		})
	}()

	scanner := bufio.NewScanner(resp.Body)
	timeout := time.After(3 * time.Second)
	receivedCorrectEvent := false

	for scanner.Scan() {
		select {
		case <-timeout:
			if !receivedCorrectEvent {
				t.Fatal("timeout waiting for correct event")
			}
			return
		default:
		}

		line := scanner.Text()
		if strings.HasPrefix(line, "event: file_modified") {
			if scanner.Scan() {
				data := scanner.Text()
				if strings.Contains(data, "other.go") {
					t.Error("received event for wrong task")
				}
				if strings.Contains(data, "main.go") && strings.Contains(data, "task-1") {
					receivedCorrectEvent = true
					return
				}
			}
		}
	}
}

func TestFileChangesSSE_RequiresTaskID(t *testing.T) {
	db := testutil.NewTestDB(t)
	defer db.Close()

	projectRepo := repository.NewProjectRepo(db)
	taskRepo := repository.NewTaskRepo(db, events.NewBroadcaster())
	llmConfigRepo := repository.NewLLMConfigRepo(db)
	execRepo := repository.NewExecutionRepo(db)
	scheduleRepo := repository.NewScheduleRepo(db)
	workerRepo := repository.NewWorkerRepo(db)
	attachmentRepo := repository.NewAttachmentRepo(db)
	chatAttachmentRepo := repository.NewChatAttachmentRepo(db)
	settingsRepo := repository.NewSettingsRepo(db)

	projectSvc := service.NewProjectService(projectRepo)
	llmSvc := service.NewLLMService(llmConfigRepo, execRepo, taskRepo, projectRepo, scheduleRepo, attachmentRepo)
	workerSvc := service.NewWorkerService(llmSvc, 0, projectRepo)
	taskSvc := service.NewTaskService(taskRepo, attachmentRepo, workerSvc)
	schedulerSvc := service.NewSchedulerService(scheduleRepo, taskRepo, workerSvc)
	broadcaster := events.NewBroadcaster()

	h := New(
		projectSvc, taskSvc, llmSvc, workerSvc, schedulerSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		llmConfigRepo, taskRepo, scheduleRepo, execRepo, workerRepo, attachmentRepo, chatAttachmentRepo, projectRepo, settingsRepo, broadcaster, nil,
	)
	h.SetFileChangeBroadcaster(events.NewFileChangeBroadcaster())

	e := echo.New()
	e.GET("/events/filechanges", h.FileChangesSSE)

	req := httptest.NewRequest(http.MethodGet, "/events/filechanges", nil)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	_ = h.FileChangesSSE(c)
	
	// The handler uses c.String() which writes to the response and returns nil
	// Check the response code and body instead
	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rec.Code)
	}
	
	if !strings.Contains(rec.Body.String(), "task_id required") {
		t.Errorf("expected error message about task_id, got %q", rec.Body.String())
	}
}
