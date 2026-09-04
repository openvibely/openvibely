package handler

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/openvibely/openvibely/internal/repository"
	"github.com/openvibely/openvibely/internal/service"
	"github.com/stretchr/testify/require"
)

type browserXAPI struct {
	mu          sync.RWMutex
	mentionsErr error
}

func (f *browserXAPI) Me(context.Context) (service.XUser, error) {
	return service.XUser{ID: "browser-bot", Username: "openvibely"}, nil
}
func (f *browserXAPI) Mentions(context.Context, string, string, string) (service.XMentionsResponse, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.mentionsErr != nil {
		return service.XMentionsResponse{}, f.mentionsErr
	}
	var out service.XMentionsResponse
	out.Meta.NewestID = "100"
	return out, nil
}
func (f *browserXAPI) Post(context.Context, string, string) (string, error) {
	return "browser-post", nil
}
func (f *browserXAPI) setMentionsError(err error) {
	f.mu.Lock()
	f.mentionsErr = err
	f.mu.Unlock()
}

func TestXChannelsProductionHandlersInChrome(t *testing.T) {
	chrome := findChromeForBrowserTest(t)
	if chrome == "" {
		t.Skip("Chrome/Chromium executable not found")
	}
	htmx, err := os.ReadFile(filepath.Join("..", "..", "web", "templates", "components", "testdata", "htmx-2.0.4.min.js"))
	require.NoError(t, err)

	h, e, _, db := setupTestHandlerWithDB(t)
	auth := repository.NewXAuthRepo(db)
	h.SetXRepositories(auth, repository.NewXUserProjectRepo(db), repository.NewXTaskContextRepo(db), repository.NewXInboundReceiptRepo(db))
	projectOne := createProject(t, h, "X Browser One")
	projectTwo := createProject(t, h, "X Browser Two")
	api := &browserXAPI{}
	originalFactory := newXAPIClientForSettings
	newXAPIClientForSettings = func(service.XCredentials) service.XAPI { return api }
	t.Cleanup(func() {
		newXAPIClientForSettings = originalFactory
		h.StopXService()
	})

	results := make(chan string, 16)
	e.POST("/browser-result", func(c echo.Context) error {
		status := c.QueryParam("status")
		message := c.QueryParam("message")
		switch message {
		case "fail-mentions":
			api.setMentionsError(io.ErrUnexpectedEOF)
		case "restore-mentions":
			api.setMentionsError(nil)
		case "stop-service":
			h.StopXService()
		}
		results <- status + ":" + message
		return c.NoContent(http.StatusNoContent)
	})

	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function fail(message) { throw new Error(message); }
	  function waitFor(check, label) { return new Promise(function(resolve, reject) { var started = performance.now(); (function poll() { try { if (check()) return resolve(); } catch (error) { return reject(error); } if (performance.now() - started > 15000) return reject(new Error('timed out waiting for ' + label)); setTimeout(poll, 20); })(); }); }
	  function refreshChannels(projectID, label) {
	    return new Promise(function(resolve, reject) {
	      var timer = setTimeout(function() { reject(new Error('timed out waiting for ' + label + ' request')); }, 15000);
	      Promise.resolve(htmx.ajax('GET', '/channels?project_id=' + encodeURIComponent(projectID), {target:'#channels-container', swap:'outerHTML'})).then(function(value) {
	        clearTimeout(timer);
	        resolve(value);
	      }, function(error) {
	        clearTimeout(timer);
	        reject(new Error(label + ' request failed: ' + String(error && error.message || error)));
	      });
	    });
	  }
	  async function runConnectionTest(expected) {
	    var modal = document.getElementById('x_config_modal');
	    var button = modal && modal.querySelector('button[hx-post="/channels/x/test"]');
	    var feedback = modal && modal.querySelector('#x-test-feedback');
	    if (!button || !feedback) fail('missing production connection-test control');
	    htmx.process(modal);
	    button.click();
	    await waitFor(function() { return feedback.textContent.indexOf(expected) >= 0; }, 'production connection test ' + expected);
	    return feedback.innerHTML;
	  }
	  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });  (async function() {
    if (!window.htmx || htmx.version !== '2.0.4') fail('real HTMX 2.0.4 was not loaded');
    var add = document.querySelector('button[onclick="openXConfigModal()"]');
    if (!add) fail('missing production Add Channel X action');
    add.click();
    var modal = document.getElementById('x_config_modal');
    if (!modal || !modal.open) fail('production X modal did not open');
    var form = modal.querySelector('form[action="/channels/x/configure"]');
    ['x_consumer_key','x_consumer_secret','x_access_token','x_access_token_secret'].forEach(function(name) { form.elements[name].value = name + '-value'; });
    form.elements.x_poll_interval_seconds.value = '300';
    form.requestSubmit();
    await waitFor(function() { return document.querySelector('[data-channel-type="x"]') && document.body.textContent.indexOf('Connected') >= 0; }, 'production configure refresh');

    document.querySelector('[data-channel-type="x"]').click();
    modal = document.getElementById('x_config_modal');
    var testButton = modal.querySelector('button[hx-post="/channels/x/test"]');
    if (!testButton) fail('missing production connection-test control');
	    var successFeedback = await runConnectionTest('Connection successful');
	    if (successFeedback.indexOf('Connection successful') < 0) fail('unexpected production connection success feedback: ' + successFeedback);
	    await report('progress', 'fail-mentions');
	    var failureFeedback = await runConnectionTest('Connection failed');
	    if (failureFeedback.indexOf('Connection failed') < 0) fail('unexpected production mention-read failure feedback: ' + failureFeedback);
	    await report('progress', 'restore-mentions');
    var authForm = modal.querySelector('form[action="/channels/x/authorized-users"]');
    authForm.elements.x_user_id.value = '123';
    authForm.elements.x_username.value = 'alice';
    authForm.requestSubmit();
    await waitFor(function() { return document.body.textContent.indexOf('@alice') >= 0; }, 'production authorized-user refresh');

	    await refreshChannels('` + projectTwo.ID + `', 'production project-two refresh');
	    await waitFor(function() { var input = document.querySelector('#x_config_modal input[name="project_id"]'); return input && input.value === '` + projectTwo.ID + `'; }, 'production project-two refresh');
	    var projectTwoModal = document.querySelector('#x_config_modal');
	    if (projectTwoModal.textContent.indexOf('@alice') >= 0 || projectTwoModal.textContent.indexOf('No users authorized') < 0) fail('project-one authorization leaked into project two');
    await report('progress', 'stop-service');
	    await refreshChannels('` + projectTwo.ID + `', 'production disconnected-state refresh');
    await waitFor(function() { return document.body.textContent.indexOf('Configured, polling offline') >= 0; }, 'production disconnected state');

	    await refreshChannels('` + projectOne.ID + `', 'production return-to-project-one refresh');
    await waitFor(function() { return document.querySelector('[data-channel-type="x"]'); }, 'return to project one');
    var deleteButton = document.querySelector('[data-channel-type="x"] button.text-error');
    deleteButton.click();
    var confirm = document.querySelector('#delete_channel_confirm_modal .btn-error');
    if (!confirm) fail('production delete confirmation did not open');
	    var removeFinished = new Promise(function(resolve, reject) {
	      var timer = setTimeout(function() { reject(new Error('timed out waiting for production remove request')); }, 15000);
	      document.body.addEventListener('htmx:afterRequest', function onRemove(event) {
	        var path = event.detail && event.detail.requestConfig && event.detail.requestConfig.path;
	        if (path !== '/channels/x/remove') return;
	        document.body.removeEventListener('htmx:afterRequest', onRemove);
	        clearTimeout(timer);
	        if (!event.detail.successful) return reject(new Error('production remove request failed'));
	        resolve();
	      });
	    });
	    confirm.click();
	    await removeFinished;
	    await refreshChannels('` + projectOne.ID + `', 'production remove refresh');
	    await waitFor(function() { return !document.querySelector('[data-channel-type="x"]') && document.querySelector('button[onclick="openXConfigModal()"]'); }, 'production remove refresh');    report('pass', 'complete');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/htmx-2.0.4.min.js" {
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(htmx)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/channels" {
			recorder := httptest.NewRecorder()
			e.ServeHTTP(recorder, r)
			for key, values := range recorder.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			w.WriteHeader(recorder.Code)
			body := recorder.Body.String()
			body = strings.Replace(body, "https://unpkg.com/htmx.org@2.0.4", "/htmx-2.0.4.min.js", 1)
			if r.Header.Get("HX-Request") == "" {
				body = strings.Replace(body, "</body>", runner+"</body>", 1)
			}
			_, _ = io.WriteString(w, body)
			return
		}
		e.ServeHTTP(w, r)
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "x-production-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	require.NoError(t, err)
	defer stderrFile.Close()
	profileDir := t.TempDir()
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profileDir, server.URL+"/channels?project_id="+projectOne.ID,
	)
	cmd.Stderr = stderrFile
	require.NoError(t, startHandlerBrowserProcess(cmd))
	defer stopHandlerBrowserProcess(cmd)

	var outcome string
	deadline := time.After(60 * time.Second)
	for outcome == "" {
		select {
		case value := <-results:
			if strings.HasPrefix(value, "pass:") || strings.HasPrefix(value, "fail:") {
				outcome = value
			}
		case <-deadline:
			outcome = "fail:timed out"
		}
	}
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("production X Channels browser regression failed: %s\nChrome stderr:\n%s", outcome, stderr)
	}
	values, err := h.settingsRepo.GetMany(context.Background(), []string{service.XSettingConsumerKey, service.XSettingAccessToken, service.XSettingSinceID})
	require.NoError(t, err)
	require.Empty(t, values[service.XSettingConsumerKey])
	require.Empty(t, values[service.XSettingAccessToken])
	require.Empty(t, values[service.XSettingSinceID])
	users, err := auth.ListByProject(context.Background(), projectOne.ID)
	require.NoError(t, err)
	require.Len(t, users, 1)
	users, err = auth.ListByProject(context.Background(), projectTwo.ID)
	require.NoError(t, err)
	require.Empty(t, users)
}
