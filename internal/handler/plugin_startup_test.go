package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginStartupObserverPrecedesScripts(t *testing.T) {
	html := instrumentPluginHTML("<!doctype html><html><head><script>broken()</script></head></html>", `</script><bad>`)
	if !strings.HasPrefix(html, "<!doctype html><script>") {
		t.Fatal("doctype lost or observer inserted too late")
	}
	if strings.Index(html, "page.failed") > strings.Index(html, "broken()") {
		t.Fatal("error observer installed after plugin")
	}
	if strings.Contains(html, `</script><bad>`) {
		t.Fatal("handle not escaped")
	}
	if !strings.Contains(html, "unhandledrejection") {
		t.Fatal("promise failures not observed")
	}
}

func TestPluginStartupBridgeAuthorization(t *testing.T) {
	env := setupPluginPages(t)
	handle := env.openPage(t, env.inA)
	rec, _ := env.bridge(t, env.inA, env.projB.ID, handle, "page.loaded", `{}`)
	if rec.Code == 200 {
		t.Fatal("cross-project report accepted")
	}
	rec, _ = env.bridge(t, env.inB, env.projB.ID, handle, "page.loaded", `{}`)
	if rec.Code == 200 {
		t.Fatal("wrong instance report accepted")
	}
	rec, resp := env.bridge(t, env.inA, env.projA.ID, handle, "page.loaded", `{}`)
	if rec.Code != 200 || !resp.OK {
		t.Fatal(rec.Body.String())
	}
	rec, _ = env.bridge(t, env.inA, env.projA.ID, handle, "page.failed", `{"error":"script crashed"}`)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if env.mgr.StartupStatus(env.inA.ID, env.inA.ContentHash).State != "failed" { // reload joined hash when needed
		in, err := env.mgr.Store().GetInstance(t.Context(), env.inA.ID)
		if err != nil {
			t.Fatal(err)
		}
		if env.mgr.StartupStatus(in.ID, in.ContentHash).State != "failed" {
			t.Fatal("failure missing")
		}
	}
	env.mgr.RetireHandle(handle)
	rec, _ = env.bridge(t, env.inA, env.projA.ID, handle, "page.loaded", `{}`)
	if rec.Code == 200 {
		t.Fatal("retired report accepted")
	}
}

func TestPluginEntryLargerThanPrefixIsStreamedWhole(t *testing.T) {
	env := setupPluginPages(t)
	for name, doctype := range map[string]string{"doctype": "<!DOCTYPE html>", "bare": ""} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			id := "custom.large-" + name
			manifest := `{"schema_version":1,"id":"` + id + `","name":"Large","version":"1.0.0","pages":[{"id":"main","title":"Large","entry":"index.html"}]}`
			body := doctype + "<p>" + strings.Repeat("x", 3*pluginHTMLPrefixBytes) + "</p><!--end-->"
			if err := os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(manifest), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			pkg, err := env.mgr.InstallFromDir(ctx, dir, "admin")
			if err != nil {
				t.Fatal(err)
			}
			in, err := env.mgr.CreateInstance(ctx, env.projA.ID, pkg.ID, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := env.mgr.Enable(ctx, in.ID); err != nil {
				t.Fatal(err)
			}
			handle := env.openPage(t, in)
			rec := env.do(t, http.MethodGet, "/plugin-assets/"+handle+"/index.html", "", nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("entry: %d %s", rec.Code, rec.Body.String())
			}
			got := rec.Body.String()
			if !strings.HasPrefix(got, doctype+"<script>") {
				t.Fatalf("observer not first after doctype: %.80q", got)
			}
			if strings.Count(got, "page.failed") != 1 || !strings.HasSuffix(got, body[len(doctype):]) {
				t.Fatal("entry body not streamed intact")
			}
			if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
				t.Fatalf("content type %q", ct)
			}
		})
	}
}
