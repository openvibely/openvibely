package service

import (
	"encoding/json"
	"github.com/openvibely/openvibely/pkg/agenttools"
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

func TestExplicitComputerProcessToolsShareStandardInterface(t *testing.T) {
	for _, target := range []string{"worktree", "computer"} {
		t.Run(target, func(t *testing.T) {
			_, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasSuffix(r.URL.Path, "/process-tools") {
					if strings.HasSuffix(r.URL.Path, "/shell") {
						t.Error("obsolete shell endpoint used")
					}
					io.WriteString(w, `{}`)
					return
				}
				var body struct {
					Name  string
					Input json.RawMessage
				}
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				require.Contains(t, []string{"shell", "process"}, body.Name)
				if strings.Contains(string(body.Input), "long-command") {
					_ = json.NewEncoder(w).Encode(map[string]string{"output": "\n[process]\nsession_id: sh_example\nstatus: running\nnext: call process with this session_id to read output or send input\n[/process]"})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]string{"output": string(body.Input)})
			})
			require.NoError(t, s.SetToolTarget(t.Context(), p.ID, target))
			rt := s.runtimeTools(t.Context(), p.ID, task.ID)
			for _, pair := range []struct{ alias, standard string }{{"computer_shell", "shell"}, {"computer_job", "process"}} {
				found := false
				for _, definition := range rt.Definitions {
					if definition.Name != pair.alias {
						continue
					}
					for _, standard := range agenttools.Definitions() {
						if standard.Name == pair.standard {
							require.Equal(t, standard.Schema, definition.Schema)
							found = true
						}
					}
				}
				require.True(t, found)
			}
			for _, tc := range []struct{ name, input string }{
				{"computer_shell", `{"command":"read x; echo $x","timeout":60,"yield_time_ms":250}`},
				{"computer_job", `{"session_id":"desktop-session","input":"hello\n","close_stdin":true,"yield_time_ms":250}`},
				{"computer_job", `{"session_id":"desktop-session","yield_time_ms":5000}`},
				{"computer_job", `{"session_id":"desktop-session","terminate":true}`},
			} {
				out, handled, bad, err := rt.Executor(t.Context(), tc.name, json.RawMessage(tc.input))
				require.NoError(t, err)
				require.True(t, handled)
				require.False(t, bad)
				require.JSONEq(t, tc.input, out)
			}
			out, handled, bad, err := rt.Executor(t.Context(), "computer_shell", json.RawMessage(`{"command":"long-command"}`))
			require.NoError(t, err)
			require.True(t, handled)
			require.False(t, bad)
			require.Contains(t, out, "next: call computer_job with this session_id")
		})
	}
}
