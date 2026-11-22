package gemini

import (
	"encoding/json"
	"reflect"
	"testing"

	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
)

func TestReplayPreservesToolImages(t *testing.T) {
	for _, split := range []bool{false, true} {
		t.Run(map[bool]string{false: "same row", true: "separate rows"}[split], func(t *testing.T) {
			calls := geminiclient.Content{Role: "model"}
			var results []geminiclient.Part
			for _, id := range []string{"one", "two"} {
				calls.Parts = append(calls.Parts, geminiclient.Part{FunctionCall: &geminiclient.FunctionCall{ID: id, Name: "screenshot", Args: json.RawMessage(`{}`)}})
				results = append(results, geminiclient.Part{FunctionResponse: &geminiclient.FunctionResponse{ID: id, Name: "screenshot", Response: map[string]any{"output": "captured " + id}, Parts: []geminiclient.FunctionResponsePart{{InlineData: &geminiclient.InlineData{MimeType: "image/png", Data: id}}}}})
			}
			contents := []geminiclient.Content{calls}
			if split {
				for _, p := range results {
					contents = append(contents, geminiclient.Content{Role: "user", Parts: []geminiclient.Part{p}})
				}
			} else {
				contents = append(contents, geminiclient.Content{Role: "user", Parts: results})
			}
			contents = append(contents, geminiclient.TextContent("model", "I see both screenshots"))
			before, _ := json.Marshal(contents)
			messages := CompletionsFromContents(contents)
			if len(messages) != 5 {
				t.Fatalf("messages: %+v", messages)
			}
			for i, id := range []string{"one", "two"} {
				m := messages[i+1]
				if m.Role != "tool" || m.ToolCallID != id || m.Content != "captured "+id {
					t.Fatalf("tool result: %+v", m)
				}
			}
			if messages[3].Role != "user" || len(messages[3].ImageContent) != 2 || messages[4].Content != "I see both screenshots" {
				t.Fatalf("image ordering: %+v", messages)
			}
			back := ContentsFromCompletions(messages)
			var data []string
			for _, c := range back {
				for _, p := range c.Parts {
					if p.InlineData != nil {
						data = append(data, p.InlineData.Data)
					}
				}
			}
			if !reflect.DeepEqual(data, []string{"one", "two"}) {
				t.Fatalf("round-trip images: %v", data)
			}
			after, _ := json.Marshal(contents)
			if string(before) != string(after) {
				t.Fatal("conversion changed source history")
			}
		})
	}
}

func TestReplayPreservesImagesAtIncompleteToolRound(t *testing.T) {
	contents := []geminiclient.Content{
		{Role: "model", Parts: []geminiclient.Part{
			{FunctionCall: &geminiclient.FunctionCall{ID: "one", Name: "first"}},
			{FunctionCall: &geminiclient.FunctionCall{ID: "two", Name: "second"}},
		}},
		{Role: "user", Parts: []geminiclient.Part{{FunctionResponse: &geminiclient.FunctionResponse{ID: "one", Name: "first", Response: map[string]any{"output": "ok"}, Parts: []geminiclient.FunctionResponsePart{
			{FileData: &geminiclient.FileData{MimeType: "image/jpeg", FileURI: "https://example.com/image.jpg"}},
		}}}}},
	}
	messages := CompletionsFromContents(contents)
	if len(messages) != 3 || len(messages[2].ImageContent) != 1 {
		t.Fatalf("lost image: %+v", messages)
	}
	url := messages[2].ImageContent[0]["image_url"].(map[string]interface{})["url"]
	if url != "https://example.com/image.jpg" {
		t.Fatalf("image URI: %v", url)
	}
}
