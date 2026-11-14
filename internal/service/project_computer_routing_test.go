package service

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputerShellAndProcessStayInDesktop(t *testing.T) {
	var shells, polls, cancellations atomic.Int32
	var finished atomic.Bool
	_, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/shell"):
			shells.Add(1)
			var args map[string]any
			require.NoError(t, json.NewDecoder(r.Body).Decode(&args))
			require.Equal(t, "Files", args["working_directory"])
			require.Equal(t, true, args["background"])
			require.Equal(t, float64(120), args["timeout_seconds"])
			io.WriteString(w, `{"job_id":"desktop-job","running":true,"output":""}`)
		case strings.HasSuffix(r.URL.Path, "/jobs/cancel"):
			cancellations.Add(1)
			var args map[string]string
			require.NoError(t, json.NewDecoder(r.Body).Decode(&args))
			require.Equal(t, "desktop-job", args["job_id"])
			finished.Store(true)
			io.WriteString(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			polls.Add(1)
			if r.URL.Query().Get("job_id") != "desktop-job" {
				io.WriteString(w, `{"jobs":[]}`)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []any{map[string]any{"job_id": "desktop-job", "running": !finished.Load(), "output": "remote output", "exit_code": 0}}})
		default:
			io.WriteString(w, `{}`)
		}
	})
	require.NoError(t, s.SetToolTarget(t.Context(), p.ID, "computer"))
	tools := s.runtimeTools(t.Context(), p.ID, task.ID)
	require.True(t, tools.SkipDefaultTools)
	names := map[string]bool{}
	for _, d := range tools.Definitions {
		names[d.Name] = true
	}
	require.True(t, names["shell"])
	require.True(t, names["process"])
	for _, name := range []string{"shell", "bash", "Bash"} {
		out, handled, bad, err := tools.Executor(t.Context(), name, json.RawMessage(`{"command":"pwd","yield_time_ms":250}`))
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, bad)
		require.Contains(t, out, `"session_id":"desktop-job"`)
		require.Contains(t, out, "remote output")
	}
	out, handled, bad, err := tools.Executor(t.Context(), "process", json.RawMessage(`{"session_id":"desktop-job","yield_time_ms":250}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)
	require.Contains(t, out, `"session_id":"desktop-job"`)
	out, handled, bad, err = tools.Executor(t.Context(), "process", json.RawMessage(`{"session_id":"desktop-job","terminate":true,"yield_time_ms":250}`))
	require.NoError(t, err)
	require.True(t, handled)
	require.False(t, bad)
	require.Contains(t, out, `"running":false`)
	require.EqualValues(t, 3, shells.Load())
	require.Positive(t, polls.Load())
	require.EqualValues(t, 1, cancellations.Load())
	for _, input := range []string{
		`{"session_id":"host-session","yield_time_ms":250}`,
		`{"session_id":"desktop-job","input":"secret"}`,
		`{"session_id":"desktop-job","close_stdin":true}`,
		`{}`, `invalid`,
	} {
		_, handled, bad, err := tools.Executor(t.Context(), "process", json.RawMessage(input))
		require.True(t, handled, input)
		require.True(t, bad, input)
		require.Error(t, err, input)
	}
}
