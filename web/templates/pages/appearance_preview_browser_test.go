package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/themes"
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

func TestBrowserFunctional_AppearanceSurfaceTreatment(t *testing.T) {
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
	runComposerFocusCDP(t, chrome, server.URL, "appearance-surfaces", func(b *composerFocusCDP) {
		b.waitFor("appearance ready", `String(!!window.selectOpenVibelyDisplay)`, "true")
		b.evaluate(`var surfaceSample=document.createElement('div');surfaceSample.innerHTML='<div class="card bg-base-100 border border-base-300">Card</div><button class="btn btn-primary">Run</button><span class="text-success">Success</span><span class="text-error">Error</span><span class="text-warning">Warning</span><span class="text-info">Info</span><code class="hljs"><span class="hljs-keyword">const</span></code>';document.getElementById('main-content').appendChild(surfaceSample); 'ready'`)
		b.evaluate(`openVibelyThemes.apply('openvibely-dark');selectOpenVibelyDisplay('surfaces','default');var codeProbe=document.createElement('div');codeProbe.className='chat-bubble-assistant-msg';codeProbe.innerHTML='<pre>Code</pre>';document.getElementById('main-content').appendChild(codeProbe); 'ready'`)
		b.waitFor("Dark default keeps page, frame and code contrast", `(()=>{const color=e=>getComputedStyle(e).backgroundColor;return JSON.stringify([color(document.getElementById('desktop-project-titlebar')),color(document.getElementById('main-content')),color(document.getElementById('task-list-panel')),color(codeProbe.firstElementChild)]);})()`, `["rgb(25, 30, 36)","rgb(21, 25, 30)","rgb(21, 25, 30)","rgb(17, 20, 24)"]`)
		for _, width := range []int{1440, 390} {
			b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
			expected := "true"
			if width < 1024 {
				expected = "false"
			}
			b.waitFor("responsive layout", `String(document.documentElement.hasAttribute('data-shell-frame'))`, expected)
			for _, definition := range themes.Catalog() {
				theme := definition.ID
				if theme == "openvibely-layered" {
					continue
				}
				b.evaluate(`openVibelyThemes.apply('` + theme + `'); 'applied'`)
				b.waitFor("surfaces preserve theme colors: "+theme, `(()=>{
      const color=e=>getComputedStyle(e).backgroundColor;
      const colors=()=>Array.from(surfaceSample.children).slice(1).map(e=>{const c=getComputedStyle(e);return [c.color,c.backgroundColor,e.querySelector('.hljs-keyword')&&getComputedStyle(e.querySelector('.hljs-keyword')).color]});
      const previews=()=>['default','layered'].map(v=>{const p=document.querySelector('[data-kind="surfaces"][data-variant="'+v+'"]');return [color(p),color(p.querySelector('.dp-page')),color(p.querySelector('.dp-list')),color(p.querySelector('.dp-surface-card'))]});
      selectOpenVibelyDisplay('surfaces','default'); const before=JSON.stringify(colors()), previewBefore=JSON.stringify(previews());
      if(document.documentElement.hasAttribute('data-theme-surfaces')) {
       const p=document.querySelector('[data-kind="surfaces"][data-variant="default"]');
       if(color(p.querySelector('.dp-page'))!==color(document.getElementById('main-content')))return 'default page preview differs';
       if(color(p.querySelector('.dp-surface-card'))!==color(surfaceSample.firstElementChild))return 'default card preview differs';
      }
      const frameBefore=getComputedStyle(document.documentElement).getPropertyValue('--shell-chrome');
      selectOpenVibelyDisplay('surfaces','layered');
      if(JSON.stringify(colors())!==before)return 'theme colors changed';
      if(JSON.stringify(previews())!==previewBefore)return 'alternatives changed';
      if(getComputedStyle(document.documentElement).getPropertyValue('--shell-chrome')===frameBefore && document.documentElement.dataset.theme!=='light')return 'frame did not change';
      const p=document.querySelector('[data-kind="surfaces"][data-variant="layered"]');
      if(color(p.querySelector('.dp-page'))!==color(document.getElementById('main-content')))return 'page preview differs';
      if(color(p.querySelector('.dp-surface-card'))!==color(surfaceSample.firstElementChild))return 'card preview differs';
      if(document.documentElement.hasAttribute('data-shell-frame') && document.documentElement.hasAttribute('data-theme-surfaces')) {
       if(color(p)!==color(document.getElementById('desktop-project-titlebar')))return 'frame preview differs';
       if(color(p.querySelector('.dp-list'))!==color(document.getElementById('task-list-panel')))return 'list preview differs';
       if(color(document.getElementById('desktop-project-titlebar'))===color(document.getElementById('main-content')))return 'active tab blends into frame';
      }
      return 'pass';
    })()`, "pass")
			}
		}
		b.evaluate(`localStorage.removeItem('openvibely.surfaceStyle');localStorage.setItem('openvibely.colorTheme','openvibely-layered');localStorage.setItem('theme','openvibely-layered'); 'saved legacy'`)
		b.call("Page.navigate", map[string]any{"url": server.URL + "/?migration=1"}, nil)
		b.waitFor("legacy theme migrated", `(()=>{if(!window.openVibelyThemes || !window.openVibelyDisplay)return 'loading';const state=[location.search,document.readyState,typeof window.selectOpenVibelyDisplay,openVibelyThemes.currentID(),openVibelyDisplay.get('surfaces'),localStorage.getItem('openvibely.colorTheme'),localStorage.getItem('openvibely.surfaceStyle')];return new URLSearchParams(state[0]).get('migration')==='1' && state[1]==='complete' && state[2]==='function' && state[3]==='openvibely-dark' && state[4]==='layered' && state[5]==='openvibely-dark' ? 'true' : JSON.stringify(state);})()`, "true")
		b.evaluate(`selectOpenVibelyDisplay('surfaces','default'); 'saved default'`)
		b.call("Page.navigate", map[string]any{"url": server.URL + "/?migration=2"}, nil)
		b.waitFor("surface preference persists", `String(new URLSearchParams(location.search).get('migration')==='2' && !!window.openVibelyDisplay && openVibelyDisplay.get('surfaces')==='default')`, "true")
	})
}
