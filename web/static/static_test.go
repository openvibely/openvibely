package static

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Every asset the layout references must exist, or URL panics at render time.
var layoutAssets = []string{
	"app.css",
	"app-utilities.css",
	"vendor/htmx.min.js",
	"vendor/idiomorph-ext.min.js",
	"vendor/marked.min.js",
	"vendor/highlight.min.js",
	"vendor/highlight-github-dark.min.css",
	"vendor/chart.umd.min.js",
}

func TestURLHashesEveryLayoutAsset(t *testing.T) {
	for _, name := range layoutAssets {
		u := URL(name)
		if !strings.HasPrefix(u, Prefix+name+"?v=") || len(u) != len(Prefix+name+"?v=")+12 {
			t.Errorf("URL(%q) = %q, want %s%s?v=<12-char hash>", name, u, Prefix, name)
		}
	}
	defer func() {
		if recover() == nil {
			t.Error("URL of a missing asset should panic")
		}
	}()
	URL("vendor/missing.js")
}

func TestHandlerServesAssetsWithTypesAndCaching(t *testing.T) {
	cases := []struct {
		url, contentType, cache string
	}{
		{URL("app.css"), "text/css", "immutable"},
		{URL("vendor/htmx.min.js"), "javascript", "immutable"},
		{Prefix + "vendor/htmx.min.js", "javascript", "no-cache"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.url, nil))
		if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
			t.Fatalf("%s: status %d, %d bytes", c.url, rec.Code, rec.Body.Len())
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, c.contentType) {
			t.Errorf("%s: Content-Type %q, want %s", c.url, ct, c.contentType)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, c.cache) {
			t.Errorf("%s: Cache-Control %q, want %s", c.url, cc, c.cache)
		}
	}
	for _, bad := range []string{Prefix, Prefix + "vendor/", Prefix + "nope.js", Prefix + "../static.go"} {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, bad, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("%s: served %d bytes, want an error", bad, rec.Body.Len())
		}
	}
}

func TestServeAssetOnlyHandlesStaticPaths(t *testing.T) {
	rec := httptest.NewRecorder()
	if ServeAsset(rec, httptest.NewRequest(http.MethodGet, "/tasks", nil)) {
		t.Fatal("ServeAsset handled a non-static path")
	}
	if !ServeAsset(rec, httptest.NewRequest(http.MethodGet, URL("app.css"), nil)) || rec.Code != http.StatusOK {
		t.Fatalf("ServeAsset did not serve app.css: %d", rec.Code)
	}
}
