package mcpclient

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/testutil/retrytest"
)

func TestHTTPMCPToolCallDoesNotReplaySideEffects(t *testing.T) {
	for _, failure := range []string{"408", "500", "502", "503", "504", "529", "lost response", "lost response with idempotency header", "truncated response", "redirect", "permanent redirect"} {
		t.Run(failure, func(t *testing.T) {
			var effects atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer r.Body.Close()
				var request jsonRPCRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				if request.Method == "initialize" {
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, request.ID)
					return
				}
				if request.Method != "tools/call" || r.Header.Get("X-MCP-Token") != "fixture" {
					t.Errorf("unexpected request: %+v", request)
				}
				if effects.Add(1) > 1 {
					// A replay looks successful but has already repeated the action.
					fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{"content":[{"type":"text","text":"sent twice"}]}}`, request.ID)
					return
				}
				switch failure {
				case "lost response", "lost response with idempotency header":
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					conn.Close()
				case "truncated response":
					w.Header().Set("Content-Length", "100")
					io.WriteString(w, `{"jsonrpc":`)
				case "redirect", "permanent redirect":
					w.Header().Set("Location", "/destination")
					status := http.StatusTemporaryRedirect
					if failure == "permanent redirect" {
						status = http.StatusPermanentRedirect
					}
					w.WriteHeader(status)
				default:
					var status int
					fmt.Sscanf(failure, "%d", &status)
					w.WriteHeader(status)
					io.WriteString(w, "response unavailable after action")
				}
			}))
			defer srv.Close()
			server := &MCPServer{kind: "http", url: srv.URL, httpClient: srv.Client(), headers: map[string]string{"X-MCP-Token": "fixture"}}
			if failure == "lost response with idempotency header" {
				server.headers["Idempotency-Key"] = "not-a-deduplication-contract"
			}
			// Reuse an established connection so transport-level replay is also
			// exercised when the server drops the tool response.
			if err := server.initialize(retrytest.Context()); err != nil {
				t.Fatal(err)
			}
			output, isError, err := server.Execute(retrytest.Context(), "send_email", map[string]interface{}{"to": "fixture@example.invalid"})
			if got := effects.Load(); got != 1 {
				t.Errorf("side effects = %d, want 1", got)
			}
			if err == nil || !isError || output != "" || !strings.Contains(err.Error(), "MCP HTTP") {
				t.Fatalf("output=%q isError=%v err=%v", output, isError, err)
			}
			if strings.Contains(strings.ToLower(err.Error()), "rolled back") {
				t.Fatalf("ambiguous failure claims rollback: %v", err)
			}
		})
	}
}
