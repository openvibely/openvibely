package pages

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_MermaidRendersAfterLiveChatCompletion(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmxJS, err := os.ReadFile(filepath.Join("..", "components", "testdata", "htmx-2.0.4.min.js"))
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}
	project := models.Project{ID: "project-mermaid-live", Name: "Mermaid live"}
	execID := "chat-mermaid-live"
	output := "Here:\n\n```mermaid\nflowchart TD\n A[Start] --> B[End]\n```\n"
	var phase atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
			_, _ = w.Write(htmxJS)
		case "/chat":
			var executions []models.Execution
			switch phase.Load() {
			case 1:
				executions = []models.Execution{{ID: execID, Status: models.ExecRunning, PromptSent: "diagram", Output: ""}}
			case 2:
				executions = []models.Execution{{ID: execID, Status: models.ExecCompleted, PromptSent: "diagram", Output: output}}
			}
			if r.Header.Get("HX-Request") == "true" {
				_, _ = w.Write([]byte(renderTerminalBrowserComponent(t, ChatContent(nil, executions, project.ID, nil, nil, false, false, 30))))
				return
			}
			document := renderTerminalBrowserComponent(t, Chat([]models.Project{project}, project.ID, nil, executions, nil, nil, false, false, 30))
			_, _ = w.Write([]byte(installTerminalBrowserPrelude(document)))
		case "/chat/send":
			phase.Store(1)
			_, _ = w.Write([]byte(renderTerminalBrowserComponent(t, components.ChatFollowupResponse("diagram", execID, "chat-messages", "", false, nil, project.ID))))
		case "/auth/me":
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	outputJSON, _ := json.Marshal(output)
	scenarios := []struct {
		name, finish string
		serverPhase  int32
	}{
		{"stream done", `S.emit('done','completed')`, 2},
		{"global done first", `fire({type:'chat_response_done',project_id:P,exec_id:E,status:'completed',completed_output:OUT});S.emit('done','completed')`, 2},
		{"stream done then global", `S.emit('done','completed');fire({type:'chat_response_done',project_id:P,exec_id:E,status:'completed',completed_output:OUT})`, 2},
		{"global done only", `fire({type:'chat_response_done',project_id:P,exec_id:E,status:'completed',completed_output:OUT})`, 2},
	}
	t.Run("composer send", func(t *testing.T) {
		phase.Store(0)
		runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id="+project.ID, "mermaid-live-chat-send", func(browser *composerFocusCDP) {
			browser.waitFor("chat page", `String(!!document.getElementById('chat-form')&&typeof window.renderMermaidDiagrams)`, "function")
			browser.evaluate(`(function(){htmx.ajax('POST','/chat/send?project_id=` + project.ID + `',{target:'#chat-messages',swap:'beforeend'});return 'ok';})()`)
			browser.waitFor("live stream", `String(!!window.__terminalStreamFor('`+execID+`'))`, "true")
			browser.evaluate(`(function(){window.__terminalStreamFor('` + execID + `').emit('message',` + string(outputJSON) + `);return 'ok';})()`)
			browser.waitFor("streamed code block", `String(!!document.querySelector('#streaming-message-`+execID+` code.language-mermaid'))`, "true")
			phase.Store(2)
			browser.evaluate(`(function(){var S=window.__terminalStreamFor('` + execID + `');S.emit('done','completed');window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:{type:'chat_response_done',project_id:'` + project.ID + `',exec_id:'` + execID + `',status:'completed',completed_output:` + string(outputJSON) + `}}));return 'ok';})()`)
			browser.waitFor("diagram rendered without refresh", `(function(){var c=document.getElementById('streaming-message-`+execID+`');var p=c&&c.closest('[data-execution-pair]');return c&&c.querySelector('.chat-mermaid img')?'rendered':'pair='+(p?p.getAttribute('data-exec-status'):'none');})()`, "rendered")
		})
	})
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			phase.Store(0)
			runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id="+project.ID, "mermaid-live-chat", func(browser *composerFocusCDP) {
				browser.waitFor("chat page", `String(!!document.getElementById('chat-messages')&&typeof window.renderMermaidDiagrams)`, "function")
				phase.Store(1)
				prelude := `var P='` + project.ID + `',E='` + execID + `',OUT=` + string(outputJSON) + `;function fire(d){window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:d}))}`
				browser.evaluate(`(function(){` + prelude + `;fire({type:'chat_new_message',project_id:P,exec_id:E,message:'diagram',source:'web'});return 'ok';})()`)
				browser.waitFor("live stream", `String(!!window.__terminalStreamFor('`+execID+`'))`, "true")
				browser.evaluate(`(function(){window.__terminalStreamFor('` + execID + `').emit('message',` + string(outputJSON) + `);return 'ok';})()`)
				browser.waitFor("streamed code block", `String(!!document.querySelector('#chat-execution-`+execID+` code.language-mermaid'))`, "true")
				phase.Store(sc.serverPhase)
				browser.evaluate(`(function(){` + prelude + `;var S=window.__terminalStreamFor(E);` + sc.finish + `;return 'ok';})()`)
				browser.waitFor("diagram rendered without refresh", `(function(){var p=document.getElementById('chat-execution-`+execID+`');return p&&p.querySelector('.chat-mermaid img')?'rendered':'status='+p.getAttribute('data-exec-status')+' code='+!!p.querySelector('code.language-mermaid')+' pending='+!!(p.querySelector('code.language-mermaid')||{})._mermaidRender;})()`, "rendered")
			})
		})
	}
}
