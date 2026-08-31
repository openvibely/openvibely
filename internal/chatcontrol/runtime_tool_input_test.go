package chatcontrol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDecodeRuntimeToolInputTreatsBlankAsEmptyObject(t *testing.T) {
	var got struct {
		Name string `json:"name"`
	}
	if err := DecodeRuntimeToolInput(json.RawMessage(" \n\t "), &got); err != nil {
		t.Fatalf("DecodeRuntimeToolInput blank: %v", err)
	}
	if got.Name != "" {
		t.Fatalf("blank input should decode as empty object, got %+v", got)
	}
}

func TestDecodeRuntimeToolInputDecodesAndWrapsInvalidJSON(t *testing.T) {
	var got struct {
		Name string `json:"name"`
	}
	if err := DecodeRuntimeToolInput(json.RawMessage(`{"name":"Ada"}`), &got); err != nil {
		t.Fatalf("DecodeRuntimeToolInput valid JSON: %v", err)
	}
	if got.Name != "Ada" {
		t.Fatalf("Name = %q, want Ada", got.Name)
	}

	err := DecodeRuntimeToolInput(json.RawMessage(`{`), &got)
	if err == nil || !strings.Contains(err.Error(), "invalid tool input JSON") {
		t.Fatalf("expected wrapped invalid JSON error, got %v", err)
	}
}
