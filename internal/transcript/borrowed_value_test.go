package transcript

import (
	"encoding/json"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/jsonview"
)

func TestFreezeBorrowedValueRetainsImmutablePayload(t *testing.T) {
	raw := json.RawMessage(`"stored tool result"`)
	view, err := jsonview.Borrow(raw)
	if err != nil {
		t.Fatal(err)
	}
	mutable := json.RawMessage(`"mutable result"`)
	input := map[string]any{"output": view, "other": mutable, "type": "function_call_output"}
	frozen := freezeReplayValue(input).(map[string]any)
	if &frozen["output"].(jsonview.Value).Raw()[0] != &raw[0] {
		t.Fatal("immutable payload was copied")
	}
	if &frozen["other"].(json.RawMessage)[0] == &mutable[0] {
		t.Fatal("mutable payload was shared")
	}
	input["output"] = "edited"
	mutable[1] = 'M'
	if text, _ := frozen["output"].(jsonview.Value).Text(); text != "stored tool result" {
		t.Fatal(text)
	}
	if string(frozen["other"].(json.RawMessage)) != `"mutable result"` {
		t.Fatal("snapshot changed")
	}
	if !equalReplayValue(view, frozen["output"]) {
		t.Fatal("borrowed equality changed")
	}
	other, _ := jsonview.Borrow(json.RawMessage(`"other"`))
	if equalReplayValue(other, view) {
		t.Fatal("different payloads compare equal")
	}
	// Concrete provider DTOs also retain the immutable view, while copying
	// mutable exported fields through the existing reflective fallback.
	typed := struct {
		Content jsonview.Value
		Mutable []byte
	}{view, []byte("old")}
	copy := freezeReplayValue(typed).(struct {
		Content jsonview.Value
		Mutable []byte
	})
	typed.Mutable[0] = 'X'
	if string(copy.Mutable) != "old" || &copy.Content.Raw()[0] != &raw[0] {
		t.Fatal("typed ownership changed")
	}
}
