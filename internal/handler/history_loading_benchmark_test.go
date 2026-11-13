package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/testutil/storefixture"
	"github.com/openvibely/openvibely/internal/testutil/teststores"
	"github.com/stretchr/testify/require"
)

type historyLoadingSample struct {
	Operation      string               `json:"operation"`
	DurationNS     int64                `json:"duration_ns"`
	AllocatedBytes uint64               `json:"allocated_bytes"`
	ResponseBytes  int                  `json:"response_bytes"`
	IO             storefixture.IOStats `json:"io"`
}

// Opt-in: uses production SQLite reader/writer routing and actual HTTP handlers.
// Setup and correctness validation are outside measured operations.
func TestHistoryLoadingHarness(t *testing.T) {
	result := os.Getenv("OV_HISTORY_RESULT")
	if result == "" {
		t.Skip("opt-in history harness")
	}
	n, err := strconv.Atoi(os.Getenv("OV_HISTORY_BLOCKS"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, n, 128)
	mode := os.Getenv("OV_HISTORY_MODE")
	require.Contains(t, []string{"completed", "running", "plan"}, mode)
	h, app, configs, stores := setupTestHandlerWithStores(t, teststores.WithProductionTopology(), teststores.WithIOCounting())
	agent := createAgent(t, configs)
	project := createProject(t, h, "Synthetic history benchmark")
	task := createTask(t, h, project.ID, "Synthetic thread")
	execution := createExec(t, h, task.ID, agent.ID)
	var want strings.Builder
	for start := 0; start < n; start += 128 {
		var changes []models.ConversationChange
		for i := start; i < min(n, start+128); i++ {
			text := fmt.Sprintf("block-%08d\n", i) + strings.Repeat("synthetic text \"quoted\" /path\n", 140)
			if mode == "plan" && i == 0 {
				text = "<proposed_plan>synthetic plan</proposed_plan>\n" + text
			}
			want.WriteString(text)
			raw, err := json.Marshal(map[string]string{"type": "text", "text": text})
			require.NoError(t, err)
			changes = append(changes, models.ConversationChange{ItemID: fmt.Sprint(i), Position: int64(i), Lane: models.ConversationDisplay, Operation: models.ConversationPut, Payload: raw, MutationID: fmt.Sprintf("fixture-%d", i)})
		}
		_, err = h.execRepo.AppendConversationChanges(t.Context(), models.ConversationOwner{ExecutionID: execution.ID}, changes)
		require.NoError(t, err)
	}
	if mode != "running" {
		require.NoError(t, h.execRepo.Complete(t.Context(), execution.ID, models.ExecCompleted, "", "", 0, 0))
	}
	expected := want.String()
	var samples []historyLoadingSample
	measure := func(op, path string) []byte {
		runtime.GC()
		stores.IO.Reset()
		stores.IO.SetEnabled(true)
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		response := htmxGet(app, path)
		elapsed := time.Since(start)
		runtime.ReadMemStats(&after)
		stats := stores.IO.Snapshot()
		stores.IO.SetEnabled(false)
		require.Equal(t, http.StatusOK, response.Code)
		samples = append(samples, historyLoadingSample{op, elapsed.Nanoseconds(), after.TotalAlloc - before.TotalAlloc, response.Body.Len(), stats})
		return response.Body.Bytes()
	}
	// Initial route includes server template rendering, but no browser execution.
	initial := measure("open_thread", "/tasks/"+task.ID+"?project_id="+project.ID)
	require.Contains(t, string(initial), fmt.Sprintf("block-%08d", n-1))
	path := func(cursor int64, revision string) string {
		return fmt.Sprintf("/chat/output/%s?before=%d&revision=%s&project_id=%s", execution.ID, cursor, revision, project.ID)
	}
	var tail models.DisplayTail
	require.NoError(t, json.Unmarshal(measure("latest_page", path(0, "")), &tail))
	var pages []string
	pages = append(pages, tail.Text)
	seen := map[int64]bool{}
	for tail.Before != 0 {
		require.False(t, seen[tail.Before], "cursor cycle")
		seen[tail.Before] = true
		require.LessOrEqual(t, len(pages), n)
		revision := tail.Revision
		require.NoError(t, json.Unmarshal(measure("older_page", path(tail.Before, revision)), &tail))
		require.Equal(t, revision, tail.Revision)
		pages = append(pages, tail.Text)
	}
	var joined strings.Builder
	for i := len(pages) - 1; i >= 0; i-- {
		joined.WriteString(pages[i])
	}
	require.Equal(t, expected, joined.String(), "pagination lost, duplicated, or reordered output")
	denied := htmxGet(app, fmt.Sprintf("/chat/output/%s?before=0&project_id=missing", execution.ID))
	require.NotEqual(t, http.StatusOK, denied.Code)
	data, err := json.MarshalIndent(map[string]any{"mode": mode, "blocks": n, "fixture_bytes": len(expected), "samples": samples}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(result, data, 0600))
}
