// Deterministic local model peer. Its CPU/memory are outside the app process.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/coder/websocket"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func main() {
	dir := flag.String("directory", "", "output directory")
	flag.Parse()
	var mu sync.Mutex
	turn, stage, outputBytes, deltaMS, compactions := 0, 0, 0, 5, 0
	toolFile := ""
	withTools := false
	requests := []map[string]any{}
	contexts := map[string][]any{}
	serial := 0
	summaries := 0
	snapshot := func() any {
		return map[string]any{"requests": requests, "compactions": compactions, "summaries": summaries}
	}
	generate := func(ctx context.Context, raw []byte, compact bool, emit func(any) error) any {
		mu.Lock()
		defer mu.Unlock()
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		inputItems, _ := body["input"].([]any)
		if previous, _ := body["previous_response_id"].(string); previous != "" {
			inputItems = append(append([]any{}, contexts[previous]...), inputItems...)
		}
		inputJSON, _ := json.Marshal(inputItems)
		encoded := string(inputJSON)
		summary := strings.Contains(string(raw), "CONTEXT CHECKPOINT COMPACTION")
		requests = append(requests, map[string]any{"turn": turn, "with_tools": withTools, "compact": compact, "summary": summary, "context_bytes": len(inputJSON), "request_bytes": len(raw), "prior_turn_seen": turn == 0 || strings.Contains(encoded, fmt.Sprintf("TURN_%d_END", turn-1)), "checkpoint_seen": strings.Contains(encoded, "APPFLOW_COMPACT"), "tool_result_seen": strings.Contains(encoded, fmt.Sprintf("TOOL_%d_END", turn))})
		if compact {
			compactions++
			return map[string]any{"output": []any{map[string]any{"type": "compaction", "encrypted_content": fmt.Sprintf("APPFLOW_COMPACT_%d", compactions)}}}
		}
		current := stage
		if summary {
			summaries++
		} else {
			stage++
		}
		text := fmt.Sprintf("I will inspect the requested evidence for turn %d.\n\n", turn)
		items := []any{}
		if summary {
			text = fmt.Sprintf("APPFLOW_SUMMARY_%d: Continue inspecting the evidence files for the user. Completed turns before %d are summarized. Preserve tool results and answer the current request.", summaries, turn)
			_ = emit(map[string]any{"type": "response.output_text.delta", "delta": text})
			items = append(items, map[string]any{"type": "message", "id": fmt.Sprintf("summary_%d", summaries), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
		} else if current == 0 && withTools {
			args, _ := json.Marshal(map[string]any{"file_path": toolFile, "limit": 10000})
			items = append(items, map[string]any{"type": "message", "id": fmt.Sprintf("prefix_%d", turn), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}}, map[string]any{"type": "function_call", "id": fmt.Sprintf("fc_%d", turn), "call_id": fmt.Sprintf("call_%d", turn), "name": "read_file", "arguments": string(args)})
			if emit(map[string]any{"type": "response.output_text.delta", "delta": text}) != nil {
				return nil
			}
		} else {
			var b strings.Builder
			fmt.Fprintf(&b, "## Turn %d findings\n\n", turn)
			for n := 0; b.Len() < outputBytes; n++ {
				fmt.Fprintf(&b, "Finding %05d: module %d has a distinct result for turn %d. Preserve **ordering** and verify item `%d`.\n\n", n, n%97, turn, n*17+turn)
			}
			fmt.Fprintf(&b, "\nTURN_%d_END\n\n[STATUS: COMPLETED]\n", turn)
			text = b.String()
			if limit, ok := body["max_output_tokens"].(float64); ok && (len(text)+3)/4 > int(limit) {
				_ = emit(map[string]any{"type": "error", "message": "fixture exceeds requested max_output_tokens"})
				return nil
			}
			for i := 0; i < len(text); i += 64 {
				end := i + 64
				if end > len(text) {
					end = len(text)
				}
				if emit(map[string]any{"type": "response.output_text.delta", "delta": text[i:end]}) != nil {
					return nil
				}
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(time.Duration(deltaMS) * time.Millisecond):
				}
			}
			items = append(items, map[string]any{"type": "message", "id": fmt.Sprintf("msg_%d", turn), "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": text}}})
		}
		requests[len(requests)-1]["response_text_bytes"] = len(text)
		requests[len(requests)-1]["estimated_output_tokens"] = (len(text) + 3) / 4
		requests[len(requests)-1]["requested_output_tokens"] = body["max_output_tokens"]
		for i, item := range items {
			if emit(map[string]any{"type": "response.output_item.done", "output_index": i, "item": item}) != nil {
				return nil
			}
		}
		serial++
		responseID := fmt.Sprintf("resp_%d", serial)
		contexts[responseID] = append(append([]any{}, inputItems...), items...)
		_ = emit(map[string]any{"type": "response.completed", "response": map[string]any{"id": responseID, "status": "completed", "output": items, "usage": map[string]int{"input_tokens": (len(inputJSON) + 3) / 4, "output_tokens": (len(text) + 3) / 4}}})
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/control", func(w http.ResponseWriter, r *http.Request) {
		var c struct {
			Turn      int    `json:"turn"`
			WithTools bool   `json:"with_tools"`
			Output    int    `json:"output_bytes"`
			File      string `json:"tool_file"`
			Delta     int    `json:"delta_ms"`
		}
		_ = json.NewDecoder(r.Body).Decode(&c)
		mu.Lock()
		turn, stage, outputBytes, toolFile, deltaMS, withTools = c.Turn, 0, c.Output, c.File, c.Delta, c.WithTools
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(snapshot())
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") == "websocket" {
			conn, e := websocket.Accept(w, r, nil)
			if e != nil {
				return
			}
			defer conn.CloseNow()
			conn.SetReadLimit(32 << 20)
			for {
				_, raw, e := conn.Read(r.Context())
				if e != nil {
					return
				}
				generate(r.Context(), raw, false, func(v any) error { b, _ := json.Marshal(v); return conn.Write(r.Context(), websocket.MessageText, b) })
			}
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			return
		}
		if strings.HasSuffix(r.URL.Path, "/compact") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(generate(r.Context(), raw, true, nil))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		generate(r.Context(), raw, false, func(v any) error {
			b, _ := json.Marshal(v)
			_, e := fmt.Fprintf(w, "data: %s\n\n", b)
			w.(http.Flusher).Flush()
			return e
		})
	})
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		panic(e)
	}
	if e = os.WriteFile(filepath.Join(*dir, "model-url"), []byte("http://"+listener.Addr().String()), 0600); e != nil {
		panic(e)
	}
	if e = http.Serve(listener, mux); e != nil {
		panic(e)
	}
}
