package gemini

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
)

func TestBuildHistorySharedFormats(t *testing.T) {
	raw := `[
 {"role":"user","content":"Inspect this file"},
 {"role":"assistant","content":"Opening it","tool_calls":[{"id":"call-1","type":"function","function":{"name":"view_image","arguments":"{\"file_path\":\"image.png\"}"}}]},
 {"role":"tool","tool_call_id":"call-1","content":"Screenshot captured"},
 {"role":"user","content":[{"type":"text","text":"Tool image"},{"type":"image_url","image_url":{"url":"data:image/png;base64,YWJj"}}]},
 {"role":"assistant","content":"I see the image"}
 ]`
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatal(err)
	}
	want := []geminiclient.Content{
		geminiclient.TextContent("user", "Inspect this file"),
		{Role: "model", Parts: []geminiclient.Part{{Text: "Opening it"}, {FunctionCall: &geminiclient.FunctionCall{ID: "call-1", Name: "view_image", Args: json.RawMessage(`{"file_path":"image.png"}`)}}}},
		{Role: "user", Parts: []geminiclient.Part{{FunctionResponse: &geminiclient.FunctionResponse{ID: "call-1", Name: "view_image", Response: map[string]any{"output": "Screenshot captured"}}}}},
		{Role: "user", Parts: []geminiclient.Part{{Text: "Tool image"}, {InlineData: &geminiclient.InlineData{MimeType: "image/png", Data: "YWJj"}}}},
		geminiclient.TextContent("model", "I see the image"),
	}
	for name, replay := range map[string]models.ExecutionReplayMessage{
		"json array": {TranscriptJSON: raw},
		"rows":       models.ReplayFromRows("", rows),
	} {
		t.Run(name, func(t *testing.T) {
			got := BuildHistory([]models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{replay}}})
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("history mismatch\ngot: %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestBuildHistoryNativeJSONKeepsSignatures(t *testing.T) {
	replay := models.ExecutionReplayMessage{TranscriptJSON: `{"contents":[{"role":"model","parts":[{"text":"answer","thoughtSignature":"signature"}]}]}`}
	got := BuildHistory([]models.Execution{{ReplayMessages: []models.ExecutionReplayMessage{replay}}})
	if len(got) != 1 || len(got[0].Parts) != 1 || got[0].Parts[0].ThoughtSignature != "signature" {
		t.Fatalf("native history lost: %+v", got)
	}
}
