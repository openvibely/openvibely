package geminiclient

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"strings"
	"testing"
)

func TestImagePreflightBudget(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		for _, nested := range []bool{false, true} {
			inline := &InlineData{MimeType: "image/png", Data: strings.Repeat("A", 6*1024*1024)}
			part := Part{InlineData: inline}
			if nested {
				part = Part{FunctionResponse: &FunctionResponse{Name: "screenshot", Response: map[string]any{"result": "ok"}, Parts: []FunctionResponsePart{{InlineData: inline}}}}
			}
			req := generateRequest{Contents: []Content{{Role: "user", Parts: []Part{part}}}}
			var wire any = req
			if oauth {
				wire = map[string]any{"model": "test", "project": "test", "request": req}
			}
			body, err := json.Marshal(wire)
			if err != nil {
				t.Fatal(err)
			}
			before := append([]byte(nil), body...)
			if err := ensureRequestFits(body, &Options{ContextWindow: 32768, MaxOutputTokens: 4096}); err != nil {
				t.Fatalf("oauth=%v nested=%v: %v", oauth, nested, err)
			}
			if tokens := estimateRequestTokens(body); tokens < 1122 || tokens > 1400 {
				t.Fatalf("unexpected image budget: %d", tokens)
			}
			if !bytes.Equal(before, body) {
				t.Fatal("budgeting modified request")
			}
		}
	}
}

func TestImageDimensionBudget(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 1920, 1080))); err != nil {
		t.Fatal(err)
	}
	if got := estimateInlineImageTokens(base64.StdEncoding.EncodeToString(buf.Bytes())); got != 2764 {
		t.Fatalf("got %d, want 2764", got)
	}
	if got := estimateInlineImageTokens("bad data"); got != 1122 {
		t.Fatalf("fallback: %d", got)
	}
}

func TestImageBudgetRetainsTextAndToolData(t *testing.T) {
	large := strings.Repeat("x", 128*1024)
	for _, part := range []Part{
		{Text: large},
		{FunctionResponse: &FunctionResponse{Name: "read", Response: map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": large}}}},
		{FunctionCall: &FunctionCall{Name: "write", Args: json.RawMessage(`{"data":"` + large + `"}`)}},
	} {
		body, err := json.Marshal(generateRequest{Contents: []Content{{Parts: []Part{part}}}})
		if err != nil {
			t.Fatal(err)
		}
		if err := ensureRequestFits(body, &Options{ContextWindow: 32768, MaxOutputTokens: 4096}); err == nil {
			t.Fatal("oversized text should fail preflight")
		}
	}
}

func TestRemoteAndMultipleImageBudget(t *testing.T) {
	body, err := json.Marshal(generateRequest{Contents: []Content{{Parts: []Part{
		{FileData: &FileData{MimeType: "image/jpeg", FileURI: "gs://bucket/image.jpg"}},
		{InlineData: &InlineData{MimeType: "image/png", Data: "bad"}},
	}}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := estimateRequestTokens(body); got < 2244 || got > 2400 {
		t.Fatalf("two-image budget: %d", got)
	}
}

func TestImageURLWithoutMimeBudget(t *testing.T) {
	tokens, err := EstimateContentsTokens([]Content{{Role: "user", Parts: []Part{{FileData: &FileData{FileURI: "https://example.com/image"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if tokens < 1122 || tokens > 1250 {
		t.Fatalf("URL image estimate: %d", tokens)
	}
}
