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
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/stretchr/testify/require"
)

// Model-menu interactions do not trigger transcript reloads. Exercise them
// separately from explicit HTMX swaps, which previously dropped question cards.
func TestChatInputRequestBrowserPreservesProgressAcrossTranscriptSwaps(t *testing.T) {
	for _, resolution := range []string{"event", "submit", "swap", "swap-completed", "next-question"} {
		t.Run(resolution, func(t *testing.T) { runChatInputRequestProgressBrowser(t, resolution) })
	}
}

func runChatInputRequestProgressBrowser(t *testing.T, resolution string) {
	if testing.Short() {
		t.Skip("skipping browser regression in short mode")
	}
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}

	tc := NewStoreTestContext(t)
	project := tc.CreateProject().WithName("Browser input model menu").Build()
	agent := tc.CreateLLMConfig().WithName("Alpha").WithProvider(models.ProviderOpenAI).WithModel("gpt-5").WithAPIKey("sk-test").AsDefault().Build()
	tc.CreateLLMConfig().WithName("Beta").WithProvider(models.ProviderAnthropic).WithModel("claude-sonnet-4").WithAPIKey("sk-test").Build()

	// A real running chat execution makes the server render a running execution
	// pair, so a transcript morph swap replaces the pair that holds the card.
	chatTask := tc.CreateTask(project.ID).WithTitle("Chat: continue?").WithCategory(models.CategoryChat).WithPrompt("continue?").Build()
	runningExec := tc.CreateExecution(chatTask.ID, agent.ID).WithStatus(models.ExecRunning).WithPromptSent("continue?").Build()

	pending, err := tc.handler.chatInputRequests.create(project.ID, runningExec.ID, []chatInputRequestQuestion{{
		ID:       "continue",
		Question: "Continue?",
		Options:  []chatInputRequestOption{{Label: "Yes", Description: "Continue"}, {Label: "No", Description: "Stop"}},
	}, {
		ID:       "scope",
		Question: "Scope?",
		Options:  []chatInputRequestOption{{Label: "Small", Description: "Minimal"}, {Label: "Large", Description: "Broad"}},
	}})
	require.NoError(t, err)

	requestEvent := map[string]any{
		"type":       "chat_user_input_requested",
		"project_id": project.ID,
		"exec_id":    runningExec.ID,
		"input_request": map[string]any{
			"id":         pending.ID,
			"exec_id":    runningExec.ID,
			"expires_at": pending.ExpiresAt.UTC().Format(time.RFC3339),
			"questions":  []map[string]any{{"id": "continue", "question": "Continue?", "options": []map[string]string{{"label": "Yes", "description": "Continue"}, {"label": "No", "description": "Stop"}}}, {"id": "scope", "question": "Scope?", "options": []map[string]string{{"label": "Small", "description": "Minimal"}, {"label": "Large", "description": "Broad"}}}},
		},
	}
	eventJSON, err := json.Marshal(requestEvent)
	require.NoError(t, err)

	result := make(chan string, 1)
	snapshotReady := make(chan struct{})
	releaseSnapshot := make(chan struct{})
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
		function cardPresent() { return document.querySelector('[data-chat-input-request-id="%s"]'); }
		function cardVisible() { var c = cardPresent(); return c && getComputedStyle(c).visibility !== 'hidden' && c.getClientRects().length > 0; }
		function cardAnswerable() { var c = cardPresent(); return c && c.querySelectorAll('[data-chat-input-option]').length >= 2; }
		function firstAnswerKept() {
			var c = cardPresent();
			var g = c && c.querySelector('[data-chat-input-question-id="continue"]');
			var b = g && g.querySelector('[data-option-label="No"]');
			return g && g.getAttribute('data-selected-label') === 'No' && b && b.getAttribute('aria-pressed') === 'true' && c.getAttribute('data-current-question-index') === '1';
		}
		try {
			window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: %s}));
			await waitFor(cardPresent, 'input request card did not render');
			await waitFor(cardVisible, 'card never became visible after initial hydration');

			var noOption = cardPresent().querySelector('[data-chat-input-question-id="continue"] [data-option-label="No"]');
			if (!noOption) fail('first question option missing');
			noOption.click();
			await waitFor(firstAnswerKept, 'first answer was not selected');

			var modelButton = document.querySelector('form.chat-input-container .chat-model-select');
			if (!modelButton) fail('model selector button missing');

			// Open the models menu.
			modelButton.click();
			await waitFor(function() { var p = document.getElementById('conversation-model-picker'); return p && !p.hidden; }, 'models menu did not open');
			if (!cardVisible()) fail('input request card hidden when models menu opened');

			// Close the models menu via an outside click, without changing the model.
			document.body.click();
			await waitFor(function() { var p = document.getElementById('conversation-model-picker'); return p && p.hidden; }, 'models menu did not close');
			await new Promise(function(resolve) { setTimeout(resolve, 150); });
			if (!cardVisible()) fail('input request card hidden after models menu closed');

			// Open again and change the model.
			modelButton.click();
			await waitFor(function() { var p = document.getElementById('conversation-model-picker'); return p && !p.hidden; }, 'models menu did not reopen');
			var picks = document.querySelectorAll('#conversation-model-picker .ov-mp-pick');
			if (!picks.length) fail('no model options to pick');
			var target = null;
			picks.forEach(function(p) { if (!target && p.textContent.indexOf('Beta') !== -1) target = p; });
			(target || picks[picks.length - 1]).click();
			await new Promise(function(resolve) { setTimeout(resolve, 250); });
			if (!cardVisible()) fail('input request card hidden after changing the model');
			if (!cardAnswerable()) fail('card lost its answer options after changing the model');
			if (!firstAnswerKept()) fail('first answer lost after changing the model');

			var pid = document.getElementById('chat-page-root').getAttribute('data-project-id');
			var originalCard = cardPresent();
			var inputFetches = 0;
			var realFetch = window.fetch;
			window.fetch = function(url) { if (String(url).indexOf('/chat/input-requests') !== -1) inputFetches++; return realFetch.apply(this, arguments); };

			// A transcript morph swap of chat-messages must restore the card.
			await htmx.ajax('GET', '/chat?project_id=' + encodeURIComponent(pid), {target:'#chat-messages', select:'#chat-messages', swap:'morph:outerHTML'});
			await waitFor(cardVisible, 'card was not restored after a chat-messages transcript swap');
			if (!cardAnswerable()) fail('restored card lost its answer options after chat-messages swap');
			await waitFor(firstAnswerKept, 'first answer lost after chat-messages swap');

			// A chat-page-root swap (full content re-render) must also restore it;
			// this is the swap target that previously dropped the card for good.
			await htmx.ajax('GET', '/chat?project_id=' + encodeURIComponent(pid), {target:'#chat-page-root', select:'#chat-page-root', swap:'morph:outerHTML'});
			await waitFor(cardVisible, 'card was not restored after a chat-page-root swap');
			if (!cardAnswerable()) fail('restored card lost its answer options after chat-page-root swap');
			await waitFor(firstAnswerKept, 'first answer lost after chat-page-root swap');
			if (cardPresent() !== originalCard) fail('card was rebuilt instead of preserved across swaps');
			if (inputFetches !== 0) fail('swaps refetched input requests instead of keeping server-rendered card');

			// A card whose execution is outside the loaded window starts at the
			// bottom. Loading that execution through an earlier-message loader
			// must move the same card beside it without resetting the answers.
			var pair = originalCard.parentElement;
			var pairID = pair.id;
			var messages = document.getElementById('chat-messages');
			messages.appendChild(originalCard);
			pair.remove();
			var loader = document.createElement('div');
			loader.id = 'chat-messages-earlier-loader';
			messages.prepend(loader);
			await htmx.ajax('GET', '/chat?project_id=' + encodeURIComponent(pid), {target:'#chat-messages-earlier-loader', select:'#' + pairID, swap:'outerHTML'});
			await waitFor(function() { return originalCard.parentElement.id === pairID; }, 'card did not move to loaded execution');
			if (!firstAnswerKept()) fail('moving card lost partial answers');

			// A failed HTMX snapshot must leave the existing card and draft alone.
			await htmx.ajax('GET', '/chat?project_id=' + encodeURIComponent(pid) + '&fail_questions=1', {target:'#chat-messages', select:'#chat-messages', swap:'morph:outerHTML'}).catch(function() {});
			if (cardPresent() !== originalCard || !firstAnswerKept()) fail('failed refresh discarded progress');

			// Capture a real pending snapshot, but delay its delivery until resolution.
			var id = originalCard.getAttribute('data-chat-input-request-id');
			var resolution = %q;
			if (resolution === 'next-question') {
				var releaseAnswer, releaseQuestion, questionFetches = 0;
				window.fetch = function(url) {
					var args = arguments;
					if (String(url).endsWith('/answer')) {
						return realFetch.apply(this, args).then(function(response) {
							return new Promise(function(resolve) { releaseAnswer = function() { resolve(response); }; });
						});
					}
					if (String(url).indexOf('/chat/input-requests?') !== -1 && ++questionFetches === 1) {
						return realFetch.apply(this, args).then(function(response) {
							return new Promise(function(resolve) { releaseQuestion = function() { resolve(response); }; });
						});
					}
					return realFetch.apply(this, args);
				};
				originalCard.querySelector('[data-chat-input-nav="recommended"]').click();
				await waitFor(function() { return releaseAnswer; }, 'answer response was not captured');
				var nextEvent = await (await realFetch('/next-question')).json();
				window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail:nextEvent}));
				await waitFor(function() { return releaseQuestion; }, 'next question fetch was not captured');
				releaseAnswer();
				await waitFor(function() { return !cardPresent(); }, 'previous answer did not finish');
				releaseQuestion();
				var nextCard = await waitFor(function() { return document.getElementById('chat-input-request-' + nextEvent.input_request.id); }, 'invalidated fetch left the next question invisible');
				if (questionFetches !== 2) fail('expected one retry, got ' + questionFetches);
				window.fetch = realFetch;
				nextCard.querySelector('[data-chat-input-nav="recommended"]').click();
				await waitFor(function() { return !nextCard.isConnected; }, 'retried question was not answerable');
				await report('pass', 'next question survives previous answer completion');
				return;
			}
			var staleHTML = await (await realFetch('/chat/input-requests?project_id=' + encodeURIComponent(pid))).text();
			var releaseSnapshot;
			window.fetch = function(url) {
				if (String(url).indexOf('/chat/input-requests?') !== -1 && !releaseSnapshot) {
					return new Promise(function(resolve) { releaseSnapshot = function() { resolve(new Response(staleHTML)); }; });
				}
				return realFetch.apply(this, arguments);
			};
			var delayedRefresh = window.refreshChatInputRequests();
			await waitFor(function() { return releaseSnapshot; }, 'refresh did not start');
			var swapTarget = resolution === 'submit' ? '#chat-page-root' : '#chat-messages';
			if (resolution === 'swap-completed') {
				if (!(await realFetch('/complete-execution')).ok) fail('could not complete execution fixture');
				originalCard.parentElement.setAttribute('data-exec-status', 'completed');
				originalCard.parentElement.setAttribute('hx-preserve', 'true');
			}
			var delayedTrigger = document.createElement('div');
			document.body.appendChild(delayedTrigger);
			var delayedTranscript = htmx.ajax('GET', '/chat?project_id=' + encodeURIComponent(pid) + '&delay_snapshot=1', {source:delayedTrigger, target:swapTarget, select:swapTarget, swap:'morph:outerHTML'});
			await realFetch('/snapshot-ready');
			if (resolution === 'submit') {
				originalCard.querySelector('[data-chat-input-nav="recommended"]').click();
				await waitFor(function() { return !cardPresent(); }, 'recommended submission did not remove card');
			} else {
				var answer = await realFetch('/chat/input-requests/' + id + '/answer', {
					method: 'POST', headers: {'Content-Type':'application/json'},
					body: JSON.stringify({project_id:pid, answers:[{question_id:'continue',label:'Yes'},{question_id:'scope',label:'Small'}]})
				});
				if (!answer.ok) fail('remote answer failed');
				if (resolution === 'event') {
					window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail:{type:'chat_user_input_resolved', project_id:pid, input_request:{id:id,completed:true}}}));
				} else {
					await htmx.ajax('GET', '/chat?project_id=' + encodeURIComponent(pid), {target:'#chat-messages', select:'#chat-messages', swap:'morph:outerHTML'});
				}
			}
			if (cardPresent() || window._chatInputDrafts[id]) fail('resolution left card or draft');
			releaseSnapshot();
			await delayedRefresh;
			if (cardPresent() || window._chatInputDrafts[id]) fail('stale refresh resurrected resolved question');
			window.fetch = realFetch;
			await realFetch('/release-snapshot');
			await delayedTranscript;
			if (cardPresent() || window._chatInputDrafts[id]) fail('stale full transcript restored answered card');
			await report('pass', 'progress preserved and remote resolution cleaned up');
		} catch (error) {
			await report('fail', String(error && error.stack || error));
		}
	})();
	</script>`, pending.ID, string(eventJSON), resolution)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/next-question" {
			next, err := tc.handler.chatInputRequests.create(project.ID, runningExec.ID, []chatInputRequestQuestion{{
				ID: "next", Question: "Continue with the next step?", Options: []chatInputRequestOption{{Label: "Yes", Description: "Continue"}, {Label: "No", Description: "Stop"}},
			}})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "chat_user_input_requested", "project_id": project.ID, "input_request": chatInputRequestForEvent(next)})
			return
		}
		if r.URL.Path == "/complete-execution" {
			if err := tc.execRepo.Complete(r.Context(), runningExec.ID, models.ExecCompleted, "Finished", "", 0, 0); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/snapshot-ready" {
			select {
			case <-snapshotReady:
			case <-r.Context().Done():
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path == "/release-snapshot" {
			close(releaseSnapshot)
			w.WriteHeader(http.StatusNoContent)
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
		if r.URL.Query().Get("fail_questions") == "1" {
			http.Error(w, "Unable to refresh Chat questions", http.StatusServiceUnavailable)
			return
		}
		recorder := httptest.NewRecorder()
		tc.echo.ServeHTTP(recorder, r)
		if r.URL.Query().Get("delay_snapshot") == "1" {
			close(snapshotReady)
			select {
			case <-releaseSnapshot:
			case <-r.Context().Done():
				return
			}
		}
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
		"--user-data-dir="+filepath.Join(t.TempDir(), "chat-input-model-menu-profile"),
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
		t.Fatal("input request model menu browser regression timed out")
	}
}
