package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	var failed, denied bool
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
			mu.Unlock()
			if deny {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			if fail && r.URL.Query().Get("projection") != "accounts" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			if r.URL.Query().Get("projection") == "accounts" {
				json.NewEncoder(w).Encode(map[string]any{"accounts": []map[string]string{{"key": "anthropic", "provider": "anthropic", "identity": "Work"}, {"key": "work", "provider": "openai", "identity": "Work"}, {"key": "personal", "provider": "openai", "identity": "Personal"}}})
				return
			}
			key := r.URL.Query().Get("account_key")
			limits := []map[string]any{{"limit_key": "seven_day", "label": "Weekly limit", "window_minutes": 10080, "used_percent": 51}}
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
			if err := Chat([]models.Project{{ID: "project", Name: "Project"}}, "project", nil, nil, nil, nil, nil, false, false, 50).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	defer releaseReset.Do(func() { close(resetGate) })
	runComposerFocusCDP(t, chrome, server.URL, "account-rail", func(b *composerFocusCDP) {
		b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 1000, "deviceScaleFactor": 1, "mobile": false}, nil)
		b.waitFor("sorted account rings", `JSON.stringify([...document.querySelectorAll('.rail-account')].map(b=>[b.dataset.key,b.querySelectorAll('circle').length]))`, `[["personal",2],["work",3],["anthropic",3]]`)
		b.waitFor("standard footer spacing", `String(Math.round(document.querySelector('#sidebar [data-nav-base="/themes"]').getBoundingClientRect().top-document.querySelector('.rail-account:last-child').getBoundingClientRect().bottom))`, "2")
		b.evaluate(`document.querySelector('[data-key="personal"]').click(); 'opened'`)
		b.waitFor("provider reset", `String(document.querySelector('#rail-account-popup button[data-action="reset-grant"]')!==null)`, "true")
		b.waitFor("weekly-only popup", `String(document.getElementById('rail-account-popup').querySelectorAll('[role="meter"]').length)`, "1")
		// HTTP LAN origins do not expose randomUUID, but getRandomValues remains available.
		b.evaluate(`Object.defineProperty(crypto, 'randomUUID', {value: undefined, configurable: true}); 'HTTP crypto capabilities'`)
		b.evaluate(`document.querySelector('[data-action="reset-grant"]').click(); 'reset'`)
		b.waitFor("reset in flight", `String(openVibelyAccountRail.busy)`, "true")
		b.evaluate(`document.querySelector('[data-key="work"]').dispatchEvent(new PointerEvent('pointerover',{bubbles:true,pointerType:'mouse'})); 'hover another account'`)
		b.waitFor("hover opens account", `String(openVibelyAccountRail.selected==='work'&&document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.waitFor("other account disabled during reset", `String(document.querySelector('[data-action="refresh"]').disabled)`, "true")
		releaseReset.Do(func() { close(resetGate) })
		b.waitFor("other account enabled after reset", `String(!openVibelyAccountRail.busy&&!document.querySelector('[data-action="refresh"]').disabled&&!document.querySelector('[data-action="reset-grant"]').disabled)`, "true")
		b.evaluate(`document.querySelector('[data-key="personal"]').click(); 'return to reset account'`)
		b.waitFor("retry retained", `String(openVibelyAccountRail.attempts.size===1)`, "true")
		b.waitFor("reset enabled", `String(!document.querySelector('[data-action="reset-grant"]').disabled)`, "true")
		b.evaluate(`document.querySelector('[data-action="reset-grant"]').click(); 'retry'`)
		b.waitFor("reset success", `String(document.getElementById('rail-account-popup').textContent.includes('Usage reset.'))`, "true")
		mu.Lock()
		if len(requests) != 2 || requests[0] == "" || requests[0] != requests[1] {
			t.Errorf("retry did not preserve idempotency key: %v", requests)
		}
		mu.Unlock()
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
		b.evaluate(`document.getElementById('rail-more').click(); 'overflow'`)
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
		// Hover must not steal focus and must allow crossing the gap into the popup.
		b.evaluate(`document.activeElement.blur(); window.hoverFocus=document.activeElement; document.querySelector('[data-key="personal"]').dispatchEvent(new PointerEvent('pointerover',{bubbles:true,pointerType:'mouse'})); 'hover'`)
		b.waitFor("hover preserves focus", `String(document.activeElement===window.hoverFocus&&document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.getElementById('rail-accounts').dispatchEvent(new PointerEvent('pointerleave')); document.getElementById('rail-account-popup').dispatchEvent(new PointerEvent('pointerenter')); window.hoverChecked=false; setTimeout(()=>window.hoverChecked=true,350); 'cross gap'`)
		b.waitFor("popup stays open on entry", `String(window.hoverChecked&&document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		b.evaluate(`document.getElementById('rail-account-popup').dispatchEvent(new PointerEvent('pointerleave')); 'leave'`)
		b.waitFor("hover popup dismisses on leave", `String(!document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		// Provider failure must not leave stale rings looking like live allowance.
		mu.Lock()
		failed = true
		mu.Unlock()
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('accountUsageChanged')); 'failed refresh'`)
		b.waitFor("failed meters", `String([...document.querySelectorAll('.rail-account')].every(b=>b.getAttribute('aria-label').includes('unavailable')&&b.querySelector('text').textContent==='!'))`, "true")
		b.evaluate(`document.querySelector('[data-key="personal"]').click(); 'error popup'`)
		b.waitFor("failure has no stale bars", `String(document.getElementById('rail-account-popup').querySelectorAll('[role="meter"]').length)`, "0")
		b.evaluate(`document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true})); 'escape'`)
		b.waitFor("Escape restores account focus", `String(document.activeElement.dataset.key==='personal'&&!document.getElementById('rail-account-popup').matches(':popover-open'))`, "true")
		mu.Lock()
		denied = true
		mu.Unlock()
		b.evaluate(`document.body.dispatchEvent(new CustomEvent('accountUsageChanged')); 'denied refresh'`)
		b.waitFor("permissions hide accounts", `String(document.querySelectorAll('.rail-account').length)`, "0")

	})
}
