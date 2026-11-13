package pages

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

// Exercise the shipped cascade with unsupported color functions, as on older
// desktop WebKit. Checking class names alone misses transparent active fills.
func TestBrowserFunctional_ChangesSelectionLegacyColors(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	legacyCSS := strings.NewReplacer("oklch(", "unsupported-oklch(", "color-mix(", "unsupported-color-mix(")
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint("legacy=", legacy), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, ".css") {
					recorder := httptest.NewRecorder()
					if static.ServeAsset(recorder, r) {
						w.Header().Set("Content-Type", "text/css")
						css := recorder.Body.String()
						if legacy {
							css = legacyCSS.Replace(css)
						}
						fmt.Fprint(w, css)
						return
					}
				}
				if r.URL.Path != "/" {
					w.WriteHeader(204)
					return
				}
				var page bytes.Buffer
				if err := layout.Base("Selection", nil, "").Render(r.Context(), &page); err != nil {
					t.Error(err)
					return
				}
				// Isolate styling from unrelated shell initialization; retain every inline
				// style and both shipped stylesheets in their production cascade order.
				html := regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>`).ReplaceAllString(page.String(), "")
				fixture := `<div class="join"><button id="diff-btn-inline" class="btn btn-sm join-item btn-active">Inline</button><button id="diff-btn-split" class="btn btn-sm join-item">Split</button></div><div id="changes-split-settings"><div class="join"><button class="btn btn-sm join-item btn-active" data-changes-mode="pr">Pull request</button><button class="btn btn-sm join-item" data-changes-mode="local">Local merge</button></div></div>`
				html = strings.Replace(html, "</body>", fixture+"</body>", 1)
				if legacy {
					html = legacyCSS.Replace(html)
				}
				w.Header().Set("Content-Type", "text/html")
				fmt.Fprint(w, html)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "changes-selection", func(b *composerFocusCDP) {
				b.waitFor("fixture ready", `String(document.readyState === 'complete' && !!document.querySelector('#diff-btn-inline'))`, "true")
				for _, theme := range []string{"dark", "light", "custom"} {
					b.evaluate(fmt.Sprintf(`(function(){var root=document.documentElement;root.dataset.theme=%q;root.dataset.colorTheme='openvibely-'+%q;root.removeAttribute('data-ov-theme-layer');root.style.cssText='';if(%q==='custom'){root.dataset.ovThemeLayer='custom';root.style.cssText='--ov-button-bg:#2468ac;--ov-button-fg:#ffffff;--ov-surface-raised:#eeeeee;--ov-text:#111111;--ov-border:#888888;--ov-hover-bg:#dddddd';}return 'ok';})()`, theme, theme, theme))
					for _, pair := range [][2]string{{"#diff-btn-inline", "#diff-btn-split"}, {`[data-changes-mode="pr"]`, `[data-changes-mode="local"]`}} {
						for turn := 0; turn < 2; turn++ {
							selected, other := pair[turn], pair[1-turn]
							result := b.evaluate(fmt.Sprintf(`(function(){var selected=document.querySelector(%q),other=document.querySelector(%q);selected.classList.add('btn-active');other.classList.remove('btn-active');var a=getComputedStyle(selected),b=getComputedStyle(other);return String(a.backgroundColor!==b.backgroundColor && a.backgroundColor!=='rgba(0, 0, 0, 0)' && a.color!==a.backgroundColor);})()`, selected, other))
							if result != "true" {
								t.Fatalf("%s selected %s has no visible highlight: %s", theme, selected, result)
							}
						}
					}
				}
			})
		})
	}
}
