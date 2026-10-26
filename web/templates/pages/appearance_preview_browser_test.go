package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_AppearanceColorStepPreviews(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := Themes([]models.Project{{ID: "project", Name: "Project"}}, "project", nil).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "appearance-step", func(b *composerFocusCDP) {
		b.waitFor("appearance ready", `String(!!window.selectOpenVibelyDisplay)`, "true")
		b.evaluate(`var sample=document.createElement('div'); sample.innerHTML='<div class="chat-input-container"></div><div class="chat-bubble-user-msg"></div><div class="chat-bubble-assistant-msg"></div>';document.getElementById('main-content').appendChild(sample); openVibelyDisplay.set('bubbles','on'); 'ready'`)
		for _, width := range []int{1440, 390} {
			b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
			expected := "true"
			if width < 1024 {
				expected = "false"
			}
			b.waitFor("responsive layout", `String(document.documentElement.hasAttribute('data-shell-frame'))`, expected)
			for _, theme := range []string{"openvibely-dark", "openvibely-layered", "openvibely-light"} {
				b.evaluate(`openVibelyThemes.apply('` + theme + `'); 'applied'`)
				b.waitFor("independent previews match real surfaces", `(()=>{
      const color=e=>getComputedStyle(e).backgroundColor;
      const off=document.querySelector('[data-kind="step"][data-variant="off"] .dp-composer');
      const on=document.querySelector('[data-kind="step"][data-variant="on"] .dp-composer');
      selectOpenVibelyDisplay('step','off'); const offColor=color(off), onColor=color(on);
      if(offColor===onColor)return 'options identical';
      if(!Array.from(sample.children).every(e=>color(e)===offColor))return 'off differs from real surface';
      selectOpenVibelyDisplay('step','on');
      if(color(off)!==offColor || color(on)!==onColor)return 'previews changed with selection';
      if(!Array.from(sample.children).every(e=>color(e)===onColor))return 'on differs from real surface';
      return 'pass';
    })()`, "pass")
			}
		}
	})
}

func TestBrowserFunctional_AppearanceBoardColumnsAtEveryWidth(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := Themes([]models.Project{{ID: "project", Name: "Project"}}, "project", nil).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "appearance-columns", func(b *composerFocusCDP) {
		b.waitFor("appearance ready", `String(!!window.selectOpenVibelyDisplay)`, "true")
		b.evaluate(`var boardSample=document.createElement('div'); boardSample.className='kanban-column'; boardSample.innerHTML='<div>Active</div><div class="card bg-base-100">Task</div>'; document.getElementById('main-content').appendChild(boardSample); 'ready'`)
		for _, width := range []int{1440, 390} {
			b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
			expected := "true"
			if width < 1024 {
				expected = "false"
			}
			b.waitFor("responsive layout", `String(document.documentElement.hasAttribute('data-shell-frame'))`, expected)
			if width == 1440 {
				for _, layout := range []string{"floating", "unified"} {
					b.evaluate(`selectOpenVibelyDisplay('tabs','attached'); selectOpenVibelyDisplay('layout','` + layout + `');document.documentElement.classList.add('task-list-collapsed'); 'collapsed'`)
					b.waitFor("collapsed attached tab spacing", `getComputedStyle(document.getElementById('desktop-project-tabs')).paddingLeft`, "10px")
					b.evaluate(`document.documentElement.classList.remove('task-list-collapsed'); 'expanded'`)
				}
			}

			for _, theme := range []string{"openvibely-dark", "openvibely-layered", "openvibely-light"} {
				b.evaluate(`openVibelyThemes.apply('` + theme + `'); 'applied'`)
				for _, step := range []string{"off", "on"} {
					b.evaluate(`selectOpenVibelyDisplay('step','` + step + `'); 'selected'`)
					for _, mode := range []string{"tint", "filled", "inset", "tintstrip"} {
						b.evaluate(`selectOpenVibelyDisplay('columns','` + mode + `'); 'selected'`)
						b.waitFor("board matches preview: "+theme+"/"+mode, `(()=>{
      const preview=document.querySelector('.board-columns-preview[data-preview="`+mode+`"]');
      const color=e=>getComputedStyle(e).backgroundColor;
      if(color(boardSample)!==color(preview.querySelector('.bcp-col')))return 'column differs';
      if(color(boardSample.querySelector('.card'))!==color(preview.querySelector('.bcp-card')))return 'card differs';
      if('`+mode+`'==='tintstrip' && color(boardSample.firstElementChild)!==color(preview.querySelector('.bcp-head')))return 'header differs';
      return 'pass';
     })()`, "pass")
					}
				}
			}
		}
	})
}
