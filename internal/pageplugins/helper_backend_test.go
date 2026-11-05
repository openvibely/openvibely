package pageplugins

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

// TestMain doubles as a JSON-RPC plugin backend when PP_HELPER_BACKEND=1.
func TestMain(m *testing.M) {
	if os.Getenv("PP_HELPER_BACKEND") == "1" || (len(os.Args) > 1 && os.Args[1] == "--pp-helper-backend") {
		runHelperBackend()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runHelperBackend() {
	out := bufio.NewWriter(os.Stdout)
	send := func(v any) {
		raw, _ := json.Marshal(v)
		out.Write(append(raw, '\n'))
		out.Flush()
	}
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64<<10), 2<<20)
	for sc.Scan() {
		var msg struct {
			ID     *int64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(sc.Bytes(), &msg) != nil {
			continue
		}
		reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result}) }
		switch msg.Method {
		case "initialize":
			if len(os.Args) > 3 && os.Args[2] == "--block-init" {
				_ = os.WriteFile(os.Args[3], []byte("started"), 0600)
				time.Sleep(30 * time.Second)
			}
			fmt.Fprintln(os.Stderr, "helper backend initialized\x1b[0m")
			reply(map[string]string{"protocol": ProtocolVersion})
		case "operation/invoke":
			var p struct {
				Operation string          `json:"operation"`
				Input     json.RawMessage `json:"input"`
			}
			_ = json.Unmarshal(msg.Params, &p)
			switch p.Operation {
			case "echo":
				reply(map[string]any{"echo": p.Input, "pid": os.Getpid()})
			case "slow":
				send(map[string]any{"jsonrpc": "2.0", "method": "events/publish", "params": map[string]any{"type": "operation.started"}})
				go func(id *int64) {
					time.Sleep(10 * time.Second)
					send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{}})
				}(msg.ID)
			case "crash":
				os.Exit(3)
			case "notify":
				send(map[string]any{"jsonrpc": "2.0", "method": "state/set", "params": map[string]any{"state": map[string]int{"n": 7}}})
				send(map[string]any{"jsonrpc": "2.0", "method": "events/publish", "params": map[string]any{"type": "data.changed"}})
				reply(map[string]any{"ok": true})
			}
		case "$/cancelRequest":
		case "shutdown":
			reply(nil)
		case "exit":
			os.Exit(0)
		}
	}
}

func helperSpec(t *testing.T) ProcessSpec {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ProcessSpec{Path: exe, Dir: t.TempDir(), Env: map[string]string{"PP_HELPER_BACKEND": "1"}}
}
