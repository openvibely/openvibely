package pages

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/service"
)

func TestXChannelsHTMXFlowInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	htmx, err := os.ReadFile("../components/testdata/htmx-2.0.4.min.js")
	if err != nil {
		t.Fatalf("read pinned HTMX fixture: %v", err)
	}

	type browserState struct {
		sync.Mutex
		configured bool
		connected  bool
		lastError  string
		authorized map[string][]models.XAuthorizedUser
	}
	state := &browserState{authorized: map[string][]models.XAuthorizedUser{}}
	viewFor := func(projectID string) ChannelsSettingsView {
		state.Lock()
		defer state.Unlock()
		status := service.XConnectionStatus{Configured: state.configured, Connected: state.connected, Running: state.connected, Username: "openvibely", LastError: state.lastError}
		return ChannelsSettingsView{
			CurrentProjectID: projectID, HasXChannel: state.configured, XStatus: status,
			XPollIntervalSeconds: "30", XSendResponses: true,
			XAuthorizedUsers: append([]models.XAuthorizedUser(nil), state.authorized[projectID]...),
		}
	}
	renderFragment := func(projectID string) string {
		var out bytes.Buffer
		if err := SettingsContent(viewFor(projectID)).Render(context.Background(), &out); err != nil {
			t.Fatalf("render Channels fragment: %v", err)
		}
		return out.String()
	}

	result := make(chan string, 16)
	var server *httptest.Server
	runner := `<script>
window.addEventListener('DOMContentLoaded', function() {
  function report(status, message) { return fetch('/browser-result?status=' + encodeURIComponent(status) + '&message=' + encodeURIComponent(message || ''), {method:'POST'}); }
  function fail(message) { throw new Error(message); }
  function waitFor(check, label) { return new Promise(function(resolve, reject) { var started = performance.now(); (function poll() { try { if (check()) return resolve(); } catch (error) { return reject(error); } if (performance.now() - started > 4000) return reject(new Error('timed out waiting for ' + label)); setTimeout(poll, 20); })(); }); }
  window.addEventListener('error', function(event) { report('fail', String(event.error && event.error.stack || event.message)); });
  (async function() {
    if (!window.htmx || htmx.version !== '2.0.4') fail('real HTMX 2.0.4 was not loaded');
    var add = document.querySelector('button[onclick="openXConfigModal()"]');
    if (!add) fail('missing Add Channel X action');
    add.click();
    var modal = document.getElementById('x_config_modal');
    if (!modal || !modal.open) fail('X configuration modal did not open');
    var form = modal.querySelector('form[action="/channels/x/configure"]');
    ['x_consumer_key','x_consumer_secret','x_access_token','x_access_token_secret'].forEach(function(name) { form.elements[name].value = name + '-value'; });
    form.elements.x_poll_interval_seconds.value = '45';
    form.requestSubmit();
    await waitFor(function() { return document.querySelector('[data-channel-type="x"]') && document.body.textContent.indexOf('Connected') >= 0; }, 'connected X card after HTMX refresh');
    await report('progress', 'configured');

    document.querySelector('[data-channel-type="x"]').click();
    modal = document.getElementById('x_config_modal');
    if (!modal.open) fail('configured X card did not reopen modal');
    var authForm = modal.querySelector('form[action="/channels/x/authorized-users"]');
    authForm.elements.x_user_id.value = '123';
    authForm.elements.x_username.value = 'alice';
    authForm.requestSubmit();
    await waitFor(function() { return document.body.textContent.indexOf('@alice') >= 0; }, 'authorized X user after HTMX refresh');
    await report('progress', 'authorized');

    htmx.ajax('GET', '/channels?project_id=project-two', {target:'#channels-container', swap:'outerHTML'});
    await waitFor(function() { var input = document.querySelector('#x_config_modal input[name="project_id"]'); return input && input.value === 'project-two'; }, 'project-two Channels fragment');
    if (document.body.textContent.indexOf('@alice') >= 0) fail('project-one X authorization leaked into project two');
    await report('progress', 'project-switched');

    htmx.ajax('POST', '/browser-disconnect?project_id=project-two', {swap:'none'});
    await waitFor(function() { return document.body.textContent.indexOf('Configured, polling offline') >= 0 && document.body.textContent.indexOf('mention access revoked') >= 0; }, 'disconnected X readiness state');
    report('pass', '');
  })().catch(function(error) { report('fail', String(error && error.stack || error)); });
});
</script>`

	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/htmx-2.0.4.min.js":
			w.Header().Set("Content-Type", "application/javascript")
			_, _ = w.Write(htmx)
		case "/browser-result":
			result <- r.URL.Query().Get("status") + ":" + r.URL.Query().Get("message")
			w.WriteHeader(http.StatusNoContent)
		case "/channels/x/configure":
			_ = r.ParseForm()
			for _, key := range []string{"x_consumer_key", "x_consumer_secret", "x_access_token", "x_access_token_secret"} {
				if !strings.HasSuffix(r.FormValue(key), "-value") {
					http.Error(w, "missing credential field", http.StatusBadRequest)
					return
				}
			}
			if r.FormValue("x_poll_interval_seconds") != "45" {
				http.Error(w, "poll interval not submitted", http.StatusBadRequest)
				return
			}
			state.Lock()
			state.configured, state.connected, state.lastError = true, true, ""
			state.Unlock()
			w.Header().Set("HX-Trigger", "channels-refresh")
			w.WriteHeader(http.StatusNoContent)
		case "/channels/x/authorized-users":
			_ = r.ParseForm()
			projectID := r.FormValue("project_id")
			state.Lock()
			state.authorized[projectID] = append(state.authorized[projectID], models.XAuthorizedUser{ID: "auth-1", ProjectID: projectID, XUserID: r.FormValue("x_user_id"), Username: r.FormValue("x_username")})
			state.Unlock()
			w.Header().Set("HX-Trigger", "channels-refresh")
			w.WriteHeader(http.StatusNoContent)
		case "/browser-disconnect":
			state.Lock()
			state.configured, state.connected, state.lastError = true, false, "mention access revoked"
			state.Unlock()
			w.Header().Set("HX-Trigger", "channels-refresh")
			w.WriteHeader(http.StatusNoContent)
		case "/channels":
			projectID := r.URL.Query().Get("project_id")
			if projectID == "" {
				projectID = "project-one"
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(renderFragment(projectID)))
		default:
			page := "<!doctype html><html><head><meta charset=\"utf-8\"><script src=\"/htmx-2.0.4.min.js\"></script>" + runner + "</head><body>" + renderFragment("project-one") + "</body></html>"
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(page))
		}
	}))
	defer server.Close()

	stderrPath := filepath.Join(t.TempDir(), "x-channels-browser.stderr")
	stderrFile, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	defer stderrFile.Close()
	profileDir, err := os.MkdirTemp("", "openvibely-x-channels-browser-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(profileDir)
	cmd := exec.Command(chrome,
		"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer", "--disable-dev-shm-usage",
		"--disable-background-networking", "--disable-background-timer-throttling", "--no-first-run", "--no-default-browser-check",
		"--user-data-dir="+profileDir, server.URL,
	)
	cmd.Stderr = stderrFile
	if err := startBrowserProcess(cmd); err != nil {
		t.Fatalf("start Chrome: %v", err)
	}
	var outcome, progress string
	deadline := time.After(25 * time.Second)
	for outcome == "" {
		select {
		case value := <-result:
			if strings.HasPrefix(value, "progress:") {
				progress = strings.TrimPrefix(value, "progress:")
				continue
			}
			outcome = value
		case <-deadline:
			outcome = "fail:timed out; last progress=" + progress
		}
	}
	stopBrowserProcess(cmd)
	if !strings.HasPrefix(outcome, "pass:") {
		stderr, _ := os.ReadFile(stderrPath)
		t.Fatalf("X Channels browser regression failed: %s\nLast progress: %s\nChrome stderr:\n%s", outcome, progress, stderr)
	}
}
