package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	llmcontracts "github.com/openvibely/openvibely/internal/llm/contracts"
	"github.com/stretchr/testify/require"
)

func TestComputerForwardsExecutionCancellation(t *testing.T) {
	for _, name := range []string{"shell", "computer_shell"} {
		t.Run(name, func(t *testing.T) {
			sent := make(chan string, 1)
			cancelled := make(chan string, 1)
			_, s, p, task := computerParityFixture(t, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasSuffix(r.URL.Path, "/process-tools"):
					var body map[string]any
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					sent <- body["execution_id"].(string)
					io.WriteString(w, `{"output":"session ready","job_id":"job","running":true}`)
				case strings.HasSuffix(r.URL.Path, "/executions/cancel"):
					require.NoError(t, r.Context().Err())
					var body map[string]string
					require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					cancelled <- body["execution_id"]
					io.WriteString(w, `{"cancelled":true}`)
				default:
					io.WriteString(w, `{}`)
				}
			})
			require.NoError(t, s.SetToolTarget(t.Context(), p.ID, "computer"))
			owner, cancel := context.WithCancel(llmcontracts.WithArtifactExecutionID(t.Context(), "execution-123"))
			defer cancel()
			rt := s.runtimeTools(owner, p.ID, task.ID)
			_, handled, _, err := rt.Executor(owner, name, json.RawMessage(`{"command":"sleep 30","yield_time_ms":250}`))
			require.NoError(t, err)
			require.True(t, handled)
			require.Equal(t, "execution-123", <-sent)
			cancel()
			select {
			case id := <-cancelled:
				require.Equal(t, "execution-123", id)
			case <-time.After(3 * time.Second):
				t.Fatal("no execution cancellation sent")
			}
		})
	}
}
