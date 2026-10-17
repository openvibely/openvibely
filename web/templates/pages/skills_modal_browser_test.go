package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_SkillEditModalCloseDoesNotLeaveCardFocusOutline(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	projectID := "project-skill-modal-focus"
	skill := SkillCard{
		Handle:      "focus_skill",
		Name:        "Focus Skill",
		Description: "A skill opened from its card.",
		Scope:       "project",
		Source:      "project",
		Content:     "Skill body",
		Enabled:     true,
	}

	var base bytes.Buffer
	if err := layout.Base("Skill modal focus", nil, projectID).Render(context.Background(), &base); err != nil {
		t.Fatalf("render skill modal base: %v", err)
	}
	var fragment bytes.Buffer
	if err := SkillsContentForProjectPage([]SkillCard{skill}, true, projectID, false).Render(context.Background(), &fragment); err != nil {
		t.Fatalf("render skill modal fragment: %v", err)
	}
	page := strings.Replace(base.String(), "</body>", fragment.String()+"</body>", 1)
	for _, external := range []string{
		static.URL("app.css"),
		static.URL("app-utilities.css"),
		static.URL("vendor/htmx.min.js"),
		static.URL("vendor/idiomorph-ext.min.js"),
		static.URL("vendor/marked.min.js"),
		static.URL("vendor/highlight.min.js"),
		"wails://wails/runtime.js",
	} {
		replacement := "/empty.js"
		if strings.HasSuffix(external, ".css") {
			replacement = "/empty.css"
		}
		page = strings.ReplaceAll(page, external, replacement)
	}
	page = strings.Replace(page, "</head>", `<style>
		.card { display: block; width: 460px; min-height: 96px; margin: 24px; border: 1px solid rgb(61, 66, 77); border-radius: 12px; background: white; }
		.card-body { padding: 16px; }
		.hidden { display: none !important; }
		dialog:not([open]) { display: none; }
		.card:focus { outline: 2px solid transparent; outline-offset: 2px; }
		.card:focus-visible { outline: 2px solid currentColor; outline-offset: 2px; }
		.modal-box { background: white; padding: 24px; }
	</style></head>`, 1)

	detail, err := json.Marshal(map[string]any{
		"handle":      skill.Handle,
		"scope":       skill.Scope,
		"name":        skill.Name,
		"description": skill.Description,
		"content":     skill.Content,
		"enabled":     skill.Enabled,
		"always_use":  skill.AlwaysUse,
		"files":       []string{},
	})
	if err != nil {
		t.Fatalf("marshal skill detail: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == fmt.Sprintf("/skills/%s/details", skill.Handle) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(detail)
			return
		}
		if r.URL.Path == "/empty.css" {
			w.Header().Set("Content-Type", "text/css; charset=utf-8")
			return
		}
		if r.URL.Path == "/empty.js" {
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()

	runComposerFocusCDP(t, chrome, server.URL, "skill-modal-focus", func(browser *composerFocusCDP) {
		cardSelector := `[data-skill-handle="focus_skill"]`
		browser.waitFor("skill card", fmt.Sprintf(`document.querySelector(%q) ? 'ready' : 'waiting'`, cardSelector), "ready")
		browser.click(cardSelector + ` .card-body > .pr-12`)
		browser.waitFor("skill edit modal", `document.getElementById('skill_modal') && document.getElementById('skill_modal').open ? 'open' : 'closed'`, "open")
		browser.click(`#skill_modal .ov-modal-close`)
		browser.waitFor("skill edit modal close", `document.getElementById('skill_modal') && !document.getElementById('skill_modal').open ? 'closed' : 'open'`, "closed")
		browser.waitFor("skill card focus release", fmt.Sprintf(`document.activeElement !== document.querySelector(%q) ? 'released' : 'focused'`, cardSelector), "released")

		state := browser.evaluate(fmt.Sprintf(`(function(){
			var card = document.querySelector(%q);
			var style = getComputedStyle(card);
			return [document.activeElement === card, card.matches(':focus-visible'), style.outlineStyle, style.outlineWidth].join('|');
		})()`, cardSelector))
		parts := strings.Split(state, "|")
		if len(parts) != 4 || parts[0] != "false" || parts[2] != "none" {
			t.Fatalf("skill card retained modal-close focus outline: %q", state)
		}
	})
}
