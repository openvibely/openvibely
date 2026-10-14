package service

import (
	"context"

	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"

	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
)

func BenchmarkExecuteListTasksTool(b *testing.B) {
	fixtures := []struct {
		name       string
		taskCount  int
		promptSize int
		configSize int
		input      json.RawMessage
	}{
		{name: "Default20_LargePayload", taskCount: 20, promptSize: 64 * 1024, configSize: 16 * 1024},
		{name: "Maximum50_LargePayload", taskCount: 50, promptSize: 64 * 1024, configSize: 16 * 1024, input: json.RawMessage(`{"limit":50}`)},
		{name: "Small20_Control", taskCount: 20, promptSize: 128, configSize: 128},
	}

	for _, fixture := range fixtures {
		b.Run(fixture.name, func(b *testing.B) {
			db, taskRepo := newTaskDiscoveryBenchmarkRepo(b, fixture.taskCount, fixture.promptSize, fixture.configSize)
			defer db.Close()

			ctx := context.Background()
			var outputBytes int
			b.ReportAllocs()
			b.ResetTimer()
			b.StartTimer()
			for i := 0; i < b.N; i++ {
				out, err := ExecuteListTasksTool(ctx, taskRepo, "default", fixture.input)
				if err != nil {
					b.Fatalf("ExecuteListTasksTool: %v", err)
				}
				outputBytes = len(out)
			}
			b.StopTimer()
			b.ReportMetric(float64(outputBytes), "output_bytes/op")
			b.ReportMetric(float64(fixture.taskCount*(fixture.promptSize+2*fixture.configSize)), "fixture_payload_bytes/op")
		})
	}
}

func newTaskDiscoveryBenchmarkRepo(b *testing.B, taskCount, promptSize, configSize int) (*teststores.Env, persistence.TaskStore) {
	b.Helper()
	b.StopTimer()
	db := teststores.New(b)
	taskRepo := db.Tasks
	ctx := context.Background()
	prompt := strings.Repeat("p", promptSize)
	chainConfig := `{"payload":"` + strings.Repeat("c", configSize) + `"}`
	swarmConfig := `{"payload":"` + strings.Repeat("s", configSize) + `"}`
	for i := 0; i < taskCount; i++ {
		task := &models.Task{
			ProjectID:   "default",
			Title:       fmt.Sprintf("Discovery benchmark task %03d", i),
			Category:    models.CategoryActive,
			Priority:    (i % 4) + 1,
			Status:      models.StatusPending,
			Prompt:      prompt,
			ChainConfig: chainConfig,
			SwarmConfig: swarmConfig,
		}
		if err := taskRepo.Create(ctx, task); err != nil {
			db.Close()
			b.Fatalf("create benchmark task %d: %v", i, err)
		}
	}
	return db, taskRepo
}

type taskDiscoveryOrderPage struct {
	name  string
	input json.RawMessage
}

// BenchmarkExecuteListTasksToolOrderScaling exercises the production discovery
// service against realistic project sizes. The task payloads stay deliberately
// small so this benchmark isolates page ordering rather than the full-row
// projection work covered by the compact-projection benchmark above.
func BenchmarkExecuteListTasksToolOrderScaling(b *testing.B) {
	for _, taskCount := range []int{100, 1000, 5000, 10000} {
		for _, page := range []taskDiscoveryOrderPage{
			{name: "Default20_SmallPayload", input: nil},
			{name: "Maximum50_SmallPayload", input: json.RawMessage(`{"limit":50}`)},
		} {
			taskCount := taskCount
			page := page
			b.Run(fmt.Sprintf("%d_tasks/%s", taskCount, page.name), func(b *testing.B) {
				db, taskRepo := newTaskDiscoveryOrderBenchmarkRepo(b, taskCount, 128, 128)
				defer db.Close()
				b.Run("Current", func(b *testing.B) {
					ctx := context.Background()
					var outputBytes int

					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						out, err := ExecuteListTasksTool(ctx, taskRepo, "default", page.input)
						if err != nil {
							b.Fatalf("ExecuteListTasksTool: %v", err)
						}
						outputBytes = len(out)
					}
					b.StopTimer()
					b.ReportMetric(float64(outputBytes), "output_bytes/op")
					b.ReportMetric(float64(taskCount*(128+2*128)), "fixture_payload_bytes/op")
				})
			})
		}
	}
}

func newTaskDiscoveryOrderBenchmarkRepo(tb testing.TB, taskCount, promptSize, configSize int) (*teststores.Env, persistence.TaskStore) {
	tb.Helper()
	env := teststores.New(tb)
	if err := env.Fixtures.SeedTaskDiscoveryWorkload(context.Background(), storefixture.TaskDiscoveryWorkload{
		ProjectID: "default", Count: taskCount, PromptBytes: promptSize, ConfigurationBytes: configSize,
	}); err != nil {
		tb.Fatalf("seed discovery workload: %v", err)
	}
	return env, env.Tasks
}
