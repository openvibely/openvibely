package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_NeutralControlFocus(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	task := &models.Task{ID: "audit", ProjectID: "audit-project", Title: "Audit task", Category: models.CategoryBacklog}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/breadcrumb-selectors/tasks" {
			_ = components.BreadcrumbSelectorResults("Task", "audit", []models.BreadcrumbSelectorItem{{ID: "audit", Name: "Audit task", URL: "/tasks/audit"}}, false, false).Render(r.Context(), w)
			return
		}
		if r.URL.Path != "/tasks/audit" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = TaskDetailPage(nil, task, nil, nil, nil, nil, nil, nil, "details", nil).Render(r.Context(), w)
		_ = components.TaskChangeActivity("audit").Render(r.Context(), w)
		_ = kanbanSearch().Render(r.Context(), w)
		_ = automationBreadcrumb("audit-project", "automation", "Audit automation", "History", false).Render(r.Context(), w)
		fmt.Fprint(w, `<div id="audit-examples"><button class="btn btn-primary">Save</button><button class="btn btn-primary btn-outline">Run</button><a class="btn btn-primary" href="#apply">Apply</a><a class="link link-primary" href="#release">Release notes</a><input type="checkbox" class="checkbox checkbox-primary"/><input type="checkbox" class="toggle toggle-primary"/><button class="btn ov-task-result-start-btn">Start</button><span class="chat-markdown"><a href="#chat-link">Chat link</a></span><a class="ov-task-result-link" href="#task-link">Task link</a><button class="chat-input-option-btn">Choice</button><details class="stream-thinking"><summary>Thinking</summary></details><button class="stream-tool-output-toggle">Tool output</button></div>`)
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/tasks/audit", "neutral-focus", func(b *composerFocusCDP) {
		b.waitFor("toolbar", `String(document.readyState==='complete' && !!document.querySelector('#task-details-opener'))`, "true")
		b.evaluate(`document.querySelector('[data-kanban-search]').dataset.open='true';document.querySelector('#kanban-search-input').value='audit';document.querySelector('#task-change-activity').hidden=false;document.querySelector('[data-change-files]').textContent='1 file';'ready'`)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyDown", "key": "Tab", "code": "Tab"}, nil)
		b.call("Input.dispatchKeyEvent", map[string]any{"type": "keyUp", "key": "Tab", "code": "Tab"}, nil)
		for _, theme := range []string{"dark", "light"} {
			b.evaluate(`document.documentElement.setAttribute('data-theme','` + theme + `');'ready'`)
			b.waitFor("primary actions retain colors with neutral focus in "+theme, `(function(){
 const reference=document.createElement('span');reference.style.outline='1px solid oklch(var(--bc) / .5)';document.body.append(reference);
 const neutral=getComputedStyle(reference).outlineColor;reference.remove();
 for(const e of document.querySelectorAll('#audit-examples .btn-primary,#audit-examples .link-primary')) {
  e.style.transition='none';e.blur();const before=getComputedStyle(e);const colors=[before.color,before.backgroundColor,before.borderTopColor].join('|');
  e.focus();const after=getComputedStyle(e);
  if(!e.matches(':focus-visible')||after.outlineStyle!=='solid'||after.outlineWidth!=='1px'||after.outlineColor!==neutral)return e.className+' '+after.outline;
  if(colors!==[after.color,after.backgroundColor,after.borderTopColor].join('|'))return 'focus changed primary colors';
 }
 return '';
})()`, "")
			b.waitFor("neutral checkbox/toggle focus preserves checked colors in "+theme, `(function(){
 const reference=document.createElement('span');reference.style.outline='1px solid oklch(var(--bc) / .5)';document.body.append(reference);
 const neutral=getComputedStyle(reference).outlineColor;reference.remove();
 for(const e of document.querySelectorAll('#audit-examples input,#audit-examples .ov-task-result-start-btn')) {
  for(const checked of [false,true]) {
   e.blur();e.checked=checked;const before=getComputedStyle(e);const colors=[before.backgroundColor,before.borderTopColor].join('|');
   e.focus();const after=getComputedStyle(e);
   if(!e.matches(':focus-visible'))return 'missing keyboard focus';
   if(after.outlineStyle!=='none'&&(after.outlineWidth!=='1px'||after.outlineColor!==neutral))return e.className+' '+after.outline;
   if(e.matches('input')&&colors!==[after.backgroundColor,after.borderTopColor].join('|'))return 'focus changed checked colors';
  }
 }
 return '';
})()`, "")
			for _, workspace := range []string{"thread", "changes"} {
				if workspace == "changes" {
					b.evaluate(`window.taskWorkspace.openChanges();'changes'`)
				} else {
					b.evaluate(`window.taskWorkspace.openThread();'thread'`)
				}
				b.waitFor("thin neutral keyboard feedback in "+theme+" "+workspace, `(function(){
     const reference=document.createElement('span');reference.style.outline='1px solid oklch(var(--bc) / .5)';document.body.append(reference);
     const neutral=getComputedStyle(reference).outlineColor;reference.remove();
     const selectors=['#task-resource-selector-button','#task-details-opener','#task-change-activity button','.kanban-search-toggle','.kanban-search-clear','[data-breadcrumb-title]','#audit-examples .chat-input-option-btn','#audit-examples summary','#audit-examples a','#audit-examples .stream-tool-output-toggle','#task-workspace-back'];
     const controls=Array.from(document.querySelectorAll(selectors.join(','))).filter(e=>e.getClientRects().length&&!e.disabled);
     if(controls.length<8)return 'missing controls';
     return controls.map(e=>{e.focus();const s=getComputedStyle(e);if(document.activeElement!==e||!e.matches(':focus-visible'))return e.outerHTML+' not keyboard focused';
      if(e.matches('.kanban-search-toggle,.kanban-search-clear'))return s.boxShadow.includes(neutral)&&s.boxShadow.includes('1px')?'':e.outerHTML+' '+s.boxShadow;
      return s.outlineWidth==='1px'&&s.outlineStyle==='solid'&&s.outlineColor===neutral?'':e.outerHTML+' '+s.outline;
     }).filter(Boolean).join('\n');
    })()`, "")
			}
		}
	})
}
