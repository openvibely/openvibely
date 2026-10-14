package handler

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

// BenchmarkProjectTabNavigationUnderLoad measures the Tasks main-content request
// made by project tabs, not the unrelated channel switch_project tool. Thirty
// simulated executions perform bounded 64 KiB output snapshots/reads at up to
// 20 Hz each. SSE clients consume real HTTP streams. This isolates server-side
// contention; it does not measure browser rendering or browser connection limits.
// Allocation counts include background goroutines; pool waits are aggregate
// across all operations during the measurement window, not per navigation.
func BenchmarkProjectTabNavigationUnderLoad(b *testing.B) {
	for _, mode := range []struct {
		name          string
		database, sse bool
	}{
		{"Idle", false, false}, {"Database", true, false}, {"SSE", false, true}, {"DatabaseAndSSE", true, true},
	} {
		b.Run(mode.name, func(b *testing.B) {

			broadcaster := events.NewBroadcaster()
			stores := teststores.New(b, teststores.WithProductionTopology(), teststores.WithBroadcaster(broadcaster))
			env := wireTestHandler(b, stores.Stores, testHandlerOptions{broadcaster: broadcaster})
			taskWriter := stores.Tasks
			execWriter := stores.Executions
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			for p := 0; p < 3; p++ {
				projectID := fmt.Sprintf("load-project-%d", p)
				if err := stores.Fixtures.SeedProjects(ctx, []models.Project{{ID: projectID, Name: projectID}}); err != nil {
					b.Fatal(err)
				}
				for j := 0; j < 10; j++ {
					id := fmt.Sprintf("load-task-%d-%d", p, j)
					if err := stores.Fixtures.SeedPendingTasks(ctx, []storefixture.PendingTask{{ID: id, ProjectID: projectID, Title: id, Prompt: "benchmark", Category: models.CategoryActive}}); err != nil {
						b.Fatal(err)
					}
					if claimed, err := taskWriter.ClaimTask(ctx, id); err != nil || !claimed {
						b.Fatalf("claim %s: claimed=%v err=%v", id, claimed, err)
					}
					if err := stores.Fixtures.SeedExecutionHistory(ctx, []storefixture.ExecutionHistoryEntry{{ID: id, TaskID: id, Status: models.ExecRunning, PromptSent: "benchmark", StartedAt: time.Now().UTC()}}); err != nil {
						b.Fatal(err)
					}
				}
			}
			server := httptest.NewServer(env.Echo)
			defer server.Close()
			client := &http.Client{Timeout: 5 * time.Second}
			defer client.CloseIdleConnections()
			var delivered atomic.Int64
			if mode.sse {
				clients := make([]*liveSSETransportClient, 0, 3)
				for p := 0; p < 3; p++ {
					stream := openLiveSSETransportClient(b, server.URL, fmt.Sprintf("/events/live?project_id=load-project-%d", p))
					clients = append(clients, stream)
					go func() {
						for range stream.events {
							delivered.Add(1)
						}
					}()
				}
				defer closeLiveSSETransportClients(b, clients)
				waitForLiveSubscriberCount(b, "task", broadcaster.SubscriberCount, 3)
			}
			var workers sync.WaitGroup
			var operations atomic.Int64
			errors := make(chan error, 30)
			ready := make(chan struct{}, 30)
			payload := strings.Repeat("stream output\n", 5042)
			if mode.database || mode.sse {
				for p := 0; p < 3; p++ {
					for j := 0; j < 10; j++ {
						projectID, id := fmt.Sprintf("load-project-%d", p), fmt.Sprintf("load-task-%d-%d", p, j)
						workers.Add(1)
						go func() {
							defer workers.Done()
							ticker := time.NewTicker(50 * time.Millisecond)
							defer ticker.Stop()
							first := true
							sequence := 0
							for {
								sequence++
								if mode.database {
									if err := execWriter.FinalizeOutput(ctx, id, fmt.Sprintf("%d\n%s", sequence, payload)); err != nil {
										if ctx.Err() == nil {
											errors <- err
										}
										return
									}
									if _, err := env.ExecRepo.GetByID(ctx, id); err != nil {
										if ctx.Err() == nil {
											errors <- err
										}
										return
									}
								}
								if mode.sse {
									broadcaster.Publish(events.TaskEvent{Type: events.TaskBoardUpdated, ProjectID: projectID, TaskID: id})
								}
								operations.Add(1)
								if first {
									ready <- struct{}{}
									first = false
								}
								select {
								case <-ctx.Done():
									return
								case <-ticker.C:
								}
							}
						}()
					}
				}
				defer func() { cancel(); workers.Wait() }()
				deadline := time.NewTimer(10 * time.Second)
				defer deadline.Stop()
				for n := 0; n < 30; n++ {
					select {
					case <-ready:
					case err := <-errors:
						b.Fatal(err)
					case <-deadline.C:
						b.Fatal("background load did not become ready")
					}
				}
			}
			latencies := make([]time.Duration, b.N)
			beforeReader, beforeWriter := stores.Contention().Reads, stores.Contention().Writes
			beforeOps, beforeDelivered := operations.Load(), delivered.Load()
			b.ReportAllocs()
			b.ResetTimer()
			started := time.Now()
			for i := 0; i < b.N; i++ {
				projectID := fmt.Sprintf("load-project-%d", i%3)
				req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/tasks?project_id="+projectID, nil)
				if err != nil {
					b.Fatal(err)
				}
				req.Header.Set("HX-Request", "true")
				req.Header.Set("HX-Target", "main-content")
				start := time.Now()
				response, err := client.Do(req)
				if err != nil {
					b.Fatalf("navigate %s: %v", projectID, err)
				}
				body, readErr := io.ReadAll(response.Body)
				response.Body.Close()
				latencies[i] = time.Since(start)
				if readErr != nil || response.StatusCode != http.StatusOK {
					b.Fatalf("navigate %s: status=%d read=%v body=%.200s", projectID, response.StatusCode, readErr, body)
				}
				bodyText := string(body)
				for j := 0; j < 10; j++ {
					if !strings.Contains(bodyText, fmt.Sprintf("load-task-%d-%d", i%3, j)) {
						b.Fatalf("navigation omitted running task %d for %s", j, projectID)
					}
				}
			}
			elapsed := time.Since(started)
			b.StopTimer()
			afterReader, afterWriter := stores.Contention().Reads, stores.Contention().Writes
			ops, received := operations.Load()-beforeOps, delivered.Load()-beforeDelivered
			cancel()
			workers.Wait()
			select {
			case err := <-errors:
				b.Fatalf("background activity: %v", err)
			default:
			}
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100].Microseconds())/1000, "p95_ms")
			b.ReportMetric(float64(latencies[len(latencies)-1].Microseconds())/1000, "max_ms")
			b.ReportMetric(float64(afterReader.WaitDuration-beforeReader.WaitDuration)/float64(time.Millisecond), "reader_wait_ms")
			b.ReportMetric(float64(afterWriter.WaitDuration-beforeWriter.WaitDuration)/float64(time.Millisecond), "writer_wait_ms")
			b.ReportMetric(float64(ops)/elapsed.Seconds(), "background_ops/s")
			b.ReportMetric(float64(received)/elapsed.Seconds(), "sse_events/s")
		})
	}
}
