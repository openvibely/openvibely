package components

import "testing"

func TestBrowserFunctional_UpwardScrollIntoLargePreviousBubble(t *testing.T) {
	for _, mode := range []string{"loaded", "paged", "shrinking", "reverse", "top"} {
		t.Run(mode, func(t *testing.T) {
			runIncrementalChatBrowser(t, `const mode="`+mode+`", paging=mode==='paged';
 const messages=document.getElementById('messages');messages.id='task-thread-messages';messages.style.width='640px';
 messages.setAttribute('data-transcript-hydrating','true');document.getElementById('pair').dataset.execStatus='completed';
 const newer=document.createElement('div');newer.dataset.executionPair='true';newer.dataset.execStatus='completed';
 const owner=document.createElement('div');owner.className='chat-bubble-assistant-msg';newer.append(owner);messages.append(newer);
 const oldPair=document.getElementById('pair');
 const loader=document.createElement('div');loader.dataset.containerId=messages.id;loader.style.height='30px';
 if(paging){oldPair.remove();messages.prepend(loader)}
 const original=window.renderChatMarkdownAsync;let release;
 window.renderChatMarkdownAsync=function(text,...args){
  if(text.length<65536)return original.call(this,text,...args);
  return new Promise(resolve=>{release=()=>resolve(original.call(this,text,...args))});
 };
 const oldSource=mode==='shrinking' ? '[unused]: https://example.com/'+('a'.repeat(70000))+'\n\n'+('Earlier **paragraph**.\n\n').repeat(50) : ('Earlier **paragraph** with some context.\n\n').repeat(2500);
 if(!paging)await renderStreamingContent(stream,oldSource,false);
 await renderStreamingContent(owner,Array.from({length:15},(_,i)=>'Newer message '+i+'\n\n[Using tool: shell | echo]\n[Tool shell done]\nresult\n[/Tool]\n').join(''),false);
 await restoreChatTranscriptScroll({messages,defaultPinned:true});await paint();
 let pageRestore;
 if(paging){
  messages.dispatchEvent(new WheelEvent('wheel',{deltaY:-24,bubbles:true}));
  messages.scrollTop=8;messages.dispatchEvent(new Event('scroll'));await paint();
  window.prepareChatEarlierSwap(loader);loader.after(oldPair);
  window.applyChatBubbleTransforms=()=>renderStreamingContent(stream,oldSource,false);
  window.afterChatEarlierSwap(loader);pageRestore=window.restoreChatEarlierScroll(messages);
 }
 // Read upward until the previous response fills the viewport and the newer
 // bubble has just left its lower edge. Its bottom is our reading boundary.
 for(let i=0;i<400;i++){
  const bounds=messages.getBoundingClientRect();
  if(newer.getBoundingClientRect().top>bounds.bottom+8)break;
  messages.dispatchEvent(new WheelEvent('wheel',{deltaY:-24,bubbles:true}));
  messages.scrollTop-=24;messages.dispatchEvent(new Event('scroll'));await paint();
 }
 assert(release && !stream._chatVirtualRows[0].ready,'previous response did not remain pending');
 assert(newer.getBoundingClientRect().top>messages.getBoundingClientRect().bottom,'did not cross the bubble boundary');
 if(mode==='reverse'||mode==='top'){
  messages.scrollTop=mode==='top'?0:messages.scrollTop+8;
  messages.dispatchEvent(new Event('scroll'));await paint();
 }
 const before=newer.getBoundingClientRect().top;
 release();
 const until=performance.now()+10000;while(!stream._chatVirtualRows[0].ready&&performance.now()<until)await pause(10);
 assert(stream._chatVirtualRows[0].ready,'previous response never finished');await pageRestore;await paint();
 const shift=newer.getBoundingClientRect().top-before;
 if(mode==='top')assert(messages.scrollTop<3,'Home was pulled away from the start of the transcript');
 else assert(Math.abs(shift)<3,'finishing the previous bubble jumped away from its end by '+shift+'px');
 return {shift};
 `)
		})
	}
}
