package pages

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/events"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestBrowserFunctional_SkillDependencyApprovalCard(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	script, err := os.ReadFile("../../assets/files/skill-dependencies.js")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	state := "pending"
	decisions := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/approval.js" {
			w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
			w.Write(script)
			return
		}
		if r.URL.Path == "/skills/dependency-preview" {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{"platform": "linux/amd64", "dependencies": []any{map[string]any{"name": "preview-tool", "version": "2.0", "health_check": []string{"--version"}}}})
			return
		}
		if strings.HasPrefix(r.URL.Path, "/chat/input-requests") {
			mu.Lock()
			defer mu.Unlock()
			if r.Method == http.MethodPost {
				if r.Header.Get("X-OpenVibely-UI") != "chat-input-answer" || r.Header.Get("Origin") == "" {
					http.Error(w, "missing browser headers", 403)
					return
				}
				var input struct {
					Answers []struct {
						Label string `json:"label"`
					} `json:"answers"`
				}
				json.NewDecoder(r.Body).Decode(&input)
				if len(input.Answers) != 1 {
					http.Error(w, "missing answer", 400)
					return
				}
				decisions++
				state = input.Answers[0].Label
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(`{"ok":true}`))
				return
			}
			w.Header().Set("Content-Type", "text/html")
			if state == "pending" {
				raw, _ := json.Marshal(map[string]any{"identity": map[string]any{"skill": "sample"}, "platform": "linux/amd64", "dependencies": []any{map[string]any{"name": "example", "version": "1.2.3", "health_check": []string{"--version"}, "packages": map[string]any{"linux/amd64": map[string]any{"url": "https://example.test/tool.tgz", "sha256": strings.Repeat("a", 64), "executable": "bin/tool"}}}}})
				ChatInputRequestCard(&events.ChatInputRequestEvent{ID: "request", ExecID: "execution", ProjectID: "project", Installation: raw, Questions: []events.ChatInputRequestQuestion{{ID: "install", Question: "Install dependencies?", Options: []events.ChatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}}).Render(r.Context(), w)
			}
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><body><main id="task-thread" data-project-id="project"><div data-execution-pair data-exec-id="execution"></div></main><script>window.showToast=function(a,b,c,options){window.approvalToast=options;};</script><script src="/approval.js"></script></body></html>`))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/?project_id=project#chat-input-request-request", "dependency-approval", func(browser *composerFocusCDP) {
		browser.waitFor("approval card", `document.getElementById('chat-input-request-request')?.textContent.includes('Install and continue') ? 'ready' : 'waiting'`, "ready")
		text := browser.evaluate(`document.getElementById('chat-input-request-request').textContent`)
		for _, want := range []string{"1.2.3", "example.test", "Download details", "SHA-256", "bin/tool", "Install and continue"} {
			if !strings.Contains(text, want) {
				t.Fatalf("card missing %q: %s", want, text)
			}
		}
		for _, unwanted := range []string{"Requires external tools", "do not prove trust", "--version", "health_check"} {
			if strings.Contains(text, unwanted) {
				t.Fatalf("unexpected technical copy %q", unwanted)
			}
		}
		browser.waitFor("no dependency subheading", `document.querySelector('[data-skill-dependency-details]')?.children.length === 1 && document.querySelector('[data-skill-dependency-details]')?.firstElementChild.tagName === 'DETAILS' ? 'ready' : 'waiting'`, "ready")
		browser.waitFor("details collapsed", `document.querySelector('[data-skill-dependency-details] details')?.open === false ? 'closed' : 'open'`, "closed")
		for _, phase := range []struct{ stage, label string }{{"downloading", "Downloading Example…"}, {"preparing", "Installing Example…"}, {"checking executable", "Checking Example…"}} {
			payload, _ := json.Marshal(map[string]string{"type": "skill_dependency_progress", "exec_id": "execution", "dependency_name": "example", "phase": phase.stage})
			browser.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:` + string(payload) + `})); 'sent'`)
			browser.waitFor("progress label", `document.querySelector('[data-skill-dependency-status]')?.textContent || ''`, phase.label)
			browser.waitFor("one shared spinner", `document.querySelectorAll('[data-skill-dependency-status] .loading-spinner').length.toString()`, "1")
		}
		browser.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'skill_dependency_progress',exec_id:'execution',dependency_name:'example',phase:'failed'}})); 'sent'`)
		browser.waitFor("failed spinner stopped", `document.querySelector('[data-skill-dependency-status]')?.textContent.includes('installation failed') && !document.querySelector('[data-skill-dependency-status] .loading-spinner') ? 'stopped' : 'waiting'`, "stopped")
		browser.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'skill_dependency_progress',exec_id:'execution',dependency_name:'example',phase:'ready'}})); 'sent'`)
		browser.waitFor("ready status removed", `document.querySelector('[data-skill-dependency-status]') ? 'waiting' : 'removed'`, "removed")
		browser.evaluate(`document.getElementById('chat-input-request-request').dataset.beforeRefresh='true';window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'skill_dependency_approval',exec_id:'execution',project_id:'project',pending_input_id:'request',status:'pending'}})); 'sent'`)
		browser.waitFor("navigable toast", `window.approvalToast?.clickURL || ''`, "/chat/input-requests/request/open?project_id=project")
		// Clicking while the refresh swaps the card splits press and release across nodes.
		browser.waitFor("refreshed card", `document.getElementById('chat-input-request-request')?.dataset.beforeRefresh === undefined ? 'ready' : 'waiting'`, "ready")
		browser.click(`#chat-input-request-request [data-option-label="Not now"]`)
		browser.waitFor("answered card removed", `document.getElementById('chat-input-request-request') ? 'waiting' : 'removed'`, "removed")
		browser.evaluate(`location.reload(); 'reload'`)
		browser.waitFor("persistent answer", `document.querySelector('[data-execution-pair]')?.dataset.skillApprovalsLoaded === 'project' && !document.getElementById('chat-input-request-request') ? 'ready' : 'waiting'`, "ready")
		browser.evaluate(`document.body.insertAdjacentHTML('beforeend','<section id="skill_import_dependency_details"></section><button id="skill_import_submit_btn">Import</button>'); window.previewImportedSkillDependencies({files:[new File(['---\ndependencies: []\n---'],'SKILL.md')]}); 'preview'`)
		browser.waitFor("import dependency preview", `document.getElementById('skill_import_dependency_details').textContent.includes('preview-tool') && !document.getElementById('skill_import_submit_btn').disabled ? 'ready' : 'waiting'`, "ready")
		mu.Lock()
		state = "pending" // A new missing-dependency question; no revocation endpoint.
		mu.Unlock()
		browser.evaluate(`window.dispatchEvent(new CustomEvent('sse-task-event',{detail:{type:'skill_dependency_approval',exec_id:'execution',project_id:'project',pending_input_id:'request',status:'pending'}})); 'refresh'`)
		browser.waitFor("new installation question", `document.querySelectorAll('#chat-input-request-request [data-chat-input-option]').length === 2 ? 'ready' : 'waiting'`, "ready")
		browser.click(`#chat-input-request-request [data-option-label="Install dependencies"]`)
		browser.waitFor("allowed card", `!document.getElementById('chat-input-request-request') ? 'approved' : 'waiting'`, "approved")
		mu.Lock()
		defer mu.Unlock()
		if decisions != 2 {
			t.Fatalf("duplicate decision: %d", decisions)
		}
	})
}
