package geminiclient

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/llm/streamdecode"
)

func TestStreamCaptureAcrossContinuationFailure(t *testing.T) {
	var capture streamdecode.Text
	result := &turnResult{}
	assembler := &callAssembler{}
	first := "data: " + `{"candidates":[{"content":{"parts":[{"text":"first "}]},"finishReason":"MAX_TOKENS","continuationToken":"next"}]}` + "\n\n"
	if err := parseStreamInto(strings.NewReader(first), nil, &capture, result, assembler); err != nil {
		t.Fatal(err)
	}
	second := "data: " + `{"candidates":[{"content":{"parts":[{"text":"second"}]}}]}` + "\n\n"
	err := parseStreamInto(strings.NewReader(second), nil, &capture, result, assembler)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected broken stream, got %v", err)
	}
	if got := capture.String(); got != "first second" {
		t.Fatalf("lost partial text: %q", got)
	}
}

func TestRequestCacheBorrowsFinalWireBuffer(t *testing.T) {
	for _, oauth := range []bool{false, true} {
		c := NewWithAPIKey("test")
		if oauth {
			c = NewWithOAuth("token", "project")
		}
		cache := &requestPreparation{}
		contents := []Content{TextContent("user", strings.Repeat("large text", 1024))}
		opts := &Options{Model: "gemini-3.8-flash"}
		for turn := 0; turn < 2; turn++ {
			body, _, err := c.prepareRequest(contents, nil, opts, cache)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range cache.items {
				raw := item.(json.RawMessage)
				offset := bytes.Index(body, raw)
				if offset < 0 || &raw[0] != &body[offset] {
					t.Fatal("cache retained a separate message buffer")
				}
			}
			contents = append(contents, TextContent("model", "next"))
		}
	}
}
