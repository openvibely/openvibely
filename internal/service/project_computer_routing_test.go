package service

import (
	"encoding/json"
	"github.com/stretchr/testify/require"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestComputerShellAndProcessStayInDesktop(t *testing.T) {
	var calls atomic.Int32
	_, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/process-tools") {
			io.WriteString(w, `{}`)
			return
		}
		calls.Add(1)
		var body struct {
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if body.Name == "process" && body.Input["session_id"] == "host-session" {
			io.WriteString(w, `{"error":"unknown or expired process session"}`)
			return
		}
		// Payloads including stdin and close_stdin must reach the isolated backend intact.
		data, _ := json.Marshal(body.Input)
		_ = json.NewEncoder(w).Encode(map[string]string{"output": string(data)})
	})
	require.NoError(t, s.SetToolTarget(t.Context(), p.ID, "computer"))
	tools := s.runtimeTools(t.Context(), p.ID, task.ID)
	require.True(t, tools.SkipDefaultTools)
	for _, tc := range []struct{ name, input string }{
		{"shell", `{"command":"pwd","yield_time_ms":250}`},
		{"bash", `{"command":"pwd"}`}, {"Bash", `{"command":"pwd"}`},
		{"process", `{"session_id":"desktop-job","input":"hello\n","close_stdin":true}`},
		{"process", `{"session_id":"desktop-job","terminate":true}`},
	} {
		out, handled, bad, err := tools.Executor(t.Context(), tc.name, json.RawMessage(tc.input))
		require.NoError(t, err)
		require.True(t, handled)
		require.False(t, bad)
		require.JSONEq(t, tc.input, out)
	}
	require.EqualValues(t, 5, calls.Load())
	for _, input := range []string{`{"session_id":"host-session"}`, `invalid`} {
		_, handled, bad, err := tools.Executor(t.Context(), "process", json.RawMessage(input))
		require.True(t, handled)
		require.True(t, bad)
		require.Error(t, err)
	}
}
