package pages

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_DesktopSidebarAtNarrowWidths(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, desktop := range []bool{true, false} {
		t.Run(fmt.Sprint(desktop), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/wails/runtime.js" {
					w.Header().Set("Content-Type", "text/javascript")
					fmt.Fprint(w, `export const Window = {};`)
					return
				}
				if r.URL.Path != "/" {
					w.WriteHeader(204)
					return
				}
				w.Header().Set("Content-Type", "text/html")
				if err := layout.Base("Models", nil, "default").Render(layout.WithDesktopMode(context.Background(), desktop), w); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "narrow-sidebar", func(b *composerFocusCDP) {
				b.waitFor("sidebar rendered", `String(!!document.getElementById("sidebar"))`, "true")
				for _, width := range []int{800, 500, 1200} {
					b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 800, "deviceScaleFactor": 1, "mobile": false}, nil)
					wantMobile := !desktop && width < 1024
					b.waitFor("mobile navbar visibility", `String(getComputedStyle(document.querySelector('.drawer-content > .navbar')).display !== 'none')`, fmt.Sprint(wantMobile))
					b.waitFor("sidebar visibility", `String(document.getElementById('sidebar').getBoundingClientRect().right > 0)`, fmt.Sprint(!wantMobile))
					if desktop {
						b.evaluate(`document.getElementById('sidebar-collapse-btn').click(); 'ok'`)
						b.waitFor("compact sidebar", `String(Math.round(document.getElementById('sidebar').getBoundingClientRect().width))`, "56")
						b.waitFor("sidebar remains in layout", `String(Math.round(document.querySelector('.drawer-content').getBoundingClientRect().left))`, "56")
						b.evaluate(`document.getElementById('sidebar-collapse-btn').click(); 'ok'`)
						b.waitFor("expanded sidebar", `String(Math.round(document.getElementById('sidebar').getBoundingClientRect().width))`, "256")
					}
				}
			})
		})
	}
}
