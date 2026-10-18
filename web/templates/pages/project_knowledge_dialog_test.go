package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	templateui "github.com/openvibely/openvibely/web/templates"
)

func TestBrowserProjectKnowledgeDialog(t *testing.T) {
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

func TestBrowserProjectDialogStableTabHeight(t *testing.T) {
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
				fmt.Fprint(w, "</body></html>")
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL, "project-tab-height-"+mode, func(b *composerFocusCDP) {
				b.waitFor("dialog ready", `String(document.readyState === 'complete' && document.querySelector('dialog').open)`, "true")
				for _, viewport := range []struct{ width, height int }{{1280, 900}, {390, 640}} {
					b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": viewport.width, "height": viewport.height, "deviceScaleFactor": 1, "mobile": false}, nil)
					result := b.evaluate(`(() => {
					 const box = document.querySelector('.modal-box');
					 const footer = box.querySelector('.modal-action');
					 const content = box.querySelector('[data-project-tab-content]');
					 const before = box.getBoundingClientRect();
					 const footerBefore = footer.getBoundingClientRect();
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
					     if (content.scrollWidth > content.clientWidth) return 'controls overflow';
					     if (innerWidth > 640 && content.scrollHeight > content.clientHeight) return 'general still needs desktop scrolling';
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
