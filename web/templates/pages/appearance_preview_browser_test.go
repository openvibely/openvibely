package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/themes"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
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
		b.evaluate(`var sample=document.createElement('div'); sample.innerHTML='<div class="chat-input-container"></div><div class="chat-bubble-user-msg"></div><div class="chat-bubble-assistant-msg"><div class="stream-tool"><div class="stream-tool-body"><div class="stream-tool-body-grid"><div class="stream-tool-body-row"><div class="stream-tool-body-label">IN</div><div class="stream-tool-body-content"><pre class="stream-tool-output-text">echo hello</pre></div></div><div class="stream-tool-body-row"><div class="stream-tool-body-label">OUT</div><div class="stream-tool-body-content"><pre class="stream-tool-output-text">hello</pre></div></div></div></div></div></div>';document.getElementById('main-content').appendChild(sample); openVibelyDisplay.set('bubbles','on'); 'ready'`)
		for _, width := range []int{1440, 390} {
			b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": width, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
			expected := "true"
			if width < 1024 {
				expected = "false"
			}
			b.waitFor("responsive layout", `String(document.documentElement.hasAttribute('data-shell-frame'))`, expected)
			for _, definition := range themes.Catalog() {
				theme := definition.ID
				b.evaluate(`openVibelyThemes.apply('` + theme + `'); 'applied'`)
				b.waitFor("independent previews match real surfaces", `(()=>{
      const color=e=>getComputedStyle(e).backgroundColor;
      if(document.documentElement.dataset.theme==='light') {
       for(const mode of ['default','layered']) for(const step of ['off','on']) {
        selectOpenVibelyDisplay('surfaces',mode);selectOpenVibelyDisplay('step',step);
        if('`+theme+`'==='vscode-light-modern') {
         const bubble=sample.querySelector('.chat-bubble-assistant-msg');
         for(const box of bubble.querySelectorAll('pre')) {
          if(color(box)!=='rgb(242, 242, 242)' || color(box)===color(bubble))return 'Light Modern tool box blends into bubble';
         }
        }
        for(const e of sample.querySelectorAll('.stream-tool-body-label,.stream-tool-body-content')) {
         if(getComputedStyle(e).borderTopWidth!=='0px')return 'light tool row has a divider';
        }
        for(const e of sample.querySelectorAll('.stream-tool-body,.stream-tool-body-content')) {
         if(color(e)!=='rgba(0, 0, 0, 0)')return 'tool wrapper paints a different surface: '+color(e);
        }
       }
      }
      for(const mode of ['default','layered']) for(const step of ['off','on']) {
       selectOpenVibelyDisplay('surfaces',mode);selectOpenVibelyDisplay('step',step);
       const menu=document.createElement('div'), tip=document.createElement('div'), card=document.createElement('div');
       menu.className='ov-menu';tip.className='ov-tooltip';card.style.backgroundColor='var(--board-card-bg)';
       document.body.append(menu,tip,card);
       const menuMatches=color(menu)===color(card), tipMatches=color(tip)===color(card);
       const dialog=document.createElement('div');dialog.className='modal-box';document.body.append(dialog);
       menu.classList.add('dropdown-content');dialog.append(menu);
       const dialogMatches=color(menu)===color(card);dialog.remove();
       menu.remove();tip.remove();card.remove();
       if(!menuMatches || !tipMatches || !dialogMatches)return 'menu/tooltip differs from card in '+mode;
       if(document.documentElement.dataset.theme!=='dark' && mode==='default')continue;
       const probe=document.createElement('div');probe.style.color=mode==='default'?'var(--ov-surface-default-border, var(--shell-page))':'var(--ov-surface-layered-border, var(--shell-chrome))';sample.appendChild(probe);
       const expected=getComputedStyle(probe).color;probe.remove();
       for(const e of sample.children)if(getComputedStyle(e).borderTopColor!==expected)return 'conversation border differs in '+mode+': '+getComputedStyle(e).borderTopColor+' expected '+expected;
       if(document.documentElement.dataset.theme==='dark') {
        const composer=sample.querySelector('.chat-input-container'), input=document.createElement('textarea');composer.appendChild(input);
        input.focus();
        const focusedBorder=getComputedStyle(composer).borderTopColor;
        input.blur();input.remove();
        if(focusedBorder!==expected)return 'focused composer border differs in '+mode;
       }
       selectOpenVibelyDisplay('bubbles','off');
       if(getComputedStyle(sample.querySelector('.chat-bubble-assistant-msg')).borderTopColor!=='rgba(0, 0, 0, 0)')return 'disabled bubble border visible';
       selectOpenVibelyDisplay('bubbles','on');
      }
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

			for _, theme := range []string{"openvibely-dark", "openvibely-light", "catppuccin-mocha", "github-light", "vscode-dark-modern"} {
				b.evaluate(`openVibelyThemes.apply('` + theme + `'); 'applied'`)
				for _, step := range []string{"off", "on"} {
					b.evaluate(`selectOpenVibelyDisplay('step','` + step + `'); 'selected'`)
					for _, mode := range []string{"tint", "filled", "inset", "tintstrip"} {
						b.evaluate(`selectOpenVibelyDisplay('columns','` + mode + `'); 'selected'`)
						b.waitFor("board matches preview: "+theme+"/"+mode, `(()=>{
      const preview=document.querySelector('.board-columns-preview[data-preview="`+mode+`"]');
      const color=e=>getComputedStyle(e).backgroundColor;
      const before=color(boardSample);
      const step=openVibelyDisplay.get('step');selectOpenVibelyDisplay('step',step==='on'?'off':'on');
      if(color(boardSample)!==before)return 'conversation step changed board';
      selectOpenVibelyDisplay('step',step);
      if('`+mode+`'==='filled') {
       const probe=document.createElement('div');probe.style.background='var(--shell-chrome)';document.body.appendChild(probe);
       const frame=color(probe);probe.remove();if(color(boardSample)!==frame)return 'filled column does not match frame';
      }
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
		if err := Themes([]models.Project{{ID: "project", Name: "Project"}}, "project", themes.Catalog()).Render(r.Context(), w); err != nil {
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
      const thumb=document.querySelector('[data-theme-id="`+theme+`"] .theme-thumbnail');
      if(thumb) {
       for(const mode of ['default','layered']) {
        selectOpenVibelyDisplay('surfaces',mode);
        const preview=document.querySelector('[data-kind="surfaces"][data-variant="'+mode+'"]');
        if(color(thumb)!==color(preview))return 'theme thumbnail frame differs';
        if(color(thumb.querySelector('.theme-thumbnail-rail'))!==color(preview.querySelector('.dp-list')))return 'theme thumbnail rail differs';
        if(color(thumb.querySelector('.theme-thumbnail-page'))!==color(preview.querySelector('.dp-page')))return 'theme thumbnail page differs';
        if(color(thumb.querySelector('.theme-thumbnail-card'))!==color(preview.querySelector('.dp-surface-card')))return 'theme thumbnail card differs';
       }
       selectOpenVibelyDisplay('surfaces','default');
      }

      if(document.documentElement.hasAttribute('data-theme-surfaces')) {
       const p=document.querySelector('[data-kind="surfaces"][data-variant="default"]');
       if(color(p.querySelector('.dp-page'))!==color(document.getElementById('main-content')))return 'default page preview differs';
       if(color(p.querySelector('.dp-surface-card'))!==color(surfaceSample.firstElementChild))return 'default card preview differs';
      }
      const luminance=e=>{
       const canvas=document.createElement('canvas');canvas.width=canvas.height=1;const ctx=canvas.getContext('2d');
       ctx.fillStyle=color(e);ctx.fillRect(0,0,1,1);
       const rgb=Array.from(ctx.getImageData(0,0,1,1).data).slice(0,3).map(v=>{v/=255;return v<=.04045?v/12.92:Math.pow((v+.055)/1.055,2.4)});
       return rgb[0]*.2126+rgb[1]*.7152+rgb[2]*.0722;
      };
      const layered=document.querySelector('[data-kind="surfaces"][data-variant="layered"]');
      const frameL=luminance(layered), railL=luminance(layered.querySelector('.dp-list')), pageL=luminance(layered.querySelector('.dp-page'));
      const highContrast='`+theme+`'.includes('high-contrast');
      if(highContrast ? !(frameL===railL && railL===pageL) : (document.documentElement.dataset.theme==='light' ? !(frameL<railL && railL<pageL) : !(pageL<railL && railL<frameL)))return 'surface brightness order reversed';
      const frameBefore=getComputedStyle(document.documentElement).getPropertyValue('--shell-chrome');
      selectOpenVibelyDisplay('surfaces','layered');
      if(JSON.stringify(colors())!==before)return 'theme colors changed';
      if(JSON.stringify(previews())!==previewBefore)return 'alternatives changed';
      if(!highContrast && getComputedStyle(document.documentElement).getPropertyValue('--shell-chrome')===frameBefore && document.documentElement.dataset.theme!=='light')return 'frame did not change';
      const p=document.querySelector('[data-kind="surfaces"][data-variant="layered"]');
      if(color(p.querySelector('.dp-page'))!==color(document.getElementById('main-content')))return 'page preview differs';
      if(color(p.querySelector('.dp-surface-card'))!==color(surfaceSample.firstElementChild))return 'card preview differs';
      if(document.documentElement.hasAttribute('data-shell-frame') && document.documentElement.hasAttribute('data-theme-surfaces')) {
       if(color(p)!==color(document.getElementById('desktop-project-titlebar')))return 'frame preview differs';
       if(color(p.querySelector('.dp-list'))!==color(document.getElementById('task-list-panel')))return 'list preview differs';
       if(highContrast) {
        if(getComputedStyle(document.querySelector('.desktop-project-tab[data-selected="true"]')).outlineColor==='rgba(0, 0, 0, 0)')return 'high contrast tab outline lost';
       } else if(color(document.getElementById('desktop-project-titlebar'))===color(document.getElementById('main-content')))return 'active tab blends into frame';
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
		// Ending the test mid-response makes the handler's write fail with a broken pipe.
		b.waitFor("surface preference persists", `String(new URLSearchParams(location.search).get('migration')==='2' && document.readyState==='complete' && !!window.openVibelyDisplay && openVibelyDisplay.get('surfaces')==='default')`, "true")
	})
}

func TestBrowserFunctional_ToolOutputPreference(t *testing.T) {
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
	runComposerFocusCDP(t, chrome, server.URL+"?section=chat", "tool-output-pref", func(b *composerFocusCDP) {
		b.waitFor("preference ready", `String(!!window.selectOpenVibelyDisplay && document.readyState==='complete')`, "true")
		b.waitFor("default compact output", `openVibelyDisplay.get('output')`, "compact")
		b.evaluate(`document.querySelector('[data-display-pref="output"][data-display-option="compact"]').click(); 'selected'`)
		b.waitFor("compact saved and selected", `String(openVibelyDisplay.get('output')==='compact' && localStorage.getItem('openvibely.outputDetail')==='compact' && document.querySelector('[data-display-pref="output"][data-display-option="compact"]').getAttribute('aria-pressed')==='true')`, "true")
		b.call("Page.navigate", map[string]any{"url": server.URL + "?section=chat&reload=1"}, nil)
		b.waitFor("compact survives reload", `String(location.search.includes('reload=1') && document.readyState==='complete' && !!window.selectOpenVibelyDisplay && openVibelyDisplay.get('output')==='compact')`, "true")
		b.evaluate(`selectOpenVibelyDisplay('output','full'); 'full'`)
		b.waitFor("full restored", `String(openVibelyDisplay.get('output')==='full' && localStorage.getItem('openvibely.outputDetail')==='full')`, "true")
		b.call("Page.navigate", map[string]any{"url": server.URL + "?section=chat&reload=2"}, nil)
		b.waitFor("saved full overrides compact default", `String(location.search.includes('reload=2') && document.readyState==='complete' && !!window.selectOpenVibelyDisplay && openVibelyDisplay.get('output')==='full')`, "true")
	})
}

func TestBrowserFunctional_UsageMeterAppearance(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/api/analytics/usage" {
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("projection") == "accounts" {
				w.Write([]byte(`{"accounts":[{"key":"a","provider":"anthropic","identity":"Work"}]}`))
			} else {
				w.Write([]byte(`{"snapshots":{"a":{"account_limits":[{"limits":[{"window_minutes":10080,"used_percent":55},{"window_minutes":300,"used_percent":25}]}]}}}`))
			}
			return
		}
		if r.URL.Path != "/" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := Themes([]models.Project{{ID: "project", Name: "Project"}}, "project", themes.Catalog()).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "usage-meter-appearance", func(b *composerFocusCDP) {
		b.waitFor("meter and previews ready", `String(document.querySelectorAll('[data-meter-preview] svg').length===12&&document.querySelectorAll('.rail-account').length===1)`, "true")
		b.waitFor("default logo rings", `String(document.documentElement.dataset.usageMeterStyle==='logos'&&!!document.querySelector('.rail-account svg path'))`, "true")
		for _, style := range []string{"letters", "logos", "fill", "bars"} {
			b.evaluate(`selectOpenVibelyDisplay('meters','` + style + `'); 'selected'`)
			b.waitFor("saved and rendered meter style", `(()=>{const style='`+style+`';if(localStorage.getItem('openvibely.usageMeterStyle')!==style||document.documentElement.dataset.usageMeterStyle!==style)return 'preference';const svg=document.querySelector('.rail-account svg');if(svg.getBoundingClientRect().width!==28)return 'size';if(style==='letters'&&!svg.querySelector('text'))return 'letter';if(style!=='letters'&&!svg.querySelector('path'))return 'logo';if(style==='fill'&&!svg.querySelector('clipPath'))return 'fill';if(style==='bars'&&svg.querySelectorAll('rect').length!==4)return 'bars';return 'ok'})()`, "ok")
		}
		b.evaluate(`window.meterReloadMarker=true; 'before reload'`)
		b.call("Page.reload", map[string]any{}, nil)
		b.waitFor("style survives reload", `String(!window.meterReloadMarker&&!!window.openVibelyThemes&&window.openVibelyDisplay?.get('meters')==='bars'&&document.querySelector('.rail-account svg')?.querySelectorAll('rect').length===4)`, "true")
		for _, theme := range themes.Catalog() {
			b.evaluate(`openVibelyThemes.apply('` + theme.ID + `'); 'theme'`)
			b.waitFor("theme remaining allowance prominence", `(()=>{const light=document.documentElement.dataset.theme==='light';const svg=document.querySelector('.rail-account svg');const canvas=document.createElement('canvas'),ctx=canvas.getContext('2d');function brightness(token){const rect=document.createElementNS(svg.namespaceURI,'rect');rect.setAttribute('fill','var('+token+')');svg.append(rect);const color=getComputedStyle(rect).fill;rect.remove();ctx.fillStyle=light?'white':'black';ctx.fillRect(0,0,1,1);ctx.fillStyle=color;ctx.fillRect(0,0,1,1);const p=ctx.getImageData(0,0,1,1).data;return p[0]+p[1]+p[2]};const left=brightness('--ov-usage-remaining'),used=brightness('--ov-usage-used');return String(light?left<used:left>used)})()`, "true")
		}
		b.evaluate(`selectOpenVibelyDisplay('meters','letters');'reset'`)
	})
}

func TestBrowserFunctional_ConversationHeaderFade(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		switch r.URL.Path {
		case "/chat":
			if err := Chat([]models.Project{{ID: "project", Name: "Project"}}, "project", nil, nil, nil, nil, nil, false, false, 30).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		case "/task":
			task := &models.Task{ID: "task-1", ProjectID: "project", Title: "Investigate header fade", Status: models.StatusCompleted, Category: models.CategoryCompleted}
			thread := components.TaskThreadView(task, nil, nil, nil, nil, nil, false, 30)
			if err := TaskDetailPage([]models.Project{{ID: "project", Name: "Project"}}, task, nil, nil, nil, nil, nil, nil, "chat", nil, thread).Render(r.Context(), w); err != nil {
				t.Error(err)
			}
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/chat", "header-fade", func(b *composerFocusCDP) {
		b.waitFor("chat page ready", `String(location.pathname==='/chat' && document.readyState==='complete' && !!document.getElementById('chat-messages'))`, "true")
		b.waitFor("soft-edge fade geometry on chat", `(()=>{
			const host = document.getElementById('chat-messages').parentElement;
			const transcript = document.getElementById('chat-messages');
			const header = document.querySelector('#chat-page-root > [data-page-header]');
			return JSON.stringify({
				headerH: Math.round(header.getBoundingClientRect().height),
				afterH: getComputedStyle(host, '::after').height,
				afterRight: getComputedStyle(host, '::after').right,
				afterPtr: getComputedStyle(host, '::after').pointerEvents,
				padTop: getComputedStyle(transcript).paddingTop,
			});
		})()`, `{"headerH":48,"afterH":"26px","afterRight":"-24px","afterPtr":"none","padTop":"24px"}`)

		b.call("Page.navigate", map[string]any{"url": server.URL + "/task"}, nil)
		b.waitFor("task thread ready", `String(location.pathname==='/task' && document.readyState==='complete' && !!document.getElementById('task-thread-messages'))`, "true")
		b.waitFor("soft-edge fade geometry on task thread", `(()=>{
			const host = document.getElementById('task-thread-messages').parentElement;
			const transcript = document.getElementById('task-thread-messages');
			const header = document.getElementById('task-breadcrumb-toolbar');
			return JSON.stringify({
				headerH: Math.round(header.getBoundingClientRect().height),
				afterH: getComputedStyle(host, '::after').height,
				afterRight: getComputedStyle(host, '::after').right,
				padTop: getComputedStyle(transcript).paddingTop,
			});
		})()`, `{"headerH":48,"afterH":"26px","afterRight":"-24px","padTop":"24px"}`)

		b.waitFor("refresh preview matches live transcript fade geometry", `(()=>{
			const host = document.getElementById('task-thread-messages').parentElement;
			const transcript = document.getElementById('task-thread-messages');
			transcript.removeAttribute('data-chat-transcript');
			transcript.setAttribute('data-thread-refresh-preview', 'true');
			const preview = {
				afterH: getComputedStyle(host, '::after').height,
				padTop: getComputedStyle(transcript).paddingTop,
			};
			transcript.removeAttribute('data-thread-refresh-preview');
			transcript.setAttribute('data-chat-transcript', '');
			return JSON.stringify(preview);
		})()`, `{"afterH":"26px","padTop":"24px"}`)

		b.waitFor("curated themes match main-content surface", `(()=>{
			const probe = document.createElement('div');
			probe.style.backgroundColor = 'var(--header-fade-bg)';
			document.body.appendChild(probe);
			const main = document.getElementById('main-content');
			const checks = [];
			for (const theme of ['openvibely-dark', 'openvibely-light', 'tokyo-night', 'rose-pine-dawn']) {
				openVibelyThemes.apply(theme);
				for (const surfaces of ['default', 'layered']) {
					openVibelyDisplay.set('surfaces', surfaces);
					const fadeBg = getComputedStyle(probe).backgroundColor;
					const mainBg = getComputedStyle(main).backgroundColor;
					if (fadeBg !== mainBg) checks.push(theme + '/' + surfaces + ':' + fadeBg + '!=' + mainBg);
				}
			}
			probe.remove();
			openVibelyThemes.apply('openvibely-dark');
			return checks.length === 0 ? 'ok' : checks.join(',');
		})()`, "ok")

		b.evaluate(`document.getElementById('task-details-opener').click(); 'open panel'`)
		b.waitFor("docked panel trims fade scrim to transcript right edge", `(()=>{
			const root = document.getElementById('task-detail-content');
			const host = document.getElementById('task-thread-messages').parentElement;
			return String(root.dataset.inspectorDocked==='true' && getComputedStyle(host, '::after').right==='-12px');
		})()`, "true")

		b.evaluate(`document.getElementById('tab-chat').classList.add('hidden'); 'switch to changes'`)
		b.waitFor("hiding thread tab removes scrim", `getComputedStyle(document.getElementById('task-thread-messages').parentElement, '::after').content`, "none")
	})
}
