//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/openvibely/openvibely/internal/computerrecording"
)

// RFB has no secure-field signal. Chromium's local debugging endpoint can
// identify focused DOM password fields. This is best effort: native widgets,
// terminal prompts, inaccessible frames and unavailable CDP remain unknown.
// Unknown is captured by policy, not asserted to be an ordinary field.
func focusedInputSecurity() computerrecording.InputSecurity {
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://127.0.0.1:"+computerPort(1)+"/json/list", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return computerrecording.InputUnknown
	}
	defer res.Body.Close()
	var targets []struct {
		Type                 string
		WebSocketDebuggerURL string
	}
	if res.StatusCode != 200 || json.NewDecoder(http.MaxBytesReader(nil, res.Body, 1<<20)).Decode(&targets) != nil {
		return computerrecording.InputUnknown
	}
	result := computerrecording.InputUnknown
	for _, target := range targets {
		if target.Type != "page" {
			continue
		}
		security := inspectRecordingField(ctx, target.WebSocketDebuggerURL)
		if security == computerrecording.InputSecure {
			return security
		}
		if security == computerrecording.InputOrdinary {
			result = security
		}
	}
	return result
}

func recordingCDP(ctx context.Context, c *websocket.Conn, id int, method string, params any, out any) error {
	data, err := json.Marshal(map[string]any{"id": id, "method": method, "params": params})
	if err != nil {
		return err
	}
	if err = c.Write(ctx, websocket.MessageText, data); err != nil {
		return err
	}
	for {
		_, data, err = c.Read(ctx)
		if err != nil {
			return err
		}
		var message struct {
			ID     int
			Result json.RawMessage
			Error  json.RawMessage
		}
		if err = json.Unmarshal(data, &message); err != nil {
			return err
		}
		if message.ID != id {
			continue
		}
		if len(message.Error) != 0 {
			return fmt.Errorf("field inspection failed")
		}
		return json.Unmarshal(message.Result, out)
	}
}

func inspectRecordingField(ctx context.Context, endpoint string) computerrecording.InputSecurity {
	c, _, err := websocket.Dial(ctx, endpoint, nil)
	if err != nil {
		return computerrecording.InputUnknown
	}
	defer c.CloseNow()
	c.SetReadLimit(1 << 20)
	var tree struct {
		FrameTree struct{ Frame struct{ ID string } }
	}
	if recordingCDP(ctx, c, 1, "Page.getFrameTree", map[string]any{}, &tree) != nil {
		return computerrecording.InputUnknown
	}
	var world struct{ ExecutionContextID int }
	if recordingCDP(ctx, c, 2, "Page.createIsolatedWorld", map[string]any{"frameId": tree.FrameTree.Frame.ID, "worldName": "ov-recording-field"}, &world) != nil {
		return computerrecording.InputUnknown
	}
	var evaluation struct {
		Result           struct{ Value string }
		ExceptionDetails json.RawMessage
	}
	if recordingCDP(ctx, c, 3, "Runtime.evaluate", map[string]any{"contextId": world.ExecutionContextID, "expression": recordingFieldExpression, "returnByValue": true}, &evaluation) != nil || len(evaluation.ExceptionDetails) != 0 {
		return computerrecording.InputUnknown
	}
	switch evaluation.Result.Value {
	case "secure":
		return computerrecording.InputSecure
	case "ordinary":
		return computerrecording.InputOrdinary
	default:
		return computerrecording.InputUnknown
	}
}

// Read metadata only, never field values. An isolated world avoids page-defined
// JavaScript overriding DOM accessors. Closed shadows and cross-origin frames
// cannot be inspected by this detector and return unknown.
const recordingFieldExpression = `(() => {
 let d = document;
 if (!d.hasFocus()) return 'unknown';
 let e = d.activeElement;
 for (let depth = 0; e && depth < 32; depth++) {
  if (e.shadowRoot && e.shadowRoot.activeElement) { e = e.shadowRoot.activeElement; continue; }
  if (e.tagName === 'IFRAME' || e.tagName === 'FRAME') {
   try { d = e.contentDocument; if (!d) return 'unknown'; e = d.activeElement; continue; }
   catch (_) { return 'unknown'; }
  }
  if ((e.tagName === 'INPUT' && e.type === 'password') ||
      ['current-password','new-password'].includes(e.autocomplete) ||
      ['disc','circle','square'].includes(d.defaultView.getComputedStyle(e).webkitTextSecurity)) return 'secure';
  if (e.tagName === 'INPUT' || e.tagName === 'TEXTAREA' || e.isContentEditable) return 'ordinary';
  return 'unknown';
 }
 return 'unknown';
})()`
