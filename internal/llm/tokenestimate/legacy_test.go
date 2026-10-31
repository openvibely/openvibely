package tokenestimate

import (
	"encoding/json"
	"testing"
)

func TestLegacyTranscriptEstimateEquivalence(t *testing.T) {
	for _, raw := range []string{
		`[{"role":"user","content":"<hello>"}]`,
		` [ {"role":"user","content":"ignored", "image_content":[{"type":"image_url", "image_url":{"url":"data:image/png;base64,abcd"}}]} ] `,
		`[{"content":[{"type":"tool_result","content":[{"type":"image","source":{"data":"abcd"}}]}]}]`,
		`[null]`, `[]`, `null`, `{"messages":[]}`, `[{"content":"lower","CONTENT":"UPPER"}]`,
	} {
		encoded := []byte(raw)
		var messages []map[string]json.RawMessage
		if json.Unmarshal(encoded, &messages) == nil && messages != nil {
			for _, m := range messages {
				if v := m["image_content"]; len(v) > 0 {
					m["content"] = v
					delete(m, "image_content")
				}
			}
			encoded, _ = json.Marshal(map[string]any{"messages": messages})
		}
		for _, kind := range []string{"image", "image_url"} {
			want, err := FromMessagesJSON(encoded, kind, 2000)
			got, e := FromLegacyTranscript([]byte(raw), kind, 2000)
			if (err == nil) != (e == nil) || err == nil && got != want {
				t.Fatalf("%s %s: got %d want %d %v/%v", kind, raw, got, want, e, err)
			}
		}
	}
}

func BenchmarkLegacyRecoveryImages(b *testing.B) {
	var text []byte
	for i := 0; i < 1<<19; i++ {
		text = append(text, 'x')
	}
	raw, _ := json.Marshal([]map[string]any{{"role": "tool", "content": string(text)}, {"role": "user", "image_content": []any{map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + string(text)}}}}})
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			var messages []map[string]json.RawMessage
			_ = json.Unmarshal(raw, &messages)
			for _, m := range messages {
				if v := m["image_content"]; len(v) > 0 {
					m["content"] = v
					delete(m, "image_content")
				}
			}
			encoded, _ := json.Marshal(map[string]any{"messages": messages})
			if _, err := FromMessagesJSON(encoded, "image_url", 2000); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("borrowed", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := FromLegacyTranscript(raw, "image_url", 2000); err != nil {
				b.Fatal(err)
			}
		}
	})
}
