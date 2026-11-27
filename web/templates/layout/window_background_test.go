package layout

import (
	"bytes"
	"strings"
	"testing"
)

func TestNativeBackgroundBeforeStylesheets(t *testing.T) {
	for _, native := range []bool{false, true} {
		var out bytes.Buffer
		ctx := WithNativeWindow(t.Context(), native)
		if err := Base("Test", nil, "").Render(ctx, &out); err != nil {
			t.Fatal(err)
		}
		html := out.String()
		background := strings.Index(html, "color-scheme: dark;")
		if !native {
			if background >= 0 {
				t.Fatal("browser page includes native background styling")
			}
			continue
		}
		stylesheet := strings.Index(html, `rel="stylesheet"`)
		if background < 0 || stylesheet < 0 || background > stylesheet {
			t.Fatal("native background must be available before external stylesheets")
		}
	}
}
