package pages

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
)

func TestBrowserFunctional_ChatLivePendingInputsUseSharedFragment(t *testing.T) {
	page := renderReconnectComponent(t, ChatContent(nil, nil, "project-focus", nil, nil, false, false, 30))
	snapshots := map[string]string{}
	for _, mode := range []models.ThreadInputMode{models.ThreadInputModeQueued, models.ThreadInputModeSteering} {
		for _, attachments := range []bool{false, true} {
			id := fmt.Sprintf("%s-%t", mode, attachments)
			input := models.ThreadInput{ID: id, Content: "Preview <with> special characters", InputMode: mode}
			if attachments {
				input.AttachmentSessionID = "attachments"
			}
			snapshots[id] = renderReconnectComponent(t, components.ChatComposerQueuedInputRows([]models.ThreadInput{input}, func(input models.ThreadInput) string { return "/chat/queued/" + input.ID + "/steer" }))
		}
	}
	snapshots["empty"] = renderReconnectComponent(t, components.ChatComposerQueuedInputRows(nil, nil))
	encoded, err := json.Marshal(snapshots)
	if err != nil {
		t.Fatal(err)
	}
	prelude := `<script src="` + static.URL("vendor/htmx.min.js") + `"></script><script>window.__realSwap = htmx.swap;</script>` + reconnectFixturePrelude(t, map[string]string{"initial": page}) + `<script>
 htmx.swap = window.__realSwap;
 window.__pendingSnapshots = ` + string(encoded) + `;
 window.__pendingCalls = [];
 window.__deferredPending = [];
 var originalFetch = window.fetch;
 window.fetch = function(url, options) {
   if (url.indexOf('/chat/pending-inputs?') !== 0) return originalFetch(url, options);
   window.__pendingCalls.push(url);
   var html = window.__pendingSnapshots[window.__pendingPhase];
   var response = {ok:true, text:function() {return Promise.resolve(html);}};
   if (window.__deferPending) return new Promise(function(resolve) {window.__deferredPending.push(function(){resolve(response);});});
   return Promise.resolve(response);
 };
 </script>`
	script := `<main id="reconnect-result"></main><script>
 window.addEventListener('DOMContentLoaded', async function() {
  var result = document.getElementById('reconnect-result');
  function check(value, message) {if (!value) throw new Error(message);}
  function emit(id, mode, extra) {
   window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail:Object.assign({type:mode === 'steering' ? 'chat_turn_steered' : 'chat_new_message', queued:mode === 'queued', project_id:'project-focus', exec_id:id, message:'event preview must not render', source:'slack'}, extra || {})}));
  }
  try {
   await __wait(30);
   var transcript = document.getElementById('chat-messages');
   var form = document.getElementById('chat-form');
   for (var mode of ['queued', 'steering']) {
    for (var attachments of [false, true]) {
     var id = mode+'-'+attachments;
     __pendingPhase = id;
     emit(id, mode, {has_attachments:attachments});
     emit(id, mode, {has_attachments:attachments});
     await __wait(30);
     var expected = document.createElement('template');
     expected.innerHTML = __pendingSnapshots[id];
     var row = document.getElementById('thread-input-'+id);
     check(row && row.outerHTML === expected.content.querySelector('[data-thread-input-id]').outerHTML, 'live fragment mismatch '+id);
     check(row.querySelector('[aria-label="Edit message"]'), 'missing Edit');
     check(row.querySelector('[hx-post="/thread-inputs/'+id+'/cancel"][hx-target="#thread-input-'+id+'"]'), 'missing cancel target');
     check(!!row.querySelector('.badge') === attachments, 'attachment badge');
     check(!!row.querySelector('[hx-post="/chat/queued/'+id+'/steer"]') === (mode === 'queued'), 'steer action');
     var before = row.outerHTML;
     emit(id, mode);
     window.dispatchEvent(new CustomEvent('sse-live-connected', {detail:{reconnected:true}}));
     await __wait(30);
     check(document.querySelectorAll('[data-thread-input-id]').length === 1, 'duplicate pending row');
     check(document.getElementById('thread-input-'+id).outerHTML === before, 'reconnect changed controls');
    }
   }
   check(transcript === document.getElementById('chat-messages') && form === document.getElementById('chat-form'), 'composer/transcript remounted');
   var calls = __pendingCalls.length;
   emit('foreign', 'queued', {project_id:'other-project'});
   emit('foreign', 'steering', {project_id:'other-project'});
   await __wait(10);
   check(__pendingCalls.length === calls, 'foreign project refreshed');
   __deferPending = true;
   __pendingPhase = 'queued-false'; emit('race', 'queued');
   __pendingPhase = 'empty';
   window.dispatchEvent(new CustomEvent('sse-chat-live-event', {detail:{type:'chat_thread_input_cancelled', project_id:'project-focus', pending_input_id:'race'}}));
   __deferredPending.pop()(); await __wait(10);
   __deferredPending.pop()(); await __wait(10);
   check(!document.querySelector('[data-thread-input-id]'), 'stale response restored cancelled row');
   __pendingPhase = 'queued-true'; emit('navigation', 'queued');
   var root = document.getElementById('chat-page-root');
   root.setAttribute('data-project-id', 'other-project');
   __deferredPending.pop()(); await __wait(10);
   check(!document.querySelector('[data-thread-input-id]'), 'late response crossed projects');
   root.setAttribute('data-project-id', 'project-focus');
   __deferPending = false;
   calls = __pendingCalls.length;
   emit('ordinary-execution', '', {queued:false, message:'ordinary prompt'});
   emit('ordinary-execution', '', {queued:false, message:'ordinary prompt'});
   await __wait(30);
   check(document.querySelectorAll('#chat-execution-ordinary-execution').length === 1, 'execution replay duplicated');
   check(document.getElementById('chat-execution-ordinary-execution').textContent.includes('ordinary prompt'), 'ordinary execution missing');
   check(__pendingCalls.length === calls, 'ordinary event refreshed pending rows');
   check(__pendingCalls.every(function(url){return url.endsWith('project_id=project-focus');}), 'incorrect project query');
   result.setAttribute('data-test-result', 'pass');
  } catch(err) {result.setAttribute('data-test-result', 'fail');result.setAttribute('data-error', err.stack || err.message);}
 });
 </script>`
	runReconnectChromeFixture(t, prelude+page+script)
}
