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
				// The sidebar can render before the scripts that initialize toast helpers.
				b.waitFor("sidebar and toast helpers ready", `String(document.readyState === 'complete' && !!document.getElementById('sidebar') && typeof window.showToast === 'function' && typeof window.syncToastContainerHost === 'function')`, "true")
				b.evaluate(`window.railUpdate = {current_version:'1', distribution:'binary', staged:true, state:'available', release:{metadata:{version:'2'}, apply_supported:true}}; openVibelyHandleSystemUpdateSnapshot(railUpdate); 'ok'`)
				b.waitFor("update accessible label", `document.getElementById('alerts-nav-link').getAttribute('aria-label')`, "Alerts · Update available")
				b.evaluate(`document.querySelector('[data-system-update-dismiss]').click(); 'ok'`)
				b.waitFor("dismissal preserves indicator", `document.getElementById('alerts-nav-link').getAttribute('data-system-update-available')`, "true")
				b.evaluate(`document.dispatchEvent(new Event('openvibely:shell-frame-change')); 'ok'`)
				b.waitFor("rail tooltip preserves update state", `document.getElementById('alerts-nav-link').getAttribute('data-tip')`, "Alerts · Update available")
				b.waitFor("rail update dot visible", `String(document.querySelector('.sidebar-update-dot').getBoundingClientRect().width)`, "7")
				b.evaluate(`openVibelyHandleSystemUpdateSnapshot({...railUpdate, state:'waiting_for_idle'}); 'ok'`)
				b.waitFor("accepted update clears indicator", `document.getElementById('alerts-nav-link').getAttribute('data-system-update-available')`, "false")
				b.waitFor("accepted update restores tooltip", `document.getElementById('alerts-nav-link').getAttribute('data-tip')`, "Alerts")
				b.evaluate(`openVibelyHandleSystemUpdateSnapshot({...railUpdate, state:'failed'}); 'ok'`)
				b.waitFor("retry restores indicator", `document.getElementById('alerts-nav-link').getAttribute('data-system-update-available')`, "true")
				b.evaluate(`openVibelyHandleSystemUpdateSnapshot(null); 'ok'`)
				b.evaluate(`showToast('Title bar overlap regression', 'completed', '', {sticky:true}); 'ok'`)
				for _, width := range []int{800, 500, 1200} {
					b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 800, "deviceScaleFactor": 1, "mobile": false}, nil)
					wantMobile := !desktop && width < 1024
					gap := 16
					if width <= 640 {
						gap = 8
					}
					toastTop := gap
					if !wantMobile {
						if desktop {
							toastTop += 54
						} else {
							toastTop += 46
						}
					}
					for _, modal := range []bool{false, true} {
						if modal {
							b.evaluate(`var toastDialog=document.createElement('dialog'); toastDialog.className='modal'; document.body.appendChild(toastDialog); toastDialog.showModal(); syncToastContainerHost(); 'ok'`)
						}
						b.waitFor("toast below visible title bar", `String(Math.round(document.querySelector('.toast-notification').getBoundingClientRect().top))`, fmt.Sprint(toastTop))
						if modal {
							b.evaluate(`toastDialog.close(); syncToastContainerHost(); toastDialog.remove(); 'ok'`)
						}
					}
					b.waitFor("mobile navbar visibility", `String(getComputedStyle(document.querySelector('.drawer-content > .navbar')).display !== 'none')`, fmt.Sprint(wantMobile))
					b.waitFor("sidebar visibility", `String(document.getElementById('sidebar').getBoundingClientRect().right > 0)`, fmt.Sprint(!wantMobile))
					b.waitFor("accessible label follows layout", `String(document.getElementById('alerts-nav-link').hasAttribute('aria-label'))`, fmt.Sprint(!wantMobile))
					b.evaluate(`openVibelyHandleSystemUpdateSnapshot(railUpdate); 'ok'`)
					b.waitFor("update snapshot respects layout", `String(document.getElementById('alerts-nav-link').getAttribute('aria-label'))`, map[bool]string{true: "null", false: "Alerts · Update available"}[wantMobile])
					if desktop {
						b.waitFor("compact rail", `String(Math.round(document.getElementById('sidebar').getBoundingClientRect().width))`, "44")
						b.waitFor("rail remains in layout", `String(Math.round(document.querySelector('.drawer-content').getBoundingClientRect().left))`, "64")
						b.evaluate(`document.getElementById('task-list-toggle').click(); 'ok'`)
						b.waitFor("task list collapsed", `String(document.documentElement.classList.contains('task-list-collapsed') && !document.getElementById('task-list-panel').getClientRects().length)`, "true")
						b.evaluate(`document.getElementById('task-list-toggle').click(); 'ok'`)
						b.waitFor("task list restored", `String(!document.documentElement.classList.contains('task-list-collapsed') && !!document.getElementById('task-list-panel').getClientRects().length)`, "true")
					}
				}
			})
		})
	}
}
