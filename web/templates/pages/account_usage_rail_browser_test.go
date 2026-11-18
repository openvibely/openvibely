package pages

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/themes"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_AccountUsageRail(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var mu sync.Mutex
	var requests []string
	var failed, denied, renamed bool
	initialLoading := true
	resetGate := make(chan struct{})
	var releaseReset sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/analytics/usage":
			mu.Lock()
			fail, deny := failed, denied
			personalKey := "personal"
			if renamed {
				personalKey = "personal-renamed"
			}
			mu.Unlock()
			if deny {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if fail && r.URL.Query().Get("projection") != "accounts" && r.URL.Query().Get("projection") != "snapshots" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if r.URL.Query().Get("projection") == "accounts" {
				json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]string{{"key": "anthropic", "provider": "anthropic", "identity": "Work"}, {"key": "work", "provider": "openai", "identity": "Work"}, {"key": personalKey, "stable_key": "personal-stable", "provider": "openai", "identity": "Personal"}}})
				return
			}
			if r.URL.Query().Get("projection") == "snapshots" {
				mu.Lock()
				loading := initialLoading
				mu.Unlock()
				snapshots := map[string]any{}
				for _, key := range []string{"personal", "work", "anthropic"} {
					limits := []map[string]any{{"limit_key": "seven_day", "label": "Weekly limit", "window_minutes": 10080, "used_percent": 85}}
					if key != "personal" {
						limits = append(limits, map[string]any{"limit_key": "five_hour", "label": "5-hour limit", "window_minutes": 300, "used_percent": 0})
					}
					snapshot := map[string]any{"refreshing": loading, "resets": map[string]any{"available_count": 1, "credits": []map[string]any{{"id": "grant", "title": "Provider grant", "status": "available", "usable": true}}}}
					if !loading {
						snapshot["account_limits"] = []map[string]any{{"limits": limits, "updated_at": "2026-11-16T10:00:00Z"}}
					}
					if fail {
						snapshot["error"] = "Usage unavailable"
						// A malformed provider grant list must not break the remaining popup.
						snapshot["resets"] = map[string]any{"credits": map[string]any{}}
					}
					if key == "personal" {
						snapshots[personalKey] = snapshot
					} else {
						snapshots[key] = snapshot
					}
				}
				json.NewEncoder(w).Encode(map[string]any{"snapshots": snapshots})
				return
			}
			key := r.URL.Query().Get("account_key")
			limits := []map[string]any{{"limit_key": "seven_day", "label": "Weekly limit", "window_minutes": 10080, "used_percent": 85}}
			if key != "personal" {
				limits = append(limits, map[string]any{"limit_key": "five_hour", "label": "5-hour limit", "window_minutes": 300, "used_percent": 0})
			}
			json.NewEncoder(w).Encode(map[string]any{"account_limits": []map[string]any{{"provider": "openai", "limits": limits, "updated_at": "2026-11-16T10:00:00Z"}}})
		case "/api/analytics/usage/resets":
			json.NewEncoder(w).Encode(map[string]any{"available_count": 1, "credits": []map[string]any{{"id": "grant", "title": "Provider grant", "status": "available", "remaining_uses": 1, "usable": true, "clears": []string{"seven_day"}}}})
		case "/api/analytics/usage/resets/consume":
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			requests = append(requests, body["idempotency_key"])
			count := len(requests)
			mu.Unlock()
			if count == 1 {
				<-resetGate
				json.NewEncoder(w).Encode(map[string]any{"outcome": "in_progress", "retry_same_request": true})
			} else {
				json.NewEncoder(w).Encode(map[string]any{"outcome": "reset", "retry_same_request": false})
			}
		case "/":
			w.Header().Set("Content-Type", "text/html")
			var rendered bytes.Buffer
			if err := Chat([]models.Project{{ID: "project", Name: "Project"}}, "project", nil, nil, nil, nil, nil, false, false, 50).Render(r.Context(), &rendered); err != nil {
				t.Error(err)
			}
			w.Write([]byte(strings.Replace(rendered.String(), "  // Independent of project navigation;", "  window.openVibelyLive.accountUsage = handlers => { window.testUsageHandlers = handlers; return {close(){}} };\n  // Independent of project navigation;", 1)))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	defer releaseReset.Do(func() { close(resetGate) })
	runComposerFocusCDP(t, chrome, server.URL, "account-rail", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("initial inventory fetched", `String(window.openVibelyAccountRail?.accounts.length===3&&!openVibelyAccountRail.loading)`, "true")
		b.waitFor("no meters before first result", `String(document.querySelectorAll('.rail-account').length)`, "0")
		mu.Lock()
		initialLoading = false
		mu.Unlock()
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('accountUsageUpdated')); 'shared results'`)
		b.waitFor("sorted account rings", `JSON.stringify([...document.querySelectorAll('.rail-account')].map(b=>[b.dataset.key,b.querySelectorAll('circle').length]))`, `[["personal",1],["work",2],["anthropic",2]]`)
		b.waitFor("weekly ring shows 15 percent remaining", `(()=>{const svg=document.querySelector('[data-key="personal"] svg');return String(Math.round(100*svg.querySelector('path[stroke]').getTotalLength()/svg.querySelector('circle').getTotalLength()))})()`, "15")
		b.waitFor("standard footer spacing", `String(Math.round(document.querySelector('#sidebar [data-nav-base="/themes"]').getBoundingClientRect().top-document.querySelector('.rail-account:last-child').getBoundingClientRect().bottom))`, "2")
		b.evaluate(`document.querySelector('[data-key="personal"]').click(); 'opened'`)
		b.waitFor("provider reset", `String(document.querySelector('#rail-account-popup button[data-action="reset-grant"]')!==null)`, "true")
		b.waitFor("weekly-only popup", `String(document.getElementById('rail-account-popup').querySelectorAll('[role="meter"]').length)`, "1")
		// HTTP LAN origins do not expose randomUUID, but getRandomValues remains available.
		b.evaluate(`Object.defineProperty(crypto, 'randomUUID', {value: undefined, configurable: true}); 'HTTP crypto capabilities'`)
		b.evaluate(`document.querySelector('[data-action="reset-grant"]').click(); 'reset'`)
		b.waitFor("reset in flight", `String(openVibelyAccountRail.busy)`, "true")
		b.clickMenuBackdrop(`[data-key="work"]`)
		b.evaluate(`document.querySelector('[data-key="work"]').click(); 'open another account'`)
		b.waitFor("click opens another account", `String(openVibelyAccountRail.selected==='work'&&document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.waitFor("other account disabled during reset", `String(document.querySelector('[data-action="refresh"]').disabled)`, "true")
		releaseReset.Do(func() { close(resetGate) })
		b.waitFor("other account enabled after reset", `String(!openVibelyAccountRail.busy&&!document.querySelector('[data-action="refresh"]').disabled&&!document.querySelector('[data-action="reset-grant"]').disabled)`, "true")
		mu.Lock()
		renamed = true
		mu.Unlock()
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('accountUsageUpdated')); 'model changed'`)
		b.waitFor("renamed account", `String(!!document.querySelector('[data-key="personal-renamed"]'))`, "true")
		b.waitFor("one shared attempts map", `String(openVibelyAccountRail.attempts===window.openVibelyResetAttempts)`, "true")
		b.clickMenuBackdrop(`[data-key="personal-renamed"]`)
		b.evaluate(`document.querySelector('[data-key="personal-renamed"]').click(); 'return to reset account'`)
		b.waitFor("retry retained", `String(openVibelyAccountRail.attempts.size===1)`, "true")
		b.waitFor("reset enabled", `String(!document.querySelector('[data-action="reset-grant"]').disabled)`, "true")
		b.evaluate(`document.querySelector('[data-action="reset-grant"]').click(); 'retry'`)
		b.waitFor("reset success", `String(document.getElementById('rail-account-popup').textContent.includes('Usage reset.'))`, "true")
		mu.Lock()
		if len(requests) != 2 || requests[0] == "" || requests[0] != requests[1] {
			t.Errorf("retry did not preserve idempotency key: %v", requests)
		}
		mu.Unlock()
		b.waitFor("definite result clears attempt", `String(window.openVibelyResetAttempts.size)`, "0")
		mu.Lock()
		renamed = false
		mu.Unlock()
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('accountUsageUpdated'));'restore fixture'`)
		b.waitFor("restored inventory", `String(!!document.querySelector('[data-key="personal"]'))`, "true")
		b.evaluate(`document.querySelector('[data-key="personal"]').click();'reopen'`)

		b.evaluate(`window.usageNotifications=0;document.body.addEventListener('accountUsageUpdated',()=>window.usageNotifications++);window.testUsageHandlers.listeners.account_usage_updated();'global notification'`)
		b.waitFor("global usage notification", `String(window.usageNotifications)`, "1")

		// Even failure before the request is sent must release the busy state.
		b.waitFor("successful reset finished", `String(!openVibelyAccountRail.busy)`, "true")
		b.evaluate(`window.savedGetRandomValues=crypto.getRandomValues; crypto.getRandomValues=()=>{throw new Error('random source unavailable')}; document.querySelector('[data-action="reset-grant"]').click(); 'generation failure'`)
		b.waitFor("generation failure releases controls", `String(!openVibelyAccountRail.busy&&!document.querySelector('[data-action="reset-grant"]').disabled&&!document.querySelector('[data-action="refresh"]').disabled)`, "true")
		b.evaluate(`crypto.getRandomValues=window.savedGetRandomValues; delete window.savedGetRandomValues; 'restored'`)
		mu.Lock()
		if len(requests) != 2 {
			t.Errorf("generation failure unexpectedly sent a reset request: %v", requests)
		}
		mu.Unlock()
		b.clickMenuBackdrop(`#rail-more`)
		b.waitFor("click off dismisses only the meter popup", `String(document.getElementById('rail-account-popup').hidden && document.getElementById('rail-overflow').hidden)`, "true")
		b.click(`#rail-more`)
		b.waitFor("overflow destinations", `[...document.querySelectorAll('#rail-overflow .sidebar-text')].map(n=>n.textContent.trim()).join(',')`, "Analytics,Grades,Reflection,Pulse")
		b.waitFor("exclusive popup", `String(!document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.body.click(); 'dismissed'`)
		b.waitFor("outside dismiss", `String(!document.getElementById('rail-overflow').matches(':popover-open'))`, "true")
		// Exercise real theme application and every surface/layout combination. The
		// popup must use the shared menu surface and both ring colors must resolve.
		for _, theme := range themes.Catalog() {
			encoded, _ := json.Marshal(theme.ID)
			b.evaluate(`openVibelyThemes.apply(` + string(encoded) + `); 'theme'`)
			b.waitFor("theme-aware meters "+theme.ID, `(()=>{
    const ring=document.querySelector('.rail-account circle'), popup=document.getElementById('rail-account-popup');
    const probe=document.createElement('div');probe.className='ov-menu';document.body.append(probe);
    for(const surface of ['default','layered'])for(const layout of ['floating','unified']){
     document.documentElement.dataset.surfaceStyle=surface;document.documentElement.dataset.layout=layout;
     if(getComputedStyle(popup).backgroundColor!==getComputedStyle(probe).backgroundColor){probe.remove();return 'surface mismatch'}
     const stroke=getComputedStyle(ring).stroke;
     if(!stroke||stroke==='none'||stroke.includes('NaN')){probe.remove();return 'unresolved stroke'}
     if(getComputedStyle(ring).strokeWidth!=='1.67px'){probe.remove();return 'stroke weight'}
    }
    probe.remove();return 'ok';
   })()`, "ok")
		}
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); document.querySelector('[data-key="personal"]').dispatchEvent(new PointerEvent('pointerover',{bubbles:true,pointerType:'mouse'})); 'hover'`)
		b.waitFor("hover does not open popup", `String(!document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.querySelector('[data-key="personal"]').click(); 'open'`)
		b.waitFor("click opens popup", `String(document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.getElementById('rail-account-popup').dispatchEvent(new PointerEvent('pointerleave')); 'leave'`)
		b.waitFor("click popup stays open", `String(document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.querySelector('[data-key="personal"]').click(); 'toggle'`)
		b.waitFor("second click closes popup", `String(!document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		// Provider failure marks usage stale without replacing the provider identity.
		b.evaluate(`window.openVibelyDisplay.set("meters", "letters"); "letters selected"`)
		mu.Lock()
		failed = true
		mu.Unlock()
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('accountUsageChanged')); 'failed refresh'`)
		b.waitFor("failed meters", `String([...document.querySelectorAll('.rail-account')].every(b=>b.getAttribute('aria-label').includes('stale')&&b.querySelector('text').textContent===(b.dataset.key==='anthropic'?'A':'O')))`, "true")
		b.evaluate(`document.querySelector('[data-key="personal"]').click(); 'error popup'`)
		b.waitFor("failure retains marked stale bars", `String(document.getElementById('rail-account-popup').querySelectorAll('[role="meter"]').length)`, "1")
		b.waitFor("malformed grants retain refresh control", `String(document.querySelector('#rail-account-popup [data-action="refresh"]')!==null)`, "true")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); 'escape'`)
		b.waitFor("Escape restores account focus", `String(document.activeElement.dataset.key==='personal'&&!document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		mu.Lock()
		denied = true
		mu.Unlock()
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('accountUsageChanged')); 'denied refresh'`)
		b.waitFor("permissions hide accounts", `String(document.querySelectorAll('.rail-account').length)`, "0")

	})
}
