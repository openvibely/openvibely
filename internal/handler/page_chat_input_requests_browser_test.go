package handler

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

func TestPageSideChatAnswersQuestion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	env := setupPluginPages(t)
	in := env.inA
	rec := env.do(t, http.MethodPost, "/api/plugin-pages/"+in.ID+"/chat?project_id="+in.ProjectID, "message=hello", map[string]string{"HX-Request": "true", "Content-Type": "application/x-www-form-urlencoded"})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	turn := env.starts[len(env.starts)-1]

	created := make(chan *chatInputRequest, 1)
	runner := `<script>
	(async function() {
		function report(status, message) { return fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':status}, body:message || status, keepalive:true}); }
		async function waitFor(predicate, message) {
			var deadline = Date.now() + 5000;
			while (Date.now() < deadline) { var v = predicate(); if (v) return v; await new Promise(function(r) { setTimeout(r, 25); }); }
			throw new Error(message);
		}
		var event, card = function() { return event && document.querySelector('#page-chat-messages [data-chat-input-request-id="' + event.input_request.id + '"]'); };
		try {
            var toggle = document.querySelector('[data-page-chat-toggle]');
            var panel = document.getElementById('page-side-chat');
            if (!toggle || !panel || !panel.hidden) throw new Error('shared panel must start collapsed');
            toggle.click();
            if (panel.hidden || toggle.getAttribute('aria-expanded') !== 'true') throw new Error('panel did not open');
            if (!panel.querySelector('form.chat-input-container') || !panel.querySelector('[aria-label="Attach files"]')) throw new Error('shared composer missing');
            if (panel.textContent.includes('uses page context + tools') || panel.textContent.includes('Chat ·')) throw new Error('unexpected panel header');
            toggle.click();
            if (!panel.hidden) throw new Error('panel did not collapse');
            toggle.click();
			await waitFor(function() { return document.querySelector('#page-chat-messages [data-exec-id]'); }, 'side chat history missing');
			await new Promise(function(r) { setTimeout(r, 200); });
			event = await (await fetch('/make-question')).json();
			window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: event}));
			await waitFor(card, 'question card did not render in side chat');
			card().querySelector('[data-option-label="No"]').click();
			await waitFor(function() { var b = card().querySelector('[data-chat-input-nav="submit"]'); return b && !b.disabled; }, 'submit not enabled');
			card().querySelector('[data-chat-input-nav="submit"]').click();
			await waitFor(function() { return !card(); }, 'card not removed after answer');
			await report('pass');
		} catch (err) { await report('fail', String(err && err.stack || err)); }
	})();
	</script>`

	result := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8192))
			select {
			case result <- r.Header.Get("X-Browser-Status") + ":" + string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/make-question" {
			pending, err := env.h.chatInputRequests.create(in.ProjectID, turn.ExecID, []chatInputRequestQuestion{{
				ID: "continue", Question: "Continue?", Options: []chatInputRequestOption{{Label: "Yes", Description: "Continue"}, {Label: "No", Description: "Stop"}},
			}})
			if err == nil {
				err = env.h.persistChatInputRequest(r.Context(), pending)
			}
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			created <- pending
			_ = json.NewEncoder(w).Encode(map[string]any{"type": events.ChatUserInputRequested, "project_id": in.ProjectID, "exec_id": turn.ExecID, "input_request": map[string]any{"id": pending.ID, "exec_id": turn.ExecID}})
			return
		}
		recorder := httptest.NewRecorder()
		env.e.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		body := recorder.Body.String()
		if r.URL.Path == "/p/"+in.ID {
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--window-size=1024,768", server.URL+"/p/"+in.ID+"?project_id="+in.ProjectID,
	)
	require.NoError(t, startHandlerBrowserProcess(t, cmd))
	defer stopHandlerBrowserProcess(t, cmd)
	select {
	case outcome := <-result:
		require.True(t, strings.HasPrefix(outcome, "pass:"), outcome)
	case <-time.After(30 * time.Second):
		t.Fatal("page side chat question browser test timed out")
	}
	pending := <-created
	mainList := env.do(t, http.MethodGet, "/chat/input-requests?project_id="+in.ProjectID, "", nil).Body.String()
	require.NotContains(t, mainList, pending.ID, "page questions must not appear in project Chat")
	answers, err := env.h.chatInputRequests.wait(t.Context(), pending.ID)
	require.NoError(t, err)
	require.Equal(t, "No", answers[0].Label)
}
