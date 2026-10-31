package tokenestimate

import (
	"encoding/json"
	"testing"
)

func TestContentAdjustmentMatchesEncodedPath(t *testing.T) {
	for _, text := range []string{
		`[{"type":"image_url","image_url":{"url":"data:image/png;base64,abc"}},{"type":"text","text":"<&>世"}]`,
		`[{"type":"tool_result","content":[{"type":"image","source":{"data":"xyz"}}]}]`,
		`[{"Type":"image","type":"text","Content":[],"content":"x"}]`,
		`[{"Type":"image","source":{"data":"abc"}}]`,
		`[{"type":"tool_result","Content":[{"type":"image","source":{}}]}]`,
	} {
		for _, kind := range []string{"image", "image_url"} {
			var content any
			if err := json.Unmarshal([]byte(text), &content); err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(content)
			want := contentImageAdjustmentSized(raw, kind, 2000, true)
			got, err := ContentAdjustment(content, kind, 2000)
			if err != nil || got != want {
				t.Fatalf("%s %s got=%d want=%d err=%v", kind, text, got, want, err)
			}
		}
	}
}
