package handler

import (
	"context"
	"fmt"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPluginAsset_ModuleLoadsInSandbox(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser test")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	env := setupPluginPages(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(`{"schema_version":1,"id":"audit.module","name":"Module","version":"1.0.0","pages":[{"id":"main","title":"Module","entry":"index.html"}]}`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.html"), []byte(`<script>parent.postMessage('classic','*')</script><script type="module" src="./app.js"></script>`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "app.js"), []byte(`import './dependency.js';`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "dependency.js"), []byte(`parent.postMessage('module-ran','*')`), 0600))
	pkg, err := env.mgr.InstallFromDir(t.Context(), dir, "audit")
	require.NoError(t, err)
	in, err := env.mgr.CreateInstance(t.Context(), env.projA.ID, pkg.ID, "")
	require.NoError(t, err)
	require.NoError(t, env.mgr.Enable(t.Context(), in.ID))
	handle := env.openPage(t, in)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/audit" {
			fmt.Fprintf(w, `<html><body><script>window.addEventListener('message',e=>{if(typeof e.data==='string') document.body.dataset.result=(document.body.dataset.result||'')+e.data;});</script><iframe sandbox="allow-scripts allow-forms" src="/plugin-assets/%s/index.html"></iframe></body></html>`, handle)
			return
		}
		env.e.ServeHTTP(w, r)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, chrome, "--headless=new", "--no-sandbox", "--user-data-dir="+t.TempDir(), "--dump-dom", "--virtual-time-budget=2000", server.URL+"/audit").CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), `data-result="classicmodule-ran"`, string(out))
}
