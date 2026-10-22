package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_KanbanSelectedCardsUseThemeTextBorder(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var base bytes.Buffer
	if err := layout.Base("Kanban selection", []models.Project{{ID: "p1", Name: "A"}}, "p1").Render(context.Background(), &base); err != nil {
		t.Fatal(err)
	}
	tasks := []models.Task{}
	for i, title := range []string{"One", "Two", "Three", "Four"} {
		tasks = append(tasks, models.Task{ID: "t" + string(rune('a'+i)), Title: title, Prompt: "Description text here", Status: models.StatusCompleted, Category: models.CategoryBacklog})
	}
	var board bytes.Buffer
	if err := components.KanbanColumn(tasks, "p1", models.CategoryBacklog, "", "", nil, nil).Render(context.Background(), &board); err != nil {
		t.Fatal(err)
	}
	html := base.String()
	end := strings.LastIndex(html, "</body>")
	page := html[:end] + `<style>*,*::before{transition:none!important}</style><div id="kanban-selection-fixture" style="position:fixed;left:0;top:0;width:520px;height:700px;z-index:999999;padding:20px" class="bg-base-100"><div id="kanban-board">` + board.String() + `</div></div><span id="probe-color"></span>` + html[end:]
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "kanban-selection", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(!!(window.openVibelyThemes && document.querySelector('#kanban-selection-fixture .card')))`, "true")
		for _, theme := range []string{"openvibely-light", "github-light", "openvibely-dark"} {
			b.evaluate(`window.openVibelyThemes.apply('` + theme + `'); 'ok'`)
			b.waitFor("applied "+theme, `document.documentElement.getAttribute('data-color-theme')`, theme)
			b.evaluate(`if (window.kanbanClearSelection) window.kanbanClearSelection(); var ins=document.querySelectorAll('#kanban-selection-fixture [data-kanban-checkbox] input'); [0,1,2].forEach(function(i){ ins[i].checked=true; window.kanbanToggleCard(ins[i]); }); 'ok'`)
			want := "oklch(var(--bc))"
			if theme == "github-light" {
				want = "var(--ov-text)"
			}
			b.waitFor(theme+" selected border uses text color", `(function(){var c=document.querySelector('#kanban-selection-fixture .task-selected'), p=document.getElementById('probe-color'); p.style.color='`+want+`'; var s=getComputedStyle(c), w=getComputedStyle(p).color; return s.borderTopColor===w && s.outlineStyle==='none' && s.backgroundImage==='none' ? 'ok' : JSON.stringify([s.borderTopColor,w,s.outlineStyle,s.backgroundImage]);})()`, "ok")
			b.waitFor(theme+" unselected card shows delete instead of checkbox", `(function(){var un=document.querySelector('#kanban-selection-fixture [data-task-id="td"].card'); var box=un.querySelector('[data-kanban-checkbox]'), x=un.querySelector('[aria-label="Delete task"]'); return getComputedStyle(box).display==='none' && getComputedStyle(x).display!=='none' ? 'ok' : JSON.stringify([getComputedStyle(box).display, getComputedStyle(x).display]);})()`, "ok")
			b.waitFor(theme+" checkbox centered with kebab", `(function(){var c=document.querySelector('#kanban-selection-fixture .task-selected'); var a=c.querySelector('[data-kanban-checkbox] input').getBoundingClientRect(), k=c.querySelector('[data-kanban-menu-trigger]').getBoundingClientRect(); var d=Math.abs((a.top+a.height/2)-(k.top+k.height/2)); return d<=0.5 ? 'ok' : 'off by '+d;})()`, "ok")
			b.waitFor(theme+" unselected card keeps normal border", `(function(){var sel=document.querySelector('#kanban-selection-fixture .task-selected'), un=document.querySelector('#kanban-selection-fixture [data-task-id="td"].card'); return un && !un.classList.contains('task-selected') && getComputedStyle(un).borderTopColor !== getComputedStyle(sel).borderTopColor ? 'ok' : 'same';})()`, "ok")
		}
	})
}
