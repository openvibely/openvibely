package components

import "testing"

func TestBrowserFunctional_VirtualHistoryPrependKeepsReadingPosition(t *testing.T) {
	for _, id := range []string{"chat-messages", "task-thread-messages"} {
		for _, anchoring := range []string{"auto", "none"} {
			t.Run(id+"/"+anchoring, func(t *testing.T) {
				runIncrementalChatBrowser(t, `const transcriptID="`+id+`", anchoring="`+anchoring+`";

 const messages=document.getElementById('messages'); messages.id=transcriptID;
 messages.style.width='640px'; messages.style.overflowAnchor=anchoring;
 document.getElementById('pair').dataset.execStatus='completed';
 const output=label=>Array.from({length:40},(_,i)=>label+' message '+i+' **formatted**.\n\n[Using tool: shell | echo '+i+']\n[Tool shell done]\nresult '+i+'\n[/Tool]\n').join('');
 await window.renderStreamingContent(stream,output('latest'),false);
 messages.scrollTop=0; await window.renderVisibleChatRows(messages); await paint();
 const loader=document.createElement('div'); loader.dataset.containerId=messages.id; loader.style.height='30px'; messages.prepend(loader);
 for(let page=0;page<3;page++) {
  messages.scrollTop=180; await window.renderVisibleChatRows(messages); await paint();
  const bounds=messages.getBoundingClientRect();
  const anchor=Array.from(messages.querySelectorAll('[data-chat-lazy-row]:not([data-virtual-chat-row])')).find(el=>el.getBoundingClientRect().bottom>bounds.top && el.getBoundingClientRect().top<bounds.bottom);
  assert(anchor,'missing visible anchor');
  const top=anchor.getBoundingClientRect().top;
  const trace=[]; const sample=label=>trace.push({label,top:anchor.getBoundingClientRect().top,scroll:messages.scrollTop,height:messages.scrollHeight}); sample('before');
  let maxShift=0, recording=true;
  function frame(){if(!recording)return;maxShift=Math.max(maxShift,Math.abs(anchor.getBoundingClientRect().top-top));requestAnimationFrame(frame)}
  requestAnimationFrame(frame);
  window.prepareChatEarlierSwap(loader);
  const pair=document.createElement('div'); pair.dataset.executionPair='true'; pair.dataset.execStatus='completed';
  const owner=document.createElement('div'); owner.className='chat-bubble-assistant-msg';pair.append(owner);loader.after(pair);
  window.applyChatBubbleTransforms=()=>window.renderStreamingContent(owner,output('page '+page),false);
  window.afterChatEarlierSwap(loader); sample('afterSwap');
  // A late non-virtual resize must still be compensated before a virtual commit.
  owner.style.paddingTop='75px'; window.preserveChatVirtualScroll(anchor,()=>{});
  window.restoreChatEarlierScroll(messages);
  await messages._chatEarlierHydrationPromise; sample('hydrated'); await paint(); sample('paint'); await pause(120); sample('settled'); recording=false;
  assert(maxShift<3,'page '+page+' moved reader '+maxShift+'px '+JSON.stringify(trace));
 }
 // Continue reading the newly loaded history. Rendering an estimated row must
 // not move the current reading row or repeatedly mount/unmount it at rest.
 messages.scrollTop=messages.scrollHeight-1000; await window.renderVisibleChatRows(messages); await paint();
 for(let step=0;step<50;step++) {
  messages.scrollTop-=90;
  const bounds=messages.getBoundingClientRect();
  const row=Array.from(messages.querySelectorAll('[data-chat-virtual-owner]')).flatMap(owner=>owner._chatVirtualRows).find(row=>{
   const el=row.node?.isConnected?row.node:row.placeholder, rect=el.getBoundingClientRect();
   return rect.bottom>bounds.top && rect.top<bounds.bottom;
  });
  assert(row,'no reading row during upward scroll');
  const element=()=>row.node?.isConnected?row.node:row.placeholder, top=element().getBoundingClientRect().top;
  await paint(); await pause(35);
  assert(Math.abs(element().getBoundingClientRect().top-top)<3,'upward step '+step+' shifted reader '+(element().getBoundingClientRect().top-top));
 }
 return {pages:3,scrollSteps:50};
 `)
			})
		}
	}
}

func TestBrowserFunctional_HiddenPagingPreservesAnchorAndCleansUp(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='task-thread-messages';messages.style.width='640px';messages.style.overflowAnchor='none';
 document.getElementById('pair').dataset.execStatus='completed';
 const output=label=>Array.from({length:40},(_,i)=>label+' message '+i+' **formatted**.\n\n[Using tool: shell | echo '+i+']\n[Tool shell done]\nresult '+i+'\n[/Tool]\n').join('');
 await window.renderStreamingContent(stream,output('latest'),false);
 const loader=document.createElement('div'); loader.dataset.containerId=messages.id;loader.style.height='30px';messages.prepend(loader);
 messages.scrollTop=100;await window.renderVisibleChatRows(messages);await paint();
 const rect=messages.getBoundingClientRect();
 const anchor=Array.from(messages.querySelectorAll('[data-chat-lazy-row]:not([data-virtual-chat-row])')).find(el=>el.getBoundingClientRect().bottom>rect.top&&el.getBoundingClientRect().top<rect.bottom);
 const top=anchor.getBoundingClientRect().top;
 window.prepareChatEarlierSwap(loader);
 const workspace=document.createElement('section');messages.replaceWith(workspace);workspace.append(messages);workspace.style.display='none';await paint();
 const older=document.createElement('div');older.dataset.executionPair='true';older.dataset.execStatus='completed';
 const owner=document.createElement('div');owner.className='chat-bubble-assistant-msg';older.append(owner);loader.after(older);
 window.applyChatBubbleTransforms=()=>window.renderStreamingContent(owner,output('older'),false);
 window.afterChatEarlierSwap(loader);const restored=window.restoreChatEarlierScroll(messages);
 await messages._chatEarlierHydrationPromise;await paint();await pause(80);
 assert(messages._earlierAnchor,'discarded pending hidden anchor');
 // Hiding again during visible-row rendering must keep the same transaction.
 const renderVisible=window.renderVisibleChatRows;let hiddenAgain=false;
 window.renderVisibleChatRows=async function(...args){await renderVisible(...args);if(!hiddenAgain){workspace.style.display='none';hiddenAgain=true;}};
 workspace.style.display='';
 for(let i=0;i<100&&!hiddenAgain;i++)await pause(10);
 assert(hiddenAgain,'visible restoration did not start');await paint();
 assert(messages._earlierAnchor,'second hide discarded pending anchor');
 window.renderVisibleChatRows=renderVisible;workspace.style.display='';await restored;await paint();
 assert(anchor.isConnected,'original reading row detached after revealing workspace');
 assert(Math.abs(anchor.getBoundingClientRect().top-top)<3,'hidden paging moved reader by '+(anchor.getBoundingClientRect().top-top)+'px, scroll='+messages.scrollTop);
 assert(!messages._earlierAnchor,'visible paging restoration did not finish');
 // Navigating away while another hidden page settles must release its waiter.
 window.prepareChatEarlierSwap(loader);workspace.style.display='none';
 window.afterChatEarlierSwap(loader);const pending=window.restoreChatEarlierScroll(messages);
 await messages._chatEarlierHydrationPromise;await paint();
 messages.remove();document.dispatchEvent(new CustomEvent('htmx:afterSwap',{detail:{target:document.body}}));
 const completed=await Promise.race([pending.then(()=>true),pause(500).then(()=>false)]);
 assert(completed && !messages._earlierAnchor,'removed hidden transcript retained its paging transaction');
 return {};
 `)
}
