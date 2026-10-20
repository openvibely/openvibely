package handler

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

// Includes JSON encoding/decoding, loopback socket IO, queueing and dispatch.
// Persistence setup is excluded, as live hub delivery precedes durable flushes.
func BenchmarkLiveWebSocketDelivery(b *testing.B) {
	stores := teststores.New(b)
	env := wireTestHandler(b, stores.Stores, testHandlerOptions{})
	hub := events.NewExecutionStreamHub()
	env.Handler.SetExecutionStreamHub(hub)
	ctx := context.Background()
	require.NoError(b, stores.Fixtures.SeedProjects(ctx, []models.Project{{ID: "bench", Name: "bench"}}))
	require.NoError(b, stores.Fixtures.SeedPendingTasks(ctx, []storefixture.PendingTask{{ID: "task", ProjectID: "bench", Title: "task", Prompt: "benchmark", Category: models.CategoryActive}}))
	require.NoError(b, stores.Fixtures.SeedExecutionHistory(ctx, []storefixture.ExecutionHistoryEntry{{ID: "exec", TaskID: "task", Status: models.ExecRunning, StartedAt: time.Now().UTC()}}))
	server := httptest.NewServer(env.Echo)
	defer server.Close()
	client := dialLive(b, server.URL)
	defer client.conn.CloseNow()
	client.send(b, liveCommand{Op: "execution", ID: "output", ProjectID: "bench", ExecID: "exec"})
	// A live subscription acknowledgement is an ordering barrier for setup.
	client.send(b, liveCommand{Op: "live", ID: "ready", ProjectID: "bench"})
	client.next(b, "ready", "subscribed")
	deadline := time.Now().Add(time.Second)
	for hub.SubscriberCount() != 1 {
		if time.Now().After(deadline) {
			b.Fatal("subscription timeout")
		}
		time.Sleep(time.Millisecond)
	}
	payload := strings.Repeat("x", 1024)
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		hub.Publish(events.ExecutionStreamEvent{ExecID: "exec", Type: events.ExecutionStreamDelta, Delta: payload, Offset: (i + 1) * len(payload)})
		client.next(b, "output", "message")
	}
	b.StopTimer()
	b.ReportMetric(1, "connections")
	b.ReportMetric(0, "SSE_connections")
}
