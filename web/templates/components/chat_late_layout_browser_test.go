package components

import "testing"

// WebKit does not provide Chrome's automatic scroll anchoring. A row can
// resize after paging settles (for example an image or diagram finishing).
func TestBrowserFunctional_VirtualHistoryLateLayoutKeepsReadingPosition(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='task-thread-messages';
 messages.style.overflowAnchor='none'; messages.style.width='640px';
 document.getElementById('pair').dataset.execStatus='completed';
 const source=Array.from({length:60},(_,i)=>'Message '+i+' **formatted**.\n\n[Using tool: shell | echo '+i+']\n[Tool shell done]\nresult\n[/Tool]\n').join('');
 await renderStreamingContent(stream,source,false);
 messages.scrollTop=120;await renderVisibleChatRows(messages);await paint();
 const loader=document.createElement('div');loader.dataset.containerId=messages.id;loader.style.height='30px';messages.prepend(loader);
 window.prepareChatEarlierSwap(loader);
 const older=document.createElement('div');older.dataset.executionPair='true';older.dataset.execStatus='completed';
 const owner=document.createElement('div');owner.className='chat-bubble-assistant-msg';older.append(owner);loader.after(older);
 window.applyChatBubbleTransforms=()=>renderStreamingContent(owner,source,false);
 window.afterChatEarlierSwap(loader);await window.restoreChatEarlierScroll(messages);await paint();
 // Straddle the boundary between the fetched page and the existing history.
 messages.scrollTop+=document.getElementById('pair').getBoundingClientRect().top-messages.getBoundingClientRect().top-120;
 await renderVisibleChatRows(messages);await paint();
 assert(!messages._earlierAnchor,'page restoration still pending');
 for(let cycle=0;cycle<3;cycle++){
  const bounds=messages.getBoundingClientRect();
  const rows=[...owner._chatVirtualRows,...stream._chatVirtualRows].filter(r=>r.ready&&r.node?.isConnected);
  const above=rows.find(r=>r.node.getBoundingClientRect().bottom<bounds.top && r.node.getBoundingClientRect().bottom>bounds.top-200);
  const reading=rows.find(r=>r.node.getBoundingClientRect().top>=bounds.top && r.node.getBoundingClientRect().top<bounds.bottom);
  assert(above&&reading,'missing mounted rows around reader');
  // Continue the upward gesture in the same frame that late content resizes.
  messages.scrollTop-=8;
  const top=reading.node.getBoundingClientRect().top;
  above.node.firstElementChild.style.paddingBottom=(cycle%2?0:90)+'px';
  await paint();
  const delta=reading.node.getBoundingClientRect().top-top;
  assert(Math.abs(delta)<3,'late row layout moved reading position '+delta+'px');
 }
 // Shrinking a row near the bottom can clamp the native scroll offset. That
 // clamp is layout, not additional upward input, and must not be counted twice.
 messages.scrollTop=messages.scrollHeight;await renderVisibleChatRows(messages);await paint();
 const endBounds=messages.getBoundingClientRect();
 const upper=stream._chatVirtualRows.find(r=>r.ready&&r.node?.isConnected&&r.node.getBoundingClientRect().bottom<endBounds.top);
 assert(upper,'missing upper row at bottom');
 upper.node.firstElementChild.style.paddingBottom='180px';await paint();
 messages.scrollTop=messages.scrollHeight-messages.clientHeight-30;await paint();
 const reading=stream._chatVirtualRows.find(r=>r.ready&&r.node?.isConnected&&r.node.getBoundingClientRect().top>=endBounds.top&&r.node.getBoundingClientRect().top<endBounds.bottom);
 assert(reading,'missing reading row near bottom');const top=reading.node.getBoundingClientRect().top;
 upper.node.firstElementChild.style.paddingBottom='0px';await paint();
 assert(Math.abs(reading.node.getBoundingClientRect().top-top)<3,'scroll clamping was counted as user movement');
 return {};
 `)
}
