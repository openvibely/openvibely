package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestChatInputRequestBrowserRendersSubmitsRetriesAndDisablesControls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Browser input request").Build()
	pending, err := tc.handler.chatInputRequests.create(project.ID, "exec-browser", []chatInputRequestQuestion{{
		ID:       "create",
		Question: "<img src=x onerror=alert(1)> Should I create a task?",
		Options: []chatInputRequestOption{
			{Label: "Create task", Description: "Create <b>the</b> task now."},
			{Label: "Not now", Description: "Do not create anything."},
		},
	}})
	require.NoError(t, err)

	requestEvent := map[string]any{
		"type":       "chat_user_input_requested",
		"project_id": project.ID,
		"exec_id":    "exec-browser",
		"input_request": map[string]any{
			"id":         pending.ID,
			"expires_at": pending.ExpiresAt.UTC().Format(time.RFC3339),
			"questions": []map[string]any{{
				"id":       "create",
				"question": "<img src=x onerror=alert(1)> Should I create a task?",
				"options": []map[string]string{
					{"label": "Create task", "description": "Create <b>the</b> task now."},
					{"label": "Not now", "description": "Do not create anything."},
				},
			}},
		},
	}
	eventJSON, err := json.Marshal(requestEvent)
	require.NoError(t, err)

	result := make(chan string, 1)
	var answerAttempts atomic.Int32
	runner := fmt.Sprintf(`<script>
	(async function() {
		function report(status, message) {
			return fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':status}, body:message || status, keepalive:true});
		}
		function fail(message) { throw new Error(message); }
		function nextFrame() { return new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); }); }
		async function waitFor(predicate, message) {
			var deadline = Date.now() + 5000;
			while (Date.now() < deadline) {
				var value = predicate();
				if (value) return value;
				await new Promise(function(resolve) { setTimeout(resolve, 25); });
			}
			fail(message);
		}
		try {
			var eventData = %s;
			window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: eventData}));
			var card = await waitFor(function() { return document.querySelector('[data-chat-input-request-id="%s"]'); }, 'input request card did not render');
			if (!card.textContent.includes('<img src=x onerror=alert(1)> Should I create a task?')) fail('question text was not rendered as inert text');
			if (card.querySelector('img') || card.querySelector('b')) fail('model-provided markup was rendered as HTML');
			var buttons = Array.prototype.slice.call(card.querySelectorAll('button[data-chat-input-option]'));
			if (buttons.length !== 2) fail('expected two option buttons, saw ' + buttons.length);
			var createButton = buttons.filter(function(button) { return button.textContent.indexOf('Create task') !== -1; })[0];
			if (!createButton) fail('create option button missing');
			createButton.click();
			await waitFor(function() { return !createButton.disabled && /Unable to submit answer/.test(card.textContent); }, 'failed submission did not re-enable controls with retry error');
			if (card.getAttribute('data-submitted') === 'true') fail('failed submission left card marked submitted');
			createButton.click();
			await waitFor(function() { return card.getAttribute('data-submitted') === 'true'; }, 'successful submission did not mark card submitted');
			await nextFrame();
			buttons = Array.prototype.slice.call(card.querySelectorAll('button[data-chat-input-option]'));
			if (!buttons.every(function(button) { return button.disabled; })) fail('controls were not disabled after successful submission');
			if (!/Selected: Create task/.test(card.textContent)) fail('selected answer was not shown after successful submission');
			if (createButton.getAttribute('aria-pressed') !== 'true') fail('selected option did not remain accessible as pressed');
			await report('pass', 'input request browser interaction passed');
		} catch (error) {
			await report('fail', String(error && error.stack || error));
		}
	})();
	</script>`, string(eventJSON), pending.ID)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			select {
			case result <- r.Header.Get("X-Browser-Status") + ":" + string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/chat/input-requests/") && strings.HasSuffix(r.URL.Path, "/answer") && answerAttempts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, "intentional first submit failure")
			return
		}
		recorder := httptest.NewRecorder()
		tc.echo.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		body := recorder.Body.String()
		if r.URL.Path == "/chat" {
			body = strings.Replace(body, "</body>", runner+"</body>", 1)
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "chat-input-request-profile"),
		"--window-size=1024,768", server.URL+"/chat?project_id="+project.ID,
	)
	require.NoError(t, startHandlerBrowserProcess(cmd))
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(cmd)
		}
	}()
	select {
	case outcome := <-result:
		stopHandlerBrowserProcess(cmd)
		stopped = true
		require.True(t, strings.HasPrefix(outcome, "pass:"), outcome)
	case <-time.After(45 * time.Second):
		t.Fatal("input request browser regression timed out")
	}
	require.Equal(t, int32(2), answerAttempts.Load())
}

func TestChatInputRequestBrowserRefreshDoesNotShowStaleActionableControls(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	tc := NewTestContext(t)
	project := tc.CreateProject().WithName("Browser input refresh").Build()
	pending, err := tc.handler.chatInputRequests.create(project.ID, "exec-refresh", []chatInputRequestQuestion{{
		ID:       "continue",
		Question: "Continue?",
		Options:  []chatInputRequestOption{{Label: "Yes", Description: "Continue"}, {Label: "No", Description: "Stop"}},
	}})
	require.NoError(t, err)

	requestEvent := map[string]any{
		"type":       "chat_user_input_requested",
		"project_id": project.ID,
		"exec_id":    "exec-refresh",
		"input_request": map[string]any{
			"id":         pending.ID,
			"expires_at": pending.ExpiresAt.UTC().Format(time.RFC3339),
			"questions":  []map[string]any{{"id": "continue", "question": "Continue?", "options": []map[string]string{{"label": "Yes", "description": "Continue"}, {"label": "No", "description": "Stop"}}}},
		},
	}
	eventJSON, err := json.Marshal(requestEvent)
	require.NoError(t, err)

	result := make(chan string, 1)
	runner := fmt.Sprintf(`<script>
	(async function() {
		function report(status, message) { return fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':status}, body:message || status, keepalive:true}); }
		function fail(message) { throw new Error(message); }
		async function waitFor(predicate, message) {
			var deadline = Date.now() + 5000;
			while (Date.now() < deadline) {
				var value = predicate();
				if (value) return value;
				await new Promise(function(resolve) { setTimeout(resolve, 25); });
			}
			fail(message);
		}
		try {
			var eventData = %s;
			window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: eventData}));
			await waitFor(function() { return document.querySelector('[data-chat-input-request-id="%s"]'); }, 'input request did not render before refresh');
			window.history.replaceState({}, '', '/chat?project_id=%s&refreshed=1');
			location.reload();
		} catch (error) {
			await report('fail', String(error && error.stack || error));
		}
	})();
	</script>`, string(eventJSON), pending.ID, project.ID)
	refreshRunner := `<script>
	(async function() {
		function report(status, message) { return fetch('/browser-result', {method:'POST', headers:{'X-Browser-Status':status}, body:message || status, keepalive:true}); }
		try {
			await new Promise(function(resolve) { requestAnimationFrame(function() { requestAnimationFrame(resolve); }); });
			if (document.querySelector('[data-chat-input-request-id]') || document.querySelector('button[data-chat-input-option]')) {
				throw new Error('stale actionable input request controls survived refresh');
			}
			await report('pass', 'refresh did not restore stale input controls');
		} catch (error) {
			await report('fail', String(error && error.stack || error));
		}
	})();
	</script>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/browser-result" {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			select {
			case result <- r.Header.Get("X-Browser-Status") + ":" + string(body):
			default:
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		recorder := httptest.NewRecorder()
		tc.echo.ServeHTTP(recorder, r)
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		body := recorder.Body.String()
		if r.URL.Path == "/chat" {
			if r.URL.Query().Get("refreshed") == "1" {
				body = strings.Replace(body, "</body>", refreshRunner+"</body>", 1)
			} else {
				body = strings.Replace(body, "</body>", runner+"</body>", 1)
			}
		}
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
		"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+filepath.Join(t.TempDir(), "chat-input-refresh-profile"),
		"--window-size=1024,768", server.URL+"/chat?project_id="+project.ID,
	)
	require.NoError(t, startHandlerBrowserProcess(cmd))
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(cmd)
		}
	}()
	select {
	case outcome := <-result:
		stopHandlerBrowserProcess(cmd)
		stopped = true
		require.True(t, strings.HasPrefix(outcome, "pass:"), outcome)
	case <-time.After(45 * time.Second):
		t.Fatal("input request refresh browser regression timed out")
	}
}
