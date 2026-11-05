package handler

import (
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
