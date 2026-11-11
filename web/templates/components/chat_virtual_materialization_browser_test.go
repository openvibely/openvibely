package components

import "testing"

func TestBrowserFunctional_VirtualIndexRetainsMediaAndMaterializesPrint(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.setAttribute('data-chat-transcript','');
 messages.style.width='640px';
 const models=Array.from({length:200},(_,i)=>({kind:'text',height:100,width:640,segments:[{content:'Indexed row '+i}]}));
 await installChatVirtualRows(stream,{models,ungrouped:true},(node,model,index)=>{
  node.style.height=(index%2?140:60)+'px';node.textContent=model.segments[0].content;
  if(index===0){const media=document.createElement('video');media.controls=true;node.append(media);}
 });
 messages.scrollTop=0;await renderVisibleChatRows(messages);await paint();await paint();
 const media=stream.querySelector('video');assert(media,'missing initial media');
 for(const offset of [6000,12000,18000,12000,6000,6300,5700,0]){
  messages.scrollTop=offset;await paint();await paint();
  const bounds=messages.getBoundingClientRect();
  for(const row of stream._chatVirtualRows){const el=row.node?.isConnected?row.node:row.placeholder,rect=el.getBoundingClientRect();
   if(rect.bottom>bounds.top&&rect.top<bounds.bottom)assert(row.node?.isConnected&&row.ready,'unrendered visible variable-height row '+row.index);
  }
  assert(media.isConnected&&stream.querySelector('video')===media,'active media was detached or replaced');
 }
 window.dispatchEvent(new Event('beforeprint'));await paint();
 assert(stream._chatVirtualRows.every(row=>row.ready&&row.node?.isConnected),'print omitted transcript rows');
 window.dispatchEvent(new Event('afterprint'));await paint();await paint();
 assert(stream.querySelectorAll('[data-virtual-chat-row]').length>150,'print did not resume windowing');
 return {};
 `)
}
