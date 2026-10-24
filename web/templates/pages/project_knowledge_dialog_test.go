package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	templateui "github.com/openvibely/openvibely/web/templates"
	"github.com/openvibely/openvibely/web/templates/layout"
)

func TestBrowserFunctional_ProjectKnowledgeDialog(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<!doctype html><html><body><dialog open><form>")
		if err := ProjectSettingsTabs("test").Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `<section id="test-general" data-project-panel="general"><input name="name" required>`)
		if err := ProjectDefaultsFields("test", &models.Project{}, nil, 4).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, "</section>")
		if err := ProjectKnowledgePanel("test", models.SkillMemoryPermissions{ReadSkills: true, WriteSkills: true, AllowGlobalSkillWrites: true}).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, "</form></dialog>")
		if err := templateui.Tooltips().Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, "</body></html>")
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "knowledge-dialog", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(document.readyState === 'complete')`, "true")
		for _, expression := range []string{
			`document.querySelector('[data-project-tab="knowledge"]').click(); String(document.getElementById('test-general').hidden && !document.getElementById('test-knowledge').hidden)`,
			`String(document.querySelectorAll('[data-model-help] svg.h-4.w-4').length === 5)`,
			`document.querySelector('[aria-label="About Write"]').click(); String(document.getElementById('ov-shared-tooltip').matches(':popover-open') && document.getElementById('ov-shared-tooltip').textContent.includes('Ordinary chat cannot write'))`,
			`document.dispatchEvent(new KeyboardEvent('keydown', {key: 'Escape', bubbles: true})); String(!document.getElementById('ov-shared-tooltip').matches(':popover-open'))`,
			`document.querySelector('[name="write_skills"]').click(); String(document.querySelector('[data-global-skill-switch]').disabled && !document.querySelector('[data-global-skill-switch]').checked && new FormData(document.querySelector('form')).get('allow_global_skill_writes') === 'off')`,
			`document.querySelector('[name="write_skills"]').click(); String(!document.querySelector('[data-global-skill-switch]').disabled && !document.querySelector('[data-global-skill-switch]').checked)`,
			`document.querySelector('[data-global-skill-switch]').click(); document.querySelector('form').reset(); String(new FormData(document.querySelector('form')).get('allow_global_skill_writes') === 'on')`,
			`document.querySelector('form').reportValidity(); String(!document.getElementById('test-general').hidden)`,
			`document.querySelector('[data-project-tab="knowledge"]').click(); document.querySelector('[name="max_workers"]').value = '5'; document.querySelector('[name="max_workers"]').reportValidity(); String(!document.getElementById('test-general').hidden)`,
			`document.querySelector('[name="max_workers"]').value = '2'; document.querySelector('[data-project-tab="knowledge"]').click(); String(new FormData(document.querySelector('form')).get('max_workers') === '2')`,
		} {
			if got := b.evaluate(expression); got != "true" {
				t.Fatalf("%s returned %s", expression, got)
			}
		}
	})
}

func TestBrowserFunctional_ProjectDialogStableTabHeight(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, mode := range []string{"new", "edit"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprintf(w, `<!doctype html><html><head><meta name="viewport" content="width=device-width, initial-scale=1"><link rel="stylesheet" href="%s"><link rel="stylesheet" href="%s"><style>*,*::before,*::after { animation: none !important; transition: none !important; }</style></head><body>`, static.URL("app.css"), static.URL("app-utilities.css"))
				dialog := NewProjectDialog(nil, true, 4)
				if mode == "edit" {
					dialog = EditProjectDialog(&models.Project{ID: "test", Name: "Test project", RepoPath: "/tmp/project", IsDefault: true}, nil, true, 4, models.ProjectRepoPathHealth{})
				}
				if err := dialog.Render(r.Context(), w); err != nil {
					t.Error(err)
				}
				fmt.Fprint(w, `<input id="standard-modal-input" class="input input-bordered" aria-label="Standard input" style="position:fixed;visibility:hidden"></body></html>`)
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "project-tab-height-"+mode, func(b *composerFocusCDP) {
				b.waitFor("dialog ready", `String(document.readyState === 'complete' && document.querySelector('dialog').open)`, "true")
				for _, viewport := range []struct{ width, height int }{{1728, 1080}, {1280, 900}, {1280, 640}, {390, 640}} {
					b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": viewport.width, "height": viewport.height, "deviceScaleFactor": 1, "mobile": false}, nil)
					result := b.evaluate(`(() => {
					 const box = document.querySelector('.modal-box');
					 const footer = box.querySelector('.modal-action');
					 const content = box.querySelector('[data-project-tab-content]');
					 const before = box.getBoundingClientRect();
					 const footerBefore = footer.getBoundingClientRect();
					 if (footerBefore.bottom > innerHeight || before.top < 0) return 'modal actions outside viewport ' + JSON.stringify({box:before.toJSON(),footer:footerBefore.toJSON(),content:content.getBoundingClientRect().toJSON(),scroll:content.scrollHeight});
					 for (const tab of ['knowledge', 'general', 'knowledge', 'general']) {
					   box.querySelector('[data-project-tab="' + tab + '"]').click();
					   if (tab === 'general') {
					     const model = box.querySelector('[name="default_agent_config_id"]');
					     const workers = box.querySelector('[name="max_workers"]');
					     if (model.closest('[data-project-panel]').dataset.projectPanel !== 'general' || workers.closest('[data-project-panel]').dataset.projectPanel !== 'general') return 'defaults outside general';
					     const m = model.getBoundingClientRect(), w = workers.getBoundingClientRect();
					     if (innerWidth > 640 ? Math.abs(m.top - w.top) > 1 || w.left <= m.left : w.top <= m.top) return 'incorrect field layout';
					     for (const field of box.querySelectorAll('[data-project-panel="general"] input:not([type="hidden"]):not([type="checkbox"]), [data-project-panel="general"] select')) {
					       const bounds = field.getBoundingClientRect();
					       if (bounds.height && Math.abs(bounds.height - w.height) > 1) return 'inconsistent control heights';
					     }
					     if (Math.abs(w.height - document.getElementById('standard-modal-input').getBoundingClientRect().height) > 1) return 'controls differ from standard modal height';
					     if (content.scrollWidth > content.clientWidth) return 'controls overflow';
					     if (innerWidth > 640 && innerHeight >= 900 && (content.scrollHeight > content.clientHeight || Math.max(m.bottom, w.bottom) > content.getBoundingClientRect().bottom)) return 'defaults require desktop scrolling';
					   }
					   if (box.querySelector('thead th').textContent !== 'Resource') return 'missing resource heading';
					   const after = box.getBoundingClientRect();
					   if (Math.abs(after.height - before.height) > 1 || Math.abs(after.top - before.top) > 1 || Math.abs(footer.getBoundingClientRect().top - footerBefore.top) > 1) return 'modal moved on ' + tab;
					 }
					 content.scrollTop = content.scrollHeight;
					 if (content.scrollHeight > content.clientHeight && content.scrollTop === 0) return 'content cannot scroll';
					 return 'stable';
					})()`)
					if result != "stable" {
						t.Fatalf("%dx%d: %s", viewport.width, viewport.height, result)
					}
				}
			})
		})
	}
}

func TestBrowserFunctional_ProjectRepoRecovery(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		fmt.Fprintf(w, `<!doctype html><html><head><link rel="stylesheet" href="%s"><link rel="stylesheet" href="%s"></head><body>`, static.URL("app.css"), static.URL("app-utilities.css"))
		if err := EditProjectDialog(&models.Project{ID: "recover-test", Name: "Test", RepoURL: "https://github.com/acme/widgets", RepoPath: "/missing", IsDefault: true}, nil, true, 4, models.ProjectRepoPathHealth{Status: models.RepoPathHealthMissing}).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, "</body></html>")
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "project-recovery", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(!!document.querySelector('[data-recover-repo]'))`, "true")
		b.evaluate(`window.recoveryRequests=[]; window.fetch=(url,options)=>{recoveryRequests.push([url,options.method]);return new Promise(resolve=>window.finishRecovery=resolve)};document.querySelector('[data-recover-repo]').click();document.querySelector('[data-recover-repo]').click();'ok'`)
		b.waitFor("single in-flight request", `String(recoveryRequests.length===1 && recoveryRequests[0][0]==='/projects/recover-test/reclone' && recoveryRequests[0][1]==='POST' && document.querySelector('[data-recover-repo]').disabled)`, "true")
		b.evaluate(`finishRecovery({ok:false,json:async()=>({error:'Check GitHub access'})});'ok'`)
		b.waitFor("retry after error", `String(!document.querySelector('[data-recover-repo]').disabled && document.querySelector('[data-recovery-feedback]').textContent==='Check GitHub access' && !document.querySelector('[data-recovery-feedback]').classList.contains('hidden'))`, "true")
		b.evaluate(`document.querySelector('[data-recover-repo]').click();finishRecovery({ok:true,json:async()=>({status:'healthy'})});'ok'`)
		b.waitFor("healthy after recovery", `String(document.querySelector('[data-repo-path-health]').dataset.repoPathHealth==='healthy' && !document.querySelector('[data-recover-repo]') && document.querySelector('[name="repo_url"]').value==='https://github.com/acme/widgets')`, "true")
	})
}

func TestBrowserFunctional_ProjectRepositorySourceVisibility(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, mode := range []string{"new", "edit", "edit-remote", "edit-legacy-local"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/" {
					if err := layout.Base("Projects", nil, "default").Render(r.Context(), w); err != nil {
						t.Error(err)
					}
					return
				}
				if r.URL.Path != "/dialog" {
					w.WriteHeader(http.StatusNoContent)
					return
				}
				dialog := NewProjectDialog(nil, true, 4)
				if mode != "new" {
					project := &models.Project{ID: "test", Name: "Test", RepoPath: "/tmp/project", IsDefault: true}
					if mode == "edit-remote" {
						project.RepoURL = "https://github.com/acme/widgets"
					}
					dialog = EditProjectDialog(project, nil, mode != "edit-legacy-local", 4, models.ProjectRepoPathHealth{})
				}
				if err := dialog.Render(r.Context(), w); err != nil {
					t.Error(err)
				}

			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "repo-source-"+mode, func(b *composerFocusCDP) {
				b.waitFor("shell ready", `String(document.readyState==='complete' && !!window.htmx && !!document.getElementById('project-settings-container'))`, "true")
				b.evaluate(`document.body.addEventListener('htmx:beforeSwap', function(e) {
 const parsed = new DOMParser().parseFromString(e.detail.serverResponse, 'text/html');
 const remote = parsed.querySelector('[name="repo_source"]').value === 'github';
 window.initialRepoVisibility = parsed.querySelector('[id$="-local-fields"]').hidden === remote && parsed.querySelector('[id$="-github-fields"]').hidden === !remote;
 }, {once:true});htmx.ajax('GET','/dialog',{target:'#project-settings-container',swap:'innerHTML'});'ok'`)
				b.waitFor("ready", `String(document.readyState==='complete' && !!document.querySelector('dialog[open]'))`, "true")
				b.waitFor("server-rendered source visibility", `String(window.initialRepoVisibility)`, "true")
				check := `(() => {
     const source=document.querySelector('[name="repo_source"]');
     const remote=source.value==='github';
     const local=document.querySelector('[id$="-local-fields"]');
     const github=document.querySelector('[id$="-github-fields"]');
     const localVisible=local.getBoundingClientRect().height>0;
     const githubVisible=github.getBoundingClientRect().height>0;
     const url=document.querySelector('[name="repo_url"]');
     return String(localVisible===!remote && githubVisible===remote && url.required===remote);
    })()`
				if got := b.evaluate(check); got != "true" {
					t.Fatalf("initial repository fields visibility: %s", got)
				}
				for _, source := range []string{"github", "local"} {
					b.evaluate(fmt.Sprintf(`var source=document.querySelector('[name="repo_source"]');source.value=%q;source.dispatchEvent(new Event('change'));'ok'`, source))
					if got := b.evaluate(check); got != "true" {
						t.Fatalf("%s fields visibility: %s", source, got)
					}
				}
			})
		})
	}
}
