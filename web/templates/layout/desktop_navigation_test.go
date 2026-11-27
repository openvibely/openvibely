package layout

import (
	"bytes"
	"runtime"
	"strings"
	"testing"
)

func TestDesktopLoginTransitionScope(t *testing.T) {
	for _, native := range []bool{false, true} {
		var out bytes.Buffer
		if err := Base("Test", nil, "").Render(WithNativeWindow(t.Context(), native), &out); err != nil {
			t.Fatal(err)
		}
		html := out.String()
		wantTransition := native && runtime.GOOS == "darwin"
		if strings.Contains(html, `id="desktop-login-transition"`) != wantTransition {
			t.Fatalf("native=%v: incorrect transition scope", native)
		}
		if strings.Contains(html, "desktop:page-background") || strings.Contains(html, "SetBackgroundColour") {
			t.Fatal("native colour workaround remains")
		}
	}
}
