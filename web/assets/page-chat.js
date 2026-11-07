// Page-specific adapter for the shared transcript controller, stream resumer,
// composer and question controls. The iframe does not own this conversation.
(function () {
 'use strict';
 window.initPageChat = function () {
  const panel = document.getElementById('page-side-chat');
  if (!panel) return;
  const root = panel.closest('[data-page-chat-root]');
  if (!root || root._pageChatMounted) return;
  if (window.openVibelyPageChat) window.openVibelyPageChat.destroy();
  root._pageChatMounted = true;
  const opener = root.querySelector('[data-page-chat-toggle]');
  const abort = new AbortController();
  const messages = () => panel.querySelector('#page-chat-messages');
  const stateKey = 'page-chat-scroll-' + root.dataset.pageChatTranscriptUrl;
  let dead = false, inFlight = false, dirty = false, generation = 0, fingerprint = '';
  const on = (el, name, fn) => el.addEventListener(name, fn, {signal:abort.signal});
  function close() { side.setOpen(false); opener.focus({preventScroll:true}); }
  const side = window.createSidePanel({root, panel, opener, storageKey:'page-chat-width', escapeDocked:true,
   onClose:close, onLayout:() => opener.setAttribute('aria-label', panel.hidden ? 'Open page chat' : 'Collapse page chat')});
  function hydrate() {
   const box = messages();
   const tracker = window.resolveScrollTracker('scrollTracker_page-chat-messages', box);
   const rendering = window.applyChatBubbleTransforms ? window.applyChatBubbleTransforms(box) : window.cleanAssistantMessages(box);
   window._initThreadStreaming();
   window.initChatInputRequestCards(box);
   window.initChatEarlierLoader(box);
   return window.openVibelyThreadController.restore({messages:box,tracker,getTracker:()=>tracker,stateKey,renderPromise:rendering});
  }
  on(opener,'click',()=>{side.setOpen(panel.hidden);if(!panel.hidden){hydrate();refresh();panel.querySelector('textarea').focus();}});
  on(panel.querySelector('[data-page-chat-close]'),'click',close);
  function stamp(box) {
   return Array.from(box.querySelectorAll('[data-execution-pair]')).map(p=>[p.id,p.dataset.execStatus,p.dataset.execStatus==='running'?'':p.dataset.executionRevision].join(':')).join('|');
  }
  function update(old, next, preserve) {
   if (!old || !next) return;
   window.Idiomorph.morph(old,next,{morphStyle:'outerHTML',callbacks:{
    beforeNodeMorphed:(from,to)=>{
     if(from.id==='page-chat-messages') Array.from(from.attributes).filter(a=>a.name.startsWith('data-earlier-')).forEach(a=>to.setAttribute(a.name,a.value));
     if (preserve && from.matches && from.matches('[data-execution-pair]') && from.id===to.id) {
      if (from.dataset.execStatus==='running' && to.dataset.execStatus==='running') return false;
      if (from.dataset.execStatus!=='running' && from.dataset.executionRevision===to.dataset.executionRevision) return false;
      from.querySelectorAll('[data-exec-id]').forEach(el=>window.openVibelyThreadController.detach(el));
     }
    },
    beforeNodeRemoved:el=>{window.openVibelyThreadController.detach(el);if(el.querySelectorAll)el.querySelectorAll('[data-exec-id]').forEach(node=>window.openVibelyThreadController.detach(node));}
   }});
   if(window.htmx) window.htmx.process(old);
  }
  async function refresh() {
   if(dead || !root.isConnected || messages().dataset.earlierRequestLoading==='true') return;
   if(inFlight){dirty=true;return;}
   inFlight=true; dirty=false;
   const version=generation, questionRevision=window._chatInputRevision || 0;
   try {
    const response=await fetch(root.dataset.pageChatTranscriptUrl,{credentials:'same-origin',signal:abort.signal,headers:{'HX-Request':'true'}});
    if(!response.ok) throw Error('Page conversation refresh: HTTP '+response.status);
    const html=await response.text();
    if(dead || !root.isConnected || version!==generation || questionRevision!==(window._chatInputRevision || 0)){dirty=true;return;}
    const template=document.createElement('template');template.innerHTML=html;
    const next=template.content.querySelector('#page-chat-messages');
    if(!next) return;
    // Paging prepends older executions through the shared loader. A live-window
    // refresh must retain that prefix and its cursor instead of hiding it again.
    const currentPairs=Array.from(messages().querySelectorAll(':scope > [data-execution-pair]'));
    const firstNext=next.querySelector('[data-execution-pair]');
    const overlap=firstNext ? currentPairs.findIndex(pair=>pair.id===firstNext.id) : -1;
    if(overlap>0){
     next.querySelectorAll('[data-earlier-loader]').forEach(el=>el.remove());
     const prefix=document.createDocumentFragment();
     const loader=messages().querySelector('[data-earlier-loader]');
     if(loader)prefix.append(loader.cloneNode(true));
     currentPairs.slice(0,overlap).forEach(pair=>prefix.append(pair.cloneNode(true)));
     next.prepend(prefix);
    }
    // Keep active streams and unchanged rendered pairs mounted while discovering
    // Analyze/queued turns. Never replace the composer or the user's draft.
    const nextStamp=stamp(next);
    const changed=nextStamp!==fingerprint || stamp(messages())!==nextStamp;
    if(changed) {
     // Keep question cards outside the transcript morph; their draft state is
     // managed by the shared question controller below.
     const cards=Array.from(messages().querySelectorAll('[data-chat-input-request-id]'));
     cards.forEach(card=>card.remove());
     update(messages(),next,true);
     cards.forEach(card=>{
      const pair=document.getElementById('chat-execution-'+card.dataset.chatInputExecId);
      if(pair && messages().contains(pair)) pair.append(card);
     });
     fingerprint=nextStamp;
    }
    ['pending-thread-inputs','page-chat-form-primary-action'].forEach(id=>{
     const current=panel.querySelector('#'+id),incoming=template.content.querySelector('#'+id);
     if(incoming) incoming.removeAttribute('hx-swap-oob');
     if(current && incoming && current.outerHTML!==incoming.outerHTML)update(current,incoming,false);
    });
    const liveCards=Array.from(template.content.querySelectorAll('[data-chat-input-request-id]'));
    const ids=new Set(liveCards.map(c=>c.id));
    messages().querySelectorAll('[data-chat-input-request-id]').forEach(card=>{if(!ids.has(card.id))window.completeChatInputRequest(card);});
    liveCards.forEach(card=>{
     if(document.getElementById(card.id))return;
     const pair=document.getElementById('chat-execution-'+card.dataset.chatInputExecId);
     if(pair && messages().contains(pair))pair.append(card);
    });
    window.initChatInputRequestCards(messages());
    if(changed) hydrate();
   } catch(error) { if(!dead) console.error('[page-chat]',error); }
   finally {inFlight=false;if(dirty && !dead)queueMicrotask(refresh);}
  }
  // Reject snapshots captured before a local send, cancellation or answer.
  on(document,'htmx:beforeRequest',e=>{if(panel.contains(e.detail.elt))generation++;});
  on(document,'htmx:afterRequest',e=>{const el=e.detail.elt;if(panel.contains(el) || (el && el.dataset && el.dataset.containerId==='page-chat-messages')){generation++;refresh();}});
  on(window,'sse-chat-live-event',e=>{if(!e.detail.project_id || e.detail.project_id===root.dataset.projectId){generation++;refresh();}});
  on(window,'sse-live-connected',()=>{generation++;refresh();});
  on(window,'focus',refresh);
  on(document,'visibilitychange',()=>{if(!document.hidden)refresh();});
  const previousRefresh=window.refreshChatComposerAction;
  window.refreshChatComposerAction=refresh;
  window.bindChatInputRequestControls();
  const timer=setInterval(()=>{if(!panel.hidden && !document.hidden)refresh();},5000);
  function destroy(){
   if(dead)return;dead=true;abort.abort();clearInterval(timer);side.destroy();root._pageChatMounted=false;
   messages().querySelectorAll('[data-exec-id]').forEach(el=>window.openVibelyThreadController.detach(el));
   if(window.refreshChatComposerAction===refresh)window.refreshChatComposerAction=previousRefresh;
  }
  on(document,'htmx:beforeCleanupElement',e=>{if(e.target===root || e.target.contains(root))destroy();});
  on(window,'pagehide',destroy);
  window.openVibelyPageChat={refresh,destroy};
  hydrate();refresh();
 };
 document.addEventListener('DOMContentLoaded',window.initPageChat);
 document.addEventListener('htmx:afterSwap',window.initPageChat);
})();
