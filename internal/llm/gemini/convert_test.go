package gemini

import (
	"encoding/json"
	"reflect"
	"testing"

	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
	openaiclient "github.com/openvibely/openvibely/pkg/openai_client"
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

func TestReplayImageURLsRoundTrip(t *testing.T) {
	for _, uri := range []string{"https://example.com/image.png", "https://example.com/download?signature=abc%2Fdef&expires=123", "gs://bucket/image.webp"} {
		for _, nested := range []bool{false, true} {
			part := geminiclient.Part{FileData: &geminiclient.FileData{MimeType: "image/png", FileURI: uri}}
			contents := []geminiclient.Content{{Role: "user", Parts: []geminiclient.Part{{Text: "look"}, part}}}
			if nested {
				contents = []geminiclient.Content{
					{Role: "model", Parts: []geminiclient.Part{{FunctionCall: &geminiclient.FunctionCall{ID: "image", Name: "view_image", Args: json.RawMessage(`{}`)}}}},
					{Role: "user", Parts: []geminiclient.Part{{FunctionResponse: &geminiclient.FunctionResponse{ID: "image", Name: "view_image", Response: map[string]any{"output": "look"}, Parts: []geminiclient.FunctionResponsePart{{FileData: part.FileData}}}}}},
				}
			}
			messages := CompletionsFromContents(contents)
			back := ContentsFromCompletions(messages)
			// Shared image messages carry URLs without MIME metadata. They must
			// survive another conversion without acquiring an invented MIME type.
			again := CompletionsFromContents(back)
			for _, converted := range [][]openaiclient.CompletionsHistoryMessage{messages, again} {
				var urls []string
				for _, m := range converted {
					for _, p := range m.ImageContent {
						if image, ok := p["image_url"].(map[string]interface{}); ok {
							urls = append(urls, image["url"].(string))
						}
					}
				}
				if !reflect.DeepEqual(urls, []string{uri}) {
					t.Fatalf("nested=%v: URLs=%v want %s", nested, urls, uri)
				}
			}
			if nested && (messages[1].Role != "tool" || messages[1].ToolCallID != "image" || messages[1].Content != "look") {
				t.Fatal("tool result changed")
			}
		}
	}
}

func TestReplayMixedInlineAndURLImages(t *testing.T) {
	messages := []openaiclient.CompletionsHistoryMessage{{Role: "user", ImageContent: []map[string]interface{}{
		{"type": "text", "text": "compare images"},
		{"type": "image_url", "image_url": map[string]interface{}{"url": "https://example.com/image"}},
		{"type": "image_url", "image_url": map[string]interface{}{"url": "data:image/png;base64,YWJj"}},
	}}}
	contents := ContentsFromCompletions(messages)
	if len(contents) != 1 || len(contents[0].Parts) != 3 {
		t.Fatalf("lost parts: %+v", contents)
	}
	parts := contents[0].Parts
	if parts[0].Text != "compare images" || parts[1].FileData == nil || parts[1].FileData.FileURI != "https://example.com/image" || parts[2].InlineData == nil || parts[2].InlineData.Data != "YWJj" {
		t.Fatalf("wrong conversion: %+v", parts)
	}
}
