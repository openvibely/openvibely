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
	pending, err := tc.handler.chatInputRequests.create(project.ID, "exec-browser", []chatInputRequestQuestion{
		{
			ID:       "create",
			Question: "<img src=x onerror=alert(1)> Should I create a task?",
			Options: []chatInputRequestOption{
				{Label: "Create task", Description: "Create <b>the</b> task now."},
				{Label: "Not now", Description: "Do not create anything."},
			},
		},
		{
			ID:       "fallback",
			Question: "Which fallback should be used?",
			Options: []chatInputRequestOption{
				{Label: "Provider-aware", Description: "Use each provider's fallback."},
				{Label: "Shared local", Description: "Use one local fallback."},
			},
		},
	})
	require.NoError(t, err)

	requestEvent := map[string]any{
		"type":       "chat_user_input_requested",
		"project_id": project.ID,
		"exec_id":    "exec-browser",
		"input_request": map[string]any{
			"id":         pending.ID,
			"expires_at": pending.ExpiresAt.UTC().Format(time.RFC3339),
			"questions": []map[string]any{
				{
					"id":       "create",
					"question": "<img src=x onerror=alert(1)> Should I create a task?",
					"options": []map[string]string{
						{"label": "Create task", "description": "Create <b>the</b> task now."},
						{"label": "Not now", "description": "Do not create anything."},
					},
				},
				{
					"id":       "fallback",
					"question": "Which fallback should be used?",
					"options": []map[string]string{
						{"label": "Provider-aware", "description": "Use each provider's fallback."},
						{"label": "Shared local", "description": "Use one local fallback."},
					},
				},
			},
		},
	}
	eventJSON, err := json.Marshal(requestEvent)
	require.NoError(t, err)

	result := make(chan string, 1)
	var answerAttempts atomic.Int32
	var firstAnswerBody atomic.Value
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
			await waitFor(function() { return document.querySelector('[data-chat-input-request-id="%s"]'); }, 'startup reconciliation did not render the input request card');
			var executionPair = document.createElement('div');
			executionPair.id = 'chat-execution-exec-browser';
			executionPair.setAttribute('data-execution-pair', 'true');
			document.getElementById('chat-messages').appendChild(executionPair);
			window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail: eventData}));
			var card = await waitFor(function() { return document.querySelector('[data-chat-input-request-id="%s"]'); }, 'input request card did not render');
			if (card.parentElement !== executionPair) fail('input request was not attached to its assistant turn');
			if (!card.textContent.includes('<img src=x onerror=alert(1)> Should I create a task?')) fail('question text was not rendered as inert text');
			if (card.querySelector('img') || card.querySelector('b')) fail('model-provided markup was rendered as HTML');
			if (card.textContent.indexOf('Input requested') !== -1 || card.textContent.indexOf('Answer each question') !== -1) fail('extra input request heading or instructions were shown above the question');
			var visibleQuestion = card.querySelector('[data-chat-input-question-index]:not(.hidden)');
			if (!visibleQuestion || visibleQuestion.querySelector('legend').classList.contains('text-sm')) fail('visible question was not promoted to the main heading');
			if (card.querySelectorAll('[data-chat-input-question-index]:not(.hidden)').length !== 1) fail('expected exactly one visible question');
			if (!/1 of 2/.test(card.textContent)) fail('question position was not shown');
			var buttons = Array.prototype.slice.call(card.querySelectorAll('[data-chat-input-option]'));
			if (buttons.length !== 6) fail('expected options plus a custom-answer choice for each question, saw ' + buttons.length);
				var createButton = buttons.filter(function(button) { return button.textContent.indexOf('Create task') !== -1; })[0];
				if (!createButton) fail('create option button missing');
				if (createButton.tagName !== 'DIV' || createButton.getAttribute('role') !== 'button' || createButton.getAttribute('tabindex') !== '0') fail('question response should render as an app card control, not a native button');
				if (createButton.getAttribute('aria-pressed') !== 'false') fail('recommended option was preselected before user click');
				if (createButton.textContent.indexOf('Recommended') === -1) fail('recommended option was not visually labeled');
				if (createButton.classList.contains('btn-outline') || createButton.classList.contains('btn-primary')) fail('question option still uses distracting DaisyUI outline/primary styling');
				['card','bg-base-100','shadow-sm','border','border-base-300','hover:border-primary/40','active:border-primary/40','hover:shadow-md','transition-all'].forEach(function(className) {
					if (!createButton.classList.contains(className)) fail('question option did not reuse shared page-card styling: ' + className);
				});
				if (!createButton.querySelector('.chat-input-recommended-badge')) fail('recommended option did not use the muted badge styling');
				var createStyle = getComputedStyle(createButton);
				var initialBorderColor = createStyle.borderColor;
				if (createStyle.cursor !== 'pointer') fail('question option does not look clickable');
				if (!createStyle.boxShadow || createStyle.boxShadow === 'none') fail('question option regressed to flat text styling');
				if (createStyle.backgroundColor === 'rgb(0, 0, 0)' || createStyle.backgroundColor === 'rgba(0, 0, 0, 0)') fail('question option rendered with an unreadable black or transparent background');
				if (createStyle.color === createStyle.backgroundColor) fail('question option text color matched its background');
				createButton.dispatchEvent(new PointerEvent('pointerdown', {bubbles:true, pointerId:1, pointerType:'mouse'}));
				await nextFrame();
				if (!createButton.classList.contains('chat-input-option-pressed')) fail('question option did not enter the stable pressed border state');
				if (!createButton.getAttribute('style').includes('border-color')) fail('question option press state did not install the inline border guard');
				var pressedStyle = getComputedStyle(createButton);
				var pressedBorderColor = pressedStyle.borderColor;
				if (pressedBorderColor === 'rgb(255, 255, 255)' || pressedBorderColor === initialBorderColor) fail('question option press state did not use the stable primary border color');
				if (pressedStyle.outlineStyle !== 'none' && pressedStyle.outlineWidth !== '0px') fail('question option press state can still paint an outline: style=' + pressedStyle.outlineStyle + ' width=' + pressedStyle.outlineWidth + ' color=' + pressedStyle.outlineColor + ' inline=' + createButton.getAttribute('style'));
				if (pressedStyle.boxShadow.indexOf('255, 255, 255') !== -1) fail('question option press state can still paint a white ring or shadow');
				createButton.dispatchEvent(new PointerEvent('pointerup', {bubbles:true, pointerId:1, pointerType:'mouse'}));
				await nextFrame();
				if (createButton.classList.contains('chat-input-option-pressed')) fail('question option stayed in pressed border state after pointer up');
				if (createStyle.getPropertyValue('--btn-focus-scale').trim() !== '1') fail('question option button can still shrink on click');
				var recommendedShortcut = card.querySelector('[data-chat-input-nav="recommended"]');
				if (!recommendedShortcut || recommendedShortcut.textContent.indexOf('Recommended and move forward') === -1) fail('recommended shortcut missing');
				if (recommendedShortcut.tagName !== 'DIV' || recommendedShortcut.getAttribute('role') !== 'button' || recommendedShortcut.getAttribute('tabindex') !== '0') fail('recommended shortcut should render as an app card control, not a native button');
				if (!recommendedShortcut.classList.contains('chat-input-option-btn') || !recommendedShortcut.classList.contains('bg-primary') || !recommendedShortcut.classList.contains('hover:border-primary') || !recommendedShortcut.classList.contains('active:border-primary') || !recommendedShortcut.classList.contains('inline-flex') || !recommendedShortcut.classList.contains('w-fit') || recommendedShortcut.classList.contains('w-full') || recommendedShortcut.classList.contains('text-primary-content') || recommendedShortcut.classList.contains('btn') || recommendedShortcut.classList.contains('btn-secondary')) fail('recommended shortcut should be a distinct content-sized primary answer card, not a full-width standalone button');
				var recommendedActions = card.querySelector('[data-chat-input-recommended-actions]');
				if (!recommendedActions || recommendedShortcut.parentElement !== recommendedActions) fail('recommended shortcut should be grouped with the answer choices');
				var navigation = card.querySelector('[data-chat-input-navigation]');
				if (!navigation || !navigation.classList.contains('justify-end') || navigation.querySelector('[data-chat-input-nav="recommended"]')) fail('navigation should contain only pager controls');
				var pager = card.querySelector('[data-chat-input-pager]');
				var position = card.querySelector('[data-chat-input-position]');
				if (!pager || !position || position.parentElement !== pager || pager.children.length !== 3 || pager.children[0] !== position || pager.children[1].getAttribute('data-chat-input-nav') !== 'previous' || pager.children[2].getAttribute('data-chat-input-nav') !== 'next') fail('question position should sit before adjacent Previous and Next controls');
				var recommendedStyle = getComputedStyle(recommendedShortcut);
				if (recommendedStyle.color !== 'rgb(17, 24, 39)') fail('recommended shortcut text should be black on the purple answer card');
				if (getComputedStyle(recommendedShortcut).getPropertyValue('--btn-focus-scale').trim() !== '1') fail('recommended shortcut can still shrink on click');
				var previousTheme = document.documentElement.getAttribute('data-theme') || '';
				var previousColorTheme = document.documentElement.getAttribute('data-color-theme') || '';
				document.documentElement.setAttribute('data-theme', 'light');
				document.documentElement.setAttribute('data-color-theme', 'openvibely-light');
				await nextFrame();
				var lightCustomButton = card.querySelector('[data-chat-input-custom-option]');
				var lightCustomStyle = getComputedStyle(lightCustomButton);
				var lightRecommendedStyle = getComputedStyle(recommendedShortcut);
				if (lightCustomStyle.color === 'rgb(255, 255, 255)' || lightCustomStyle.color === lightCustomStyle.backgroundColor) fail('custom answer option text is unreadable in light mode');
				if (lightRecommendedStyle.color !== 'rgb(17, 24, 39)') fail('recommended shortcut text should stay black in light mode');
				if (previousTheme) document.documentElement.setAttribute('data-theme', previousTheme); else document.documentElement.removeAttribute('data-theme');
				if (previousColorTheme) document.documentElement.setAttribute('data-color-theme', previousColorTheme); else document.documentElement.removeAttribute('data-color-theme');
				await nextFrame();
				var nextButton = card.querySelector('button[data-chat-input-nav="next"]');
				if (!nextButton || !nextButton.disabled) fail('Next should stay disabled until the user selects an answer');
				recommendedShortcut.click();
				await waitFor(function() { return !recommendedShortcut.disabled && /Unable to submit answer/.test(card.textContent); }, 'failed recommended shortcut did not re-enable controls with retry error');
				if (card.getAttribute('data-submitted') === 'true') fail('failed recommended shortcut left card marked submitted');
				createButton.click();
				await waitFor(function() { return /2 of 2/.test(card.textContent) && card.textContent.indexOf('Which fallback should be used?') !== -1; }, 'clicking recommended option did not show the second question');
				card.querySelector('button[data-chat-input-nav="previous"]').click();
				await waitFor(function() { return /1 of 2/.test(card.textContent); }, 'Previous did not return to the first question');
				nextButton = card.querySelector('button[data-chat-input-nav="next"]');
				if (!nextButton || nextButton.disabled) fail('Next should be enabled after the user selects the recommended option');
				if (!createButton.classList.contains('chat-input-option-selected')) fail('selected option did not use the local selected styling');
				if (getComputedStyle(createButton).borderColor !== initialBorderColor) fail('selected option changed border color and can flash like page cards');
				if (createButton.classList.contains('btn-primary')) fail('selected option fell back to distracting primary button styling');
				nextButton.click();
				await waitFor(function() { return /2 of 2/.test(card.textContent) && card.textContent.indexOf('Which fallback should be used?') !== -1; }, 'Next did not show the second question');
				card.querySelector('button[data-chat-input-nav="previous"]').click();
				var notNowButton = buttons.filter(function(button) { return button.textContent.indexOf('Not now') !== -1; })[0];
				notNowButton.click();
				await waitFor(function() { return /2 of 2/.test(card.textContent); }, 'option selection did not automatically advance');
				var customButton = card.querySelector('[data-chat-input-question-id="fallback"] [data-chat-input-custom-option]');
				if (!customButton) fail('custom-answer option missing');
				customButton.click();
				var customInput = card.querySelector('[data-chat-input-question-id="fallback"] [data-chat-input-custom-answer]');
				if (!customInput || customInput.closest('[data-chat-input-custom-wrap]').classList.contains('hidden')) fail('custom-answer input was not shown');
				customInput.value = 'Use the deployment-configured fallback';
				customInput.dispatchEvent(new Event('input', {bubbles:true}));
				var submitButton = card.querySelector('button[data-chat-input-nav="submit"]');
				if (!submitButton || submitButton.disabled) fail('Submit was not enabled after every question was answered');
				submitButton.click();
				await waitFor(function() { return card.getAttribute('data-completed') === 'true'; }, 'successful submission did not complete card');
				await nextFrame();
				buttons = Array.prototype.slice.call(card.querySelectorAll('[data-chat-input-option]'));
			if (buttons.length !== 0) fail('completed question retained actionable controls');
				if (!/Asked 2 questions/.test(card.textContent)) fail('completed question summary was not shown');
				if (!/Not now/.test(card.textContent) || !/Use the deployment-configured fallback/.test(card.textContent)) fail('selected answers were not retained in collapsed details');
				if (card.querySelector('details').open) fail('completed question details should start collapsed');
			await report('pass', 'input request browser interaction passed');
		} catch (error) {
			await report('fail', String(error && error.stack || error));
		}
	})();
	</script>`, string(eventJSON), pending.ID, pending.ID)

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
			body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			firstAnswerBody.Store(string(body))
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
	require.JSONEq(t, `{"project_id":"`+project.ID+`","answers":[{"question_id":"create","label":"Create task"},{"question_id":"fallback","label":"Provider-aware"}]}`, firstAnswerBody.Load().(string))
	require.Equal(t, int32(2), answerAttempts.Load())
}

func TestChatInputRequestBrowserRefreshRestoresPendingControls(t *testing.T) {
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
			async function waitFor(predicate, message) {
				var deadline = Date.now() + 5000;
				while (Date.now() < deadline) {
					var value = predicate();
					if (value) return value;
					await new Promise(function(resolve) { setTimeout(resolve, 25); });
				}
				throw new Error(message);
			}
			try {
				var card = await waitFor(function() { return document.querySelector('[data-chat-input-request-id="%s"]'); }, 'pending input request was not restored after refresh');
				if (card.querySelectorAll('[data-chat-input-option]').length !== 3) throw new Error('restored request did not contain options and a custom-answer control');
				await report('pass', 'refresh restored pending input controls');
			} catch (error) {
				await report('fail', String(error && error.stack || error));
			}
		})();
	</script>`
	refreshRunner = fmt.Sprintf(refreshRunner, pending.ID)

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
