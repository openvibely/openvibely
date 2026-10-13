package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_ChannelConfigDismissal(t *testing.T) {
	content := renderSettingsContentForWebhookTest(t, nil)
	fixture := `<main id="reconnect-result"></main>` + content + `<script>
 (async function() {
 const field = id => document.getElementById(id);
 const assert = (ok, message) => { if (!ok) throw Error(message); };
 const wait = () => new Promise(resolve => setTimeout(resolve, 50));
 const dialogs = ['channel_modal', 'github_config_modal', 'slack_config_modal', 'discord_config_modal', 'email_config_modal'];
 const otherDialogs = ['webhook_modal', 'outbound_targets_modal'];
 const allDialogs = dialogs.concat(otherDialogs);
 const request = (path, successful, source, trigger) => {
   const detail = {successful, xhr: {getResponseHeader: () => trigger || ''}};
   if (source === 'requestConfig') detail.requestConfig = {path};
   else if (source === 'pathInfo') detail.pathInfo = {requestPath: path};
   else detail.path = path;
   document.body.dispatchEvent(new CustomEvent('htmx:afterRequest', {detail}));
 };
 const openAll = () => allDialogs.forEach(id => { if (!field(id).open) field(id).show(); });
 const assertOnlyClosed = id => allDialogs.forEach(other => assert(field(other).open === (other !== id), 'Wrong dialog state: ' + other + ' after ' + id));
 try {
   // Use each rendered form's actual endpoint to catch route/form drift.
   for (const id of dialogs) {
     const modal = field(id);
     const path = modal.querySelector('form[hx-post]').getAttribute('hx-post');
     for (const source of ['requestConfig', 'pathInfo', 'path']) {
       openAll();
       field('discord_bot_token').value = 'unsaved-token';
       request(path + '?project_id=project-1', false, source);
       allDialogs.forEach(other => assert(field(other).open, 'Failed save closed ' + other));
       assert(field('discord_bot_token').value === 'unsaved-token', 'Failure reset Discord draft');
       request(path + '?project_id=project-1', true, source);
       assertOnlyClosed(id);
       await wait();
       if (id === 'discord_config_modal') assert(field('discord_bot_token').value === field('discord_bot_token').defaultValue, 'Successful save did not reset Discord');
     }
     for (const button of modal.querySelectorAll('button')) {
       if (!button.matches('.ov-modal-close') && !['Cancel', 'close'].includes(button.textContent.trim())) continue;
       openAll();
       field('discord_bot_token').value = 'unsaved-token';
       button.click();
       assertOnlyClosed(id);
       await wait();
       if (id === 'discord_config_modal') assert(field('discord_bot_token').value === field('discord_bot_token').defaultValue, 'Dismissal did not reset Discord');
     }
   }
   openAll();
   for (const path of ['/channels/telegram/test', '/channels/github/configure/extra', '/channels/slack/test', '/channels/email/test', '/channels/discord/test']) {
     request(path, true);
     allDialogs.forEach(id => assert(field(id).open, 'Unrelated request closed ' + id));
   }
   request('/channels/webhooks/endpoint-1', false);
   allDialogs.forEach(id => assert(field(id).open, 'Failed webhook save closed ' + id));
   request('/channels/webhooks/endpoint-1', true);
   assertOnlyClosed('webhook_modal');
   openAll();
   request('/channels/send-message-explicit-targets', false, 'path', 'outbound-targets-card-refresh');
   request('/channels/send-message-explicit-targets', true);
   allDialogs.forEach(id => assert(field(id).open, 'Outbound save without successful refresh closed ' + id));
   request('/channels/send-message-explicit-targets', true, 'path', 'outbound-targets-card-refresh');
   assertOnlyClosed('outbound_targets_modal');
   field('reconnect-result').setAttribute('data-test-result', 'pass');
 } catch (error) {
   field('reconnect-result').setAttribute('data-test-result', 'fail');
   field('reconnect-result').setAttribute('data-test-error', String(error.stack));
 }
 })();
 </script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html><body>" + fixture + "</body></html>"))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL, "channel-config-dismissal", func(b *composerFocusCDP) {
		b.waitFor("channel dialog dismissal", `(function(){var result=document.getElementById('reconnect-result');return result && (result.getAttribute('data-test-error') || result.getAttribute('data-test-result')) || 'pending';})()`, "pass")
	})
}
