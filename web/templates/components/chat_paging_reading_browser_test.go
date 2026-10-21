package components

import "testing"

// Keep the current message stable while the reader continues scrolling and
// the preceding execution is still being hydrated. A newly visible tail must
// not take ownership of the anchor while that message remains on screen.
func TestBrowserFunctional_PagingWhileReadingKeepsCurrentExecution(t *testing.T) {
	for _, id := range []string{"chat-messages", "task-thread-messages"} {
		t.Run(id, func(t *testing.T) {
			runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='`+id+`';messages.style.width='640px';
 const current=document.getElementById('pair');current.dataset.execStatus='completed';
 const prompt=document.createElement('div');prompt.className='chat-bubble-user-msg';prompt.textContent='fix';current.prepend(prompt);
 await renderStreamingContent(stream,'I will fix the constructors.\n\n'+('More details.\n\n').repeat(40),false);
 const loader=document.createElement('div');loader.dataset.containerId=messages.id;loader.style.height='120px';messages.prepend(loader);
 messages.scrollTop=20;await renderVisibleChatRows(messages);await paint();
 window.prepareChatEarlierSwap(loader);
 const previous=document.createElement('div');previous.dataset.executionPair='true';previous.dataset.execStatus='completed';
 const tail=document.createElement('div');tail.className='chat-markdown';tail.style.height='200px';tail.textContent='Earlier response still settling';previous.append(tail);loader.after(previous);
 let finish;
 window.applyChatBubbleTransforms=()=>new Promise(resolve=>{finish=resolve});
 window.afterChatEarlierSwap(loader);const restored=window.restoreChatEarlierScroll(messages);await paint();
 for(let step=0;step<3;step++){
  messages.dispatchEvent(new WheelEvent('wheel',{deltaY:-8,bubbles:true}));
  messages.scrollTop-=8;messages.dispatchEvent(new Event('scroll'));await paint();
  const bounds=messages.getBoundingClientRect(), before=prompt.getBoundingClientRect().top;
  assert(before>=bounds.top&&before<bounds.bottom,'current prompt is not visible');
  // Subsequent rendering in the preceding execution changes its height while
  // both it and the message the reader started with overlap the viewport.
  window.preserveChatVirtualScroll(tail,()=>{tail.style.height=(step%2?200:100)+'px'});
  await paint();
  assert(Math.abs(prompt.getBoundingClientRect().top-before)<3,'paging moved the visible prompt by '+(prompt.getBoundingClientRect().top-before)+'px');
 }
 // Once the reader actually leaves the original execution, ownership must
 // move to the older execution rather than pinning the previous message.
 window.preserveChatVirtualScroll(tail,()=>{tail.style.height='1400px'});await paint();
 messages.dispatchEvent(new WheelEvent('wheel',{deltaY:-600,bubbles:true}));
 messages.scrollTop-=messages.getBoundingClientRect().bottom+20-prompt.getBoundingClientRect().top;
 messages.dispatchEvent(new Event('scroll'));await paint();
 assert(messages._earlierAnchor.pair===previous,'paging anchor did not follow the reader into older history');
 finish();await restored;await paint();
 assert(!messages._earlierAnchor,'paging restoration did not finish');
 return {};
 `)
		})
	}
}

// A prepend already compensated by paging must not be reapplied by the
// virtualizer's deferred ResizeObserver snapshot on the following frame.
func TestBrowserFunctional_PagingRebasesPendingVirtualLayout(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='task-thread-messages';messages.style.width='640px';
 document.getElementById('pair').dataset.execStatus='completed';
 await renderStreamingContent(stream,('A paragraph of existing conversation.\n\n').repeat(60),false);
 const loader=document.createElement('div');loader.dataset.containerId=messages.id;loader.style.height='120px';messages.prepend(loader);
 messages.scrollTop=300;await renderVisibleChatRows(messages);await paint();
 const bounds=messages.getBoundingClientRect();
 const reading=Array.from(stream.querySelectorAll('p')).find(p=>p.getBoundingClientRect().top>=bounds.top&&p.getBoundingClientRect().top<bounds.bottom);
 assert(reading,'missing visible paragraph');const before=reading.getBoundingClientRect().top;
 prepareChatEarlierSwap(loader);
 const previous=document.createElement('div');previous.dataset.executionPair='true';previous.style.height='200px';loader.after(previous);
 let finish;window.applyChatBubbleTransforms=()=>new Promise(resolve=>{finish=resolve});
 afterChatEarlierSwap(loader);const restored=restoreChatEarlierScroll(messages);
 await renderVisibleChatRows(messages);await paint();
 assert(Math.abs(reading.getBoundingClientRect().top-before)<3,'prepend compensated twice: '+(reading.getBoundingClientRect().top-before));
 finish();await restored;await paint();
 assert(Math.abs(reading.getBoundingClientRect().top-before)<3,'settled prepend moved reading position');
 return {};
 `)
}
