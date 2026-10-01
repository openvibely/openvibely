package layout

import (
	"bytes"
	"context"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/web/static"
)

// Pages must load their CSS and JS from the OpenVibely server, never from a CDN, so they
// work offline (desktop) and never stall on a slow third-party host.
func TestBaseLoadsOnlyFirstPartyAssets(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", nil, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("render base: %v", err)
	}
	html := buf.String()
	for _, host := range []string{"cdn.tailwindcss.com", "cdn.jsdelivr.net", "unpkg.com"} {
		if strings.Contains(html, host) {
			t.Errorf("base layout still references %s", host)
		}
	}
	external := regexp.MustCompile(`<(?:script|link)[^>]+(?:src|href)="https?://`)
	if m := external.FindString(html); m != "" {
		t.Errorf("base layout loads an external asset: %s", m)
	}
	for _, asset := range []string{"app.css", "app-utilities.css", "vendor/htmx.min.js", "vendor/idiomorph-ext.min.js", "vendor/marked.min.js", "vendor/mermaid.min.js", "vendor/highlight.min.js", "vendor/highlight-github-dark.min.css", "vendor/chart.umd.min.js"} {
		if !strings.Contains(html, static.URL(asset)) {
			t.Errorf("base layout does not reference %s", static.URL(asset))
		}
	}
	// Utilities must come after the inline <style> blocks so they override them, as the
	// Tailwind CDN script's generated styles did.
	utilities := strings.Index(html, static.URL("app-utilities.css"))
	head := strings.Index(html, "</head>")
	lastStyle := strings.LastIndex(html[:head], "</style>")
	if utilities < lastStyle || utilities > head {
		t.Error("app-utilities.css must be the last stylesheet in <head>")
	}
}

func TestPageTemplatesDoNotLoadCDNScripts(t *testing.T) {
	files, err := os.ReadDir("../pages")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".templ") {
			continue
		}
		data, err := os.ReadFile("../pages/" + f.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, host := range []string{"cdn.tailwindcss.com", "cdn.jsdelivr.net", "unpkg.com"} {
			if strings.Contains(string(data), host) {
				t.Errorf("%s references %s; vendor the library in web/assets instead", f.Name(), host)
			}
		}
	}
}
