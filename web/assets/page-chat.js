// Page adapter: identity, endpoints and panel layout only. Conversation behavior
// belongs to the same controller used by Chat and task threads.
(function () {
 'use strict';
 window.initPageChat = function () {
  const panel=document.getElementById('page-side-chat');
  const root=panel && panel.closest('[data-page-chat-root]');
  if(!root || root._pageChatMounted)return;
  if(window.openVibelyPageChat)window.openVibelyPageChat.destroy();
  root._pageChatMounted=true;
  const opener=root.querySelector('[data-page-chat-toggle]');
  const abort=new AbortController();
  const on=(el,name,fn)=>el.addEventListener(name,fn,{signal:abort.signal});
  const close=()=>{side.setOpen(false);opener.focus({preventScroll:true});};
  const side=window.createSidePanel({root,panel,opener,storageKey:'page-chat-width',escapeDocked:true,
   onClose:close,onLayout:()=>opener.setAttribute('aria-label',panel.hidden?'Open page chat':'Collapse page chat')});
  const conversation=window.openVibelyThreadController.mount({root,panel,
   messages:panel.querySelector('[data-chat-transcript]'),projectID:root.dataset.projectId,
   transcriptURL:root.dataset.pageChatTranscriptUrl,stateKey:'page-chat-scroll-'+root.dataset.pageChatTranscriptUrl,
   controls:['pending-thread-inputs','page-chat-form-primary-action']});
  on(opener,'click',()=>{side.setOpen(panel.hidden);if(!panel.hidden){conversation.hydrate();conversation.refresh();panel.querySelector('textarea').focus();}});
  on(panel.querySelector('[data-page-chat-close]'),'click',close);
  function destroy(){abort.abort();conversation.destroy();side.destroy();root._pageChatMounted=false;}
  on(document,'htmx:beforeCleanupElement',e=>{if(e.target===root || e.target.contains(root))destroy();});
  on(window,'pagehide',destroy);
  window.openVibelyPageChat={refresh:conversation.refresh,destroy};
 };
 document.addEventListener('DOMContentLoaded',window.initPageChat);
 document.addEventListener('htmx:afterSwap',window.initPageChat);
})();
