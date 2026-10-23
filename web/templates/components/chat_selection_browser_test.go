package components

import "testing"

func TestBrowserFunctional_SelectAllResumesVirtualization(t *testing.T) {
	for _, id := range []string{"chat-messages", "task-thread-messages"} {
		for _, modifier := range []string{"metaKey", "ctrlKey"} {
			t.Run(id+"/"+modifier, func(t *testing.T) {
				runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='`+id+`';
 document.getElementById('pair').dataset.execStatus='completed';
 let source='';for(let i=0;i<80;i++)source+='Message '+i+'\n[Using tool: shell | echo '+i+']\n[Tool shell done]\nresult '+i+'\n[/Tool]\n';
 await renderStreamingContent(stream,source,false);await paint();
 const count=()=>stream.querySelectorAll('.stream-tool').length;
 const key=k=>document.dispatchEvent(new KeyboardEvent('keydown',{key:k,`+modifier+`:true,bubbles:true}));
 const selection=window.getSelection();
 const select=()=>{key('a');selection.selectAllChildren(messages);document.dispatchEvent(new Event('selectionchange'));};
 const clear=()=>{selection.removeAllRanges();document.dispatchEvent(new Event('selectionchange'));};
 assert(count()>0&&count()<25,'fixture not virtualized');
 select();await paint();assert(count()===80,'Select All omitted history');
 clear();await paint();await renderVisibleChatRows(messages);await paint();
 assert(count()>0&&count()<25,'clearing selection left '+count()+' mounted tools');
 // Find must remain active even if its selected text is cleared.
 key('f');await materializeVirtualChatRows(messages);await paint();
 select();await paint();clear();await paint();
 assert(count()===80,'deselection cancelled active Find');
 key('Escape');await paint();assert(count()<25,'Escape did not resume windowing');
 // Context-menu selection also works without a keyboard suspension.
 selection.selectAllChildren(messages);document.dispatchEvent(new Event('selectionchange'));await paint();
 assert(count()===80,'selection-only path omitted history');
 clear();await paint();assert(count()<25,'selection-only path did not resume');
 return {};
 `)
			})
		}
	}
}
