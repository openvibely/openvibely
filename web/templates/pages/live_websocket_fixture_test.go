package pages

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// A real WebSocket transport for page-only fixtures. Legacy event text is
// decoded at the fixture boundary; the browser receives the production protocol.
func serveBrowserLiveFixture(w http.ResponseWriter, r *http.Request, events <-chan string, intercept ...func(string) bool) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	commands := make(chan map[string]any, 16)
	go func() {
		defer cancel()
		for {
			var cmd map[string]any
			if wsjson.Read(ctx, conn, &cmd) != nil {
				return
			}
			select {
			case commands <- cmd:
			case <-ctx.Done():
				return
			}
		}
	}()
	if wsjson.Write(ctx, conn, map[string]any{"event": "ready"}) != nil {
		return
	}
	id := ""
	for {
		// Scripted events stay queued until the page subscribes; otherwise an
		// event emitted before the subscribe handshake is silently lost.
		pending := events
		if id == "" {
			pending = nil
		}
		select {
		case <-ctx.Done():
			return
		case cmd := <-commands:
			if cmd["op"] == "live" {
				id, _ = cmd["id"].(string)
				if wsjson.Write(ctx, conn, map[string]any{"id": id, "event": "subscribed"}) != nil {
					return
				}
			}
			if cmd["op"] == "unsubscribe" && cmd["id"] == id {
				id = ""
			}
		case text := <-pending:
			if len(intercept) > 0 && intercept[0](text) {
				return
			}
			name, data := "", ""
			if strings.HasPrefix(text, "{") {
				var payload struct {
					Type string `json:"type"`
				}
				if json.Unmarshal([]byte(text), &payload) != nil {
					return
				}
				name, data = payload.Type, text
			}
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "event: ") {
					name = strings.TrimPrefix(line, "event: ")
				}
				if strings.HasPrefix(line, "data: ") {
					data = strings.TrimPrefix(line, "data: ")
				}
			}
			if wsjson.Write(ctx, conn, map[string]any{"id": id, "event": name, "data": json.RawMessage(data)}) != nil {
				return
			}
		}
	}
}
