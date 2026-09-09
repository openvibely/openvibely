package handler

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/events"
)

type liveSSETransportClient struct {
	response *http.Response
	events   <-chan string
	done     <-chan struct{}
}

func openLiveSSETransportClient(t testing.TB, serverURL, requestPath string) *liveSSETransportClient {
	t.Helper()

	response, err := http.Get(serverURL + requestPath)
	if err != nil {
		t.Fatalf("GET %s: %v", requestPath, err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("GET %s status = %d, want %d", requestPath, response.StatusCode, http.StatusOK)
	}

	scanner := bufio.NewScanner(response.Body)
	if !scanner.Scan() || scanner.Text() != ": ping" {
		response.Body.Close()
		t.Fatalf("GET %s did not receive initial SSE ping", requestPath)
	}
	if !scanner.Scan() || scanner.Text() != "" {
		response.Body.Close()
		t.Fatalf("GET %s did not receive SSE ping separator", requestPath)
	}

	eventsByType := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(eventsByType)
		defer close(done)

		currentType := ""
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "event: "):
				currentType = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: ") && currentType != "":
				eventsByType <- currentType
			case line == "":
				currentType = ""
			}
		}
	}()

	return &liveSSETransportClient{
		response: response,
		events:   eventsByType,
		done:     done,
	}
}

func closeLiveSSETransportClient(t testing.TB, client *liveSSETransportClient) {
	t.Helper()
	if err := client.response.Body.Close(); err != nil {
		t.Errorf("close SSE response: %v", err)
	}
	select {
	case <-client.done:
	case <-time.After(3 * time.Second):
		t.Error("timed out waiting for SSE client reader to stop")
	}
}

func waitForLiveSSEEvent(t testing.TB, client *liveSSETransportClient, want string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case got, ok := <-client.events:
			if !ok {
				t.Fatalf("SSE stream ended before receiving %q", want)
			}
			if got == want {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for SSE event %q", want)
		}
	}
}

func TestLiveEventsSSE_ProjectScopedSubscriptionSkipsUnscopedFileStream(t *testing.T) {
	taskBroadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileBroadcaster := events.NewFileChangeBroadcaster()
	h := &Handler{
		broadcaster:           taskBroadcaster,
		chatBroadcaster:       chatBroadcaster,
		fileChangeBroadcaster: fileBroadcaster,
	}

	e := echo.New()
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/events/live?project_id=project-1", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan error, 1)
	go func() {
		done <- h.LiveEventsSSE(e.NewContext(req, rec))
	}()

	waitForLiveSubscriberCount(t, "task", taskBroadcaster.SubscriberCount, 1)
	waitForLiveSubscriberCount(t, "chat", chatBroadcaster.SubscriberCount, 1)
	if got := fileBroadcaster.SubscriberCount(); got != 0 {
		t.Fatalf("project-scoped file subscribers = %d, want 0 without task_id", got)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("LiveEventsSSE returned an error after request cancellation: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for project-scoped LiveEventsSSE to return")
	}
	waitForLiveSubscriberCount(t, "task", taskBroadcaster.SubscriberCount, 0)
	waitForLiveSubscriberCount(t, "chat", chatBroadcaster.SubscriberCount, 0)
}

func TestLiveEventsSSE_ScopedTransportLimits50ClientFanout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping timing-sensitive SSE transport test in short mode")
	}

	taskBroadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileBroadcaster := events.NewFileChangeBroadcaster()
	var writes atomic.Int64
	var responseBytes atomic.Int64
	h := &Handler{
		broadcaster:           taskBroadcaster,
		chatBroadcaster:       chatBroadcaster,
		fileChangeBroadcaster: fileBroadcaster,
		liveSSEWriteObserver: func(written int) {
			writes.Add(1)
			responseBytes.Add(int64(written))
		},
	}

	e := echo.New()
	e.GET("/events/live", h.LiveEventsSSE)
	server := httptest.NewServer(e)
	t.Cleanup(server.Close)

	const (
		clientCount  = 50
		projectCount = 10
		matching     = clientCount / projectCount
	)
	clients := make([]*liveSSETransportClient, 0, clientCount)
	matchingClients := make([]*liveSSETransportClient, 0, matching)
	for i := 0; i < clientCount; i++ {
		scope := i % projectCount
		client := openLiveSSETransportClient(t, server.URL, fmt.Sprintf("/events/live?project_id=project-%d&task_id=task-%d", scope, scope))
		clients = append(clients, client)
		if scope == 0 {
			matchingClients = append(matchingClients, client)
		}
	}
	t.Cleanup(func() {
		for _, client := range clients {
			closeLiveSSETransportClient(t, client)
		}
	})

	waitForLiveSubscriberCount(t, "task", taskBroadcaster.SubscriberCount, clientCount)
	waitForLiveSubscriberCount(t, "chat", chatBroadcaster.SubscriberCount, clientCount)
	waitForLiveSubscriberCount(t, "file-change", fileBroadcaster.SubscriberCount, clientCount)

	taskAttempts := taskBroadcaster.DeliveryAttempts()
	taskBroadcaster.Publish(events.TaskEvent{
		Type:      events.TaskStatusChanged,
		TaskID:    "task-0",
		ProjectID: "project-0",
		Status:    "completed",
	})
	for _, client := range matchingClients {
		waitForLiveSSEEvent(t, client, string(events.TaskStatusChanged))
	}
	if got := taskBroadcaster.DeliveryAttempts() - taskAttempts; got != matching {
		t.Fatalf("task delivery attempts = %d, want %d matching clients", got, matching)
	}

	chatAttempts := chatBroadcaster.DeliveryAttempts()
	chatBroadcaster.Publish(events.ChatEvent{
		Type:      events.ChatResponseDone,
		ProjectID: "project-0",
		TaskID:    "task-0",
		ExecID:    "exec-0",
	})
	for _, client := range matchingClients {
		waitForLiveSSEEvent(t, client, string(events.ChatResponseDone))
	}
	if got := chatBroadcaster.DeliveryAttempts() - chatAttempts; got != matching {
		t.Fatalf("chat delivery attempts = %d, want %d matching clients", got, matching)
	}

	fileAttempts := fileBroadcaster.DeliveryAttempts()
	fileBroadcaster.Publish(events.FileChangeEvent{
		Type:      events.DiffSnapshot,
		TaskID:    "task-0",
		ExecID:    "exec-0",
		Timestamp: time.Now().UnixMilli(),
	})
	for _, client := range matchingClients {
		waitForLiveSSEEvent(t, client, string(events.DiffSnapshot))
	}
	if got := fileBroadcaster.DeliveryAttempts() - fileAttempts; got != matching {
		t.Fatalf("file-change delivery attempts = %d, want %d matching clients", got, matching)
	}

	const publishedEvents = 3
	if got, want := writes.Load(), int64(matching*publishedEvents); got != want {
		t.Fatalf("handler serialization/write count = %d, want %d matching writes", got, want)
	}
	candidateBytes := responseBytes.Load()
	if candidateBytes <= 0 {
		t.Fatal("expected matching SSE response bytes to be recorded")
	}
	if candidateBytes%matching != 0 {
		t.Fatalf("matching SSE response bytes = %d, want an even %d-client total", candidateBytes, matching)
	}

	baselineWrites := int64(clientCount * publishedEvents)
	baselineBytes := candidateBytes / matching * clientCount
	if got := (baselineWrites - writes.Load()) * 100 / baselineWrites; got < 80 {
		t.Fatalf("serialization/write reduction = %d%%, want at least 80%%", got)
	}
	if got := (baselineBytes - candidateBytes) * 100 / baselineBytes; got < 80 {
		t.Fatalf("SSE response-byte reduction = %d%%, want at least 80%%", got)
	}
}

type liveSSEBenchmarkPublisher struct {
	name             string
	publish          func()
	deliveryAttempts func() uint64
}

func BenchmarkLiveEventsSSETransport(b *testing.B) {
	for _, clientCount := range []int{1, 10, 50} {
		for _, scoped := range []bool{false, true} {
			for _, publisherKind := range []string{"task", "chat", "file"} {
				name := fmt.Sprintf("%s/%s/%d_clients", publisherKind, map[bool]string{false: "global", true: "scoped"}[scoped], clientCount)
				b.Run(name, func(b *testing.B) {
					benchmarkLiveEventsSSETransport(b, clientCount, scoped, publisherKind)
				})
			}
		}
	}
}

func benchmarkLiveEventsSSETransport(b *testing.B, clientCount int, scoped bool, publisherKind string) {
	taskBroadcaster := events.NewBroadcaster()
	chatBroadcaster := events.NewChatBroadcaster()
	fileBroadcaster := events.NewFileChangeBroadcaster()
	writeTimes := make(chan time.Time, clientCount)
	var writes atomic.Int64
	var responseBytes atomic.Int64
	h := &Handler{
		broadcaster:           taskBroadcaster,
		chatBroadcaster:       chatBroadcaster,
		fileChangeBroadcaster: fileBroadcaster,
		liveSSEWriteObserver: func(written int) {
			writes.Add(1)
			responseBytes.Add(int64(written))
			writeTimes <- time.Now()
		},
	}

	e := echo.New()
	e.GET("/events/live", h.LiveEventsSSE)
	server := httptest.NewServer(e)
	defer server.Close()

	clients := make([]*liveSSETransportClient, 0, clientCount)
	matching := 0
	for i := 0; i < clientCount; i++ {
		path := "/events/live"
		if scoped {
			scope := i % 10
			path = fmt.Sprintf("/events/live?project_id=project-%d&task_id=task-%d", scope, scope)
			if scope == 0 {
				matching++
			}
		} else {
			matching++
		}
		clients = append(clients, openLiveSSETransportClient(b, server.URL, path))
	}
	defer func() {
		for _, client := range clients {
			closeLiveSSETransportClient(b, client)
		}
	}()

	waitForLiveSubscriberCount(b, "task", taskBroadcaster.SubscriberCount, clientCount)
	waitForLiveSubscriberCount(b, "chat", chatBroadcaster.SubscriberCount, clientCount)
	waitForLiveSubscriberCount(b, "file-change", fileBroadcaster.SubscriberCount, clientCount)

	publishers := map[string]liveSSEBenchmarkPublisher{
		"task": {
			name: "task",
			publish: func() {
				taskBroadcaster.Publish(events.TaskEvent{Type: events.TaskStatusChanged, TaskID: "task-0", ProjectID: "project-0", Status: "running"})
			},
			deliveryAttempts: taskBroadcaster.DeliveryAttempts,
		},
		"chat": {
			name: "chat",
			publish: func() {
				chatBroadcaster.Publish(events.ChatEvent{Type: events.ChatResponseDone, TaskID: "task-0", ProjectID: "project-0", ExecID: "exec-0"})
			},
			deliveryAttempts: chatBroadcaster.DeliveryAttempts,
		},
		"file": {
			name: "file",
			publish: func() {
				fileBroadcaster.Publish(events.FileChangeEvent{Type: events.DiffSnapshot, TaskID: "task-0", ExecID: "exec-0", Timestamp: 1})
			},
			deliveryAttempts: fileBroadcaster.DeliveryAttempts,
		},
	}
	publisher, ok := publishers[publisherKind]
	if !ok {
		b.Fatalf("unknown publisher kind %q", publisherKind)
	}

	latencies := make([]int64, 0, b.N)
	attemptsBefore := publisher.deliveryAttempts()
	writesBefore := writes.Load()
	bytesBefore := responseBytes.Load()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		started := time.Now()
		publisher.publish()
		lastWrite := started
		for received := 0; received < matching; received++ {
			writtenAt := <-writeTimes
			if writtenAt.After(lastWrite) {
				lastWrite = writtenAt
			}
		}
		latencies = append(latencies, lastWrite.Sub(started).Nanoseconds())
	}
	b.StopTimer()

	b.ReportMetric(float64(publisher.deliveryAttempts()-attemptsBefore)/float64(b.N), "delivery-attempts/op")
	b.ReportMetric(float64(writes.Load()-writesBefore)/float64(b.N), "handler-writes/op")
	b.ReportMetric(float64(responseBytes.Load()-bytesBefore)/float64(b.N), "sse-response-bytes/op")
	b.ReportMetric(float64(medianLiveSSELatency(latencies)), "publish-to-write-ns/op")
}

func medianLiveSSELatency(values []int64) int64 {
	if len(values) == 0 {
		return 0
	}
	ordered := append([]int64(nil), values...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	return ordered[len(ordered)/2]
}
