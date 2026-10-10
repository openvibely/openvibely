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
			browser.waitFor("streamed diagram", `String(!!document.querySelector('#streaming-message-`+execID+` code.language-mermaid, #streaming-message-`+execID+` .chat-mermaid'))`, "true")
			phase.Store(2)
			browser.evaluate(`(function(){var S=window.__terminalStreamFor('` + execID + `');S.emit('done','completed');window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:{type:'chat_response_done',project_id:'` + project.ID + `',exec_id:'` + execID + `',status:'completed',completed_output:` + string(outputJSON) + `}}));return 'ok';})()`)
			browser.waitFor("diagram rendered without refresh", `(function(){var c=document.getElementById('streaming-message-`+execID+`');var p=c&&c.closest('[data-execution-pair]');return c&&c.querySelector('.chat-mermaid img')?'rendered':'pair='+(p?p.getAttribute('data-exec-status'):'none');})()`, "rendered")
		})
	})
	t.Run("renders when fence closes mid-stream", func(t *testing.T) {
		phase.Store(0)
		runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id="+project.ID, "mermaid-live-chat-fence", func(browser *composerFocusCDP) {
			browser.waitFor("chat page", `String(!!document.getElementById('chat-messages')&&typeof window.renderMermaidDiagrams)`, "function")
			phase.Store(1)
			browser.evaluate(`(function(){window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:{type:'chat_new_message',project_id:'` + project.ID + `',exec_id:'` + execID + `',message:'diagram',source:'web'}}));return 'ok';})()`)
			browser.waitFor("live stream", `String(!!window.__terminalStreamFor('`+execID+`'))`, "true")
			pair := `document.getElementById('chat-execution-` + execID + `')`
			emit := func(text string) {
				j, _ := json.Marshal(text)
				browser.evaluate(`(function(){window.__terminalStreamFor('` + execID + `').emit('message',` + string(j) + `);return 'ok';})()`)
			}
			emit("Here:\n\n```mermaid\nflowchart TD\n A[Start] --> B[End]\n")
			browser.waitFor("open fence shown as code", `String(!!`+pair+`.querySelector('code.language-mermaid'))`, "true")
			emit("```")
			browser.waitFor("partial closing line stays code", `String(!!`+pair+`.querySelector('code.language-mermaid'))`, "true")
			if got := browser.evaluateAwait(`(async function(){await new Promise(r=>setTimeout(r,500));return String(!!` + pair + `.querySelector('.chat-mermaid'))})()`); got != "false" {
				t.Fatalf("unclosed fence rendered as diagram: %s", got)
			}
			emit("\n\nMore text")
			browser.waitFor("diagram rendered while running", `(function(){var p=`+pair+`;return p.getAttribute('data-exec-status')+':'+!!p.querySelector('.chat-mermaid img')})()`, "running:true")
			browser.evaluate(`(function(){window.__mermaidFlashes=0;new MutationObserver(function(){if(` + pair + `.querySelector('code.language-mermaid'))window.__mermaidFlashes++;}).observe(` + pair + `,{childList:true,subtree:true});return 'ok';})()`)
			for i := 0; i < 5; i++ {
				emit(" and more")
			}
			emit("\n\n```js\nconst open = true;\n")
			browser.waitFor("later text streamed", `String(!!`+pair+`.querySelector('code.language-js'))`, "true")
			if got := browser.evaluate(`(function(){var p=` + pair + `;return !!p.querySelector('.chat-mermaid img')+':'+window.__mermaidFlashes})()`); got != "true:0" {
				t.Fatalf("diagram did not survive live redraws (rendered:flashes) = %s", got)
			}
		})
	})
	nested := []struct{ name, open, close string }{
		{"list item", "1. Step one\n\n    ```mermaid\n    flowchart TD\n     A[Start] --> B[End]\n", "    ```\n\n2. Step two"},
		{"list marker", "1. ```mermaid\n   flowchart TD\n    A[Start] --> B[End]\n", "   ```\n\n2. Step two"},
		{"blockquote", "> ```mermaid\n> flowchart TD\n>  A[Start] --> B[End]\n", "> ```\n\nAfter"},
		{"four-space fence inside", "```mermaid\nflowchart TD\n A[\"Start\n    ```\n\"] --> B[End]\n", "```\n\nAfter"},
		{"list-marker fence inside", "```mermaid\nflowchart TD\n A[\"Start\n- ```\n\"] --> B[End]\n", "```\n\nAfter"},
		// Markdown measures the closing fence from the list item's content, not the opening fence.
		{"list fence indented from content", "1. Step one\n\n      ```mermaid\n      flowchart TD\n       A[\"Start\n         ```\n      \"] --> B[End]\n", "      ```\n\n2. Step two"},
		{"lazy list line before fence", "1. Step one\ncontinued lazily\n\n    ```mermaid\n    flowchart TD\n     A[Start] --> B[End]\n", "    ```\n\n2. Step two"},
		{"tab after list marker", "1.\t```mermaid\n    flowchart TD\n     A[Start] --> B[End]\n", "    ```\n\nAfter"},
	}
	for _, nc := range nested {
		t.Run("nested fence "+nc.name, func(t *testing.T) {
			phase.Store(0)
			runComposerFocusCDP(t, chrome, server.URL+"/chat?project_id="+project.ID, "mermaid-live-chat-nested", func(browser *composerFocusCDP) {
				browser.waitFor("chat page", `String(!!document.getElementById('chat-messages')&&typeof window.renderMermaidDiagrams)`, "function")
				phase.Store(1)
				browser.evaluate(`(function(){window.dispatchEvent(new CustomEvent('sse-chat-live-event',{detail:{type:'chat_new_message',project_id:'` + project.ID + `',exec_id:'` + execID + `',message:'diagram',source:'web'}}));return 'ok';})()`)
				browser.waitFor("live stream", `String(!!window.__terminalStreamFor('`+execID+`'))`, "true")
				pair := `document.getElementById('chat-execution-` + execID + `')`
				emit := func(text string) {
					j, _ := json.Marshal(text)
					browser.evaluate(`(function(){window.__terminalStreamFor('` + execID + `').emit('message',` + string(j) + `);return 'ok';})()`)
				}
				emit(nc.open)
				browser.waitFor("open nested fence shown as code", `String(!!`+pair+`.querySelector('code.language-mermaid'))`, "true")
				if got := browser.evaluateAwait(`(async function(){await new Promise(r=>setTimeout(r,500));var p=` + pair + `;return !!p.querySelector('.chat-mermaid')+':'+!!p.querySelector('[data-mermaid-error]')})()`); got != "false:false" {
					t.Fatalf("open nested fence rendered or errored (diagram:error) = %s", got)
				}
				emit(nc.close)
				browser.waitFor("nested diagram rendered while running", `(function(){var p=`+pair+`;return p.getAttribute('data-exec-status')+':'+!!p.querySelector('.chat-mermaid img')+':'+!!p.querySelector('[data-mermaid-error]')})()`, "running:true:false")
			})
		})
	}
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
				browser.waitFor("streamed diagram", `String(!!document.querySelector('#chat-execution-`+execID+` code.language-mermaid, #chat-execution-`+execID+` .chat-mermaid'))`, "true")
				phase.Store(sc.serverPhase)
				browser.evaluate(`(function(){` + prelude + `;var S=window.__terminalStreamFor(E);` + sc.finish + `;return 'ok';})()`)
				browser.waitFor("diagram rendered without refresh", `(function(){var p=document.getElementById('chat-execution-`+execID+`');return p&&p.querySelector('.chat-mermaid img')?'rendered':'status='+p.getAttribute('data-exec-status')+' code='+!!p.querySelector('code.language-mermaid')+' pending='+!!(p.querySelector('code.language-mermaid')||{})._mermaidRender;})()`, "rendered")
			})
		})
	}
}
