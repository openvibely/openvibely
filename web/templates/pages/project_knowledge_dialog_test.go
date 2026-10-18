package pages

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBrowserProjectKnowledgeDialog(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "<!doctype html><html><body><dialog open><form>")
		if err := ProjectSettingsTabs("test").Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, `<section id="test-general" data-project-panel="general"><input name="name" required></section>`)
		if err := ProjectKnowledgePanel("test", models.SkillMemoryPermissions{ReadSkills: true, WriteSkills: true, AllowGlobalSkillWrites: true}).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
		fmt.Fprint(w, "</form></dialog></body></html>")
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "knowledge-dialog", func(b *composerFocusCDP) {
		b.waitFor("ready", `String(document.readyState === 'complete')`, "true")
		for _, expression := range []string{
			`document.querySelector('[data-project-tab="knowledge"]').click(); String(document.getElementById('test-general').hidden && !document.getElementById('test-knowledge').hidden)`,
			`document.querySelector('[name="write_skills"]').click(); String(document.querySelector('[data-global-skill-switch]').disabled && new FormData(document.querySelector('form')).get('allow_global_skill_writes') === 'on')`,
			`document.querySelector('[name="write_skills"]').click(); String(!document.querySelector('[data-global-skill-switch]').disabled && document.querySelector('[data-global-skill-switch]').checked)`,
			`document.querySelector('[data-global-skill-switch]').click(); document.querySelector('form').reset(); String(new FormData(document.querySelector('form')).get('allow_global_skill_writes') === 'on')`,
			`document.querySelector('form').reportValidity(); String(!document.getElementById('test-general').hidden)`,
		} {
			if got := b.evaluate(expression); got != "true" {
				t.Fatalf("%s returned %s", expression, got)
			}
		}
	})
}
