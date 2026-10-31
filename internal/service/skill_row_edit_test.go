package service

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/models"
	"testing"
)

func TestSkillApprovalEditsOnlyAffectedRows(t *testing.T) {
	rows := []json.RawMessage{
		json.RawMessage(`{"type":"message","role":"user","content":"preserve <>&"}`),
		json.RawMessage(`{"type":"function_call","name":"skill_view","call_id":"c","arguments":"{\"name\":\"test\"}"}`),
		json.RawMessage(`{"type":"function_call_output","call_id":"c","output":"waiting"}`),
	}
	original := models.ReplayFromRows("responses_input", rows)
	updated, found, err := completeSkillToolReplay(original, "question", json.RawMessage(`{"name":"test"}`), "approved", false, "c")
	if err != nil || !found {
		t.Fatal(found, err)
	}
	if updated.TranscriptJSON != "" || updated.Rows == nil {
		t.Fatal("approval rebuilt whole transcript")
	}
	for _, i := range []int{0, 1} {
		if &updated.Rows.Items[i][0] != &original.Rows.Items[i][0] {
			t.Fatal("unchanged row copied", i)
		}
	}
	if string(original.Rows.Items[2]) != string(rows[2]) {
		t.Fatal("source mutated")
	}
	var result map[string]any
	_ = json.Unmarshal(updated.Rows.Items[2], &result)
	if result["output"] != "approved" {
		t.Fatal(result)
	}
}
