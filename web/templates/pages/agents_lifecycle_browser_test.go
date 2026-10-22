package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_MemoryCuratorHookTogglePreservesContract(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var page bytes.Buffer
	if err := AgentsContent(nil, nil).Render(context.Background(), &page); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(page.Bytes())
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "memory-hook-toggle", func(browser *composerFocusCDP) {
		browser.waitFor("hook renderer", `typeof buildLifecycleHookRow`, "function")
		result := browser.evaluate(`(function() {
 lifecycleProtectedAgent = true;
 lifecycleHooksState = [{id:'recall',when:'route_task',skill_key:'recall_memory',blocking:false,enabled:true,output_contract:'selected_memories'}];
 renderLifecycleHooks();
 const row = document.querySelector('#lifecycle_hooks_list [data-hook-idx]');
 const enabled = row.querySelector('[data-hook-field="enabled"]');
 const contract = row.querySelector('[data-hook-field="output_contract"]');
 if (enabled.disabled || !contract.disabled || contract.value !== 'selected_memories') return 'invalid controls';
 for (const value of [false, true]) {
 enabled.checked = value;
 const saved = collectLifecycleHooksFromDOM()[0];
 if (saved.enabled !== value || saved.output_contract !== 'selected_memories' || saved.skill_key !== 'recall_memory') return 'invalid payload';
 }
 return 'ok';
 })()`)
		if result != "ok" {
			t.Fatalf("Memory Curator toggle: %s", result)
		}
	})
}
