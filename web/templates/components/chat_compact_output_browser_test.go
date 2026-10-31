package components

import "testing"

func TestBrowserFunctional_CompactToolOutput(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const source='I will check the files.\n[Using tool: bash]\n[Tool bash done]\nhello from tool\n[/Tool]\nHere is the answer.';
 const setMode = mode => {document.documentElement.setAttribute('data-output-detail',mode);document.dispatchEvent(new CustomEvent('openvibely:display-change',{detail:{name:'output',value:mode}}));};
 setMode('full');
 await renderStreamingContent(stream,source,false);
 let body=stream.querySelector('.stream-tool-body');
 assert(body && body.getBoundingClientRect().height>0,'Full output must show tools');
 setMode('compact');
 assert(body.getBoundingClientRect().height===0,'Compact must hide tool details');
 assert(stream.querySelector('.chat-markdown').getBoundingClientRect().height>0,'Commentary must remain visible');
 assert(stream.textContent.includes('Here is the answer.'),'Answer must remain');
 stream.querySelector('button.stream-activity-summary').click();
 assert(body.getBoundingClientRect().height>0,'Tool must expand');
 await renderStreamingContent(stream,source+' More response.',false);
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height>0,'Expansion must survive rerender');
 stream.querySelector('button.stream-activity-summary').click();
 await renderStreamingContent(stream,source+' More response. Done.',false);
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height===0,'Collapse must survive rerender');
 setMode('full');
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height>0,'Switching to Full must restore tool details');
 setMode('compact');
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height===0,'Compact should retain collapsed choice');
 releaseToolOutputState(null);
 const partial='Progress message.\n[Using tool: bash]\n[Tool bash done]\npartial output';
 await renderStreamingContent(stream,partial,true);
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height===0,'Streaming tool defaults collapsed');
 stream.querySelector('button.stream-activity-summary').click();
 await renderStreamingContent(stream,partial+' continues\n[/Tool]\nFinished.',true);
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height>0,'Manual expansion survives streaming completion');
 const grouped='Before.\n[Using tool: read_file]\n[Tool read_file done]\nfirst file\n[/Tool]\n[Using tool: bash]\n[Tool bash error]\ncommand failed\n[/Tool]\nAfter.';
 releaseToolOutputState(null);
 await renderStreamingContent(stream,grouped,false);
 assert(stream.querySelectorAll('.stream-activity-summary').length===1,'Consecutive tools must form one group');
 assert(stream.querySelector('.stream-activity-label').textContent==='2 tool calls · 1 failed','Group must summarize calls and failures');
 assert(stream.querySelectorAll('.chat-markdown').length===2,'Prose must remain outside activity');
 stream.querySelector('.stream-activity-summary').click();
 assert(Array.from(stream.querySelectorAll('.stream-tool')).every(n=>n.getBoundingClientRect().height>0),'Expansion reveals both original tools');
 await renderStreamingContent(stream,'[Using tool: read_file]\n',false);
 const waiting=stream.querySelector('.stream-activity-summary');
 assert(waiting && waiting.textContent.includes('Running'),'Pending activity has expandable live label');
 waiting.click();
 assert(stream.querySelector('.stream-tool').getBoundingClientRect().height>0,'Pending group reveals the tool even without result details');
 return {};
 `)
}

func TestBrowserFunctional_CompactToolOutputRemount(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='task-thread-messages';messages.style.height='160px';messages.style.overflowAnchor='none';
 document.getElementById('pair').dataset.execStatus='completed';
 messages.setAttribute('data-transcript-hydrating','true');
 const setMode=mode=>{document.documentElement.setAttribute('data-output-detail',mode);document.dispatchEvent(new CustomEvent('openvibely:display-change',{detail:{name:'output',value:mode}}));};
 const models=Array.from({length:30},(_,index)=>({kind:index===0?'using':'text',segments:[]}));
 let firstRenderCount=0;
 setMode('full');
 await installChatVirtualRows(stream,{models},async(node,model,index)=>{
  if(index!==0){node.style.height='200px';node.textContent='Other output '+index;return;}
  firstRenderCount++;node._chatToolStateKey='remount-tool';
  await renderStreamingContent(node,'[Using tool: bash]\n[Tool bash done]\nTool contents\n[/Tool]',false,{virtualRow:true});
 });
 await restoreChatTranscriptScroll({messages,defaultPinned:false});
 async function scrollTo(top){messages.scrollTop=top;messages.dispatchEvent(new Event('scroll'));await paint();await paint();}
 await scrollTo(0);
 const row=stream._chatVirtualRows[0];
 assert(row.ready && row.node.isConnected,'first tool must be mounted');
 const body=row.node.querySelector('.stream-tool-body');
 const initialRenderCount=firstRenderCount;
 assert(body.getBoundingClientRect().height>0,'Full starts expanded');
 await scrollTo(messages.scrollHeight);
 assert(!row.node.isConnected,'real virtualization must detach row');
 setMode('compact');
 await scrollTo(0);
 assert(row.node.isConnected && firstRenderCount===initialRenderCount,'retained row must remount without rerender');
 assert(body.getBoundingClientRect().height===0 && row.node.querySelector('button').getAttribute('aria-expanded')==='false','remounted row must use Compact');
 await scrollTo(messages.scrollHeight);setMode('full');await scrollTo(0);
 assert(body.getBoundingClientRect().height>0 && row.node.querySelector('.stream-activity-summary').hidden,'remounted Full must restore details and disable disclosure');
 setMode('compact');row.node.querySelector('button').click();
 await scrollTo(messages.scrollHeight);setMode('full');setMode('compact');await scrollTo(0);
 assert(body.getBoundingClientRect().height>0 && row.node.querySelector('button').getAttribute('aria-expanded')==='true','manual Compact expansion must survive remount');
 return {};
 `)
}

func TestBrowserFunctional_CompactActivityHistoryAndStreaming(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='chat-messages';
 const pair=document.getElementById('pair');pair.dataset.execStatus='completed';
 const setMode=async mode=>{document.documentElement.setAttribute('data-output-detail',mode);document.dispatchEvent(new CustomEvent('openvibely:display-change',{detail:{name:'output',value:mode}}));await renderVisibleChatRows(messages);await paint();};
 let tools='';for(let i=0;i<12;i++)tools+='[Using tool: bash | echo '+i+']\n[Tool bash done]\n'+('result '+i+' ').repeat(150)+'\n[/Tool]\n';
 await setMode('compact');
 await renderStreamingContent(stream,'Before.\n'+tools+'After.',true);await paint();
 assert(stream._chatVirtualData.models.length===3,'History should project prose, one activity group, prose');
 assert(stream.querySelectorAll('.stream-activity-summary').length===1,'History tools share one fold');
 let summary=stream.querySelector('.stream-activity-summary');
 assert(summary.textContent.includes('12 tool calls') && summary.getAttribute('aria-expanded')==='false','History starts folded with count');
 summary.click();await paint();
 assert(Array.from(stream.querySelectorAll('.stream-tool')).every(n=>n.getBoundingClientRect().height>0),'Group reveals original tools');
 await setMode('full');
 assert(stream._chatVirtualData.models.length===14,'Full retains individually virtualized tools');
 await setMode('compact');
 summary=stream.querySelector('.stream-activity-summary');
 assert(summary.getAttribute('aria-expanded')==='true','Manual group expansion survives mode switching');
 releaseToolOutputState(null);
 pair.dataset.execStatus='running';
 await renderIncrementalChatContent(stream,'Before.\n'+tools+'[Using tool: read_file]\n',true);await paint();
 assert(stream.querySelectorAll('.stream-activity-summary').length===1,'Large streaming activity must not split into multiple groups');
 assert(stream.querySelector('.stream-activity-summary').textContent.includes('Running Read'),'Group reports running tool');
 stream.querySelector('.stream-activity-summary').click();
 await renderIncrementalChatContent(stream,'Before.\n'+tools+'[Using tool: read_file]\n[Tool read_file done]\ncontents\n[/Tool]\nDone.',true);await paint();
 assert(stream.querySelector('.stream-activity-summary').getAttribute('aria-expanded')==='true','Expansion survives incremental result arrival');
 messages.scrollTop=messages.scrollHeight;messages.dispatchEvent(new Event('scroll'));await renderVisibleChatRows(messages);await paint();
 assert(Array.from(stream.querySelectorAll('.chat-markdown')).some(n=>n.textContent.includes('Done.')),'Response remains available below activity');
 messages.scrollTop=0;messages.dispatchEvent(new Event('scroll'));await renderVisibleChatRows(messages);await paint();
 assert(Array.from(stream.querySelectorAll('.chat-markdown')).some(n=>n.textContent.includes('Before.')),'Progress remains available above activity');
 await setMode('full');await paint();await paint();
 await setMode('compact');await paint();await paint();
 assert(stream.querySelectorAll('.stream-activity-summary').length===1,'Switching live output modes must regroup sealed sections');
 await renderIncrementalChatContent(stream,'[Using tool: read_file]\n',true);
 applyChatExecutionTerminalStatus(pair,'cancelled');
 assert(!stream.querySelector('.stream-activity-summary').textContent.includes('Running'),'Cancelled activity must not claim to be running');
 return {};
 `)
}

func TestBrowserFunctional_CompactFoldFocus(t *testing.T) {
	runIncrementalChatBrowser(t, `
 document.documentElement.setAttribute('data-output-detail','compact');
 await renderIncrementalChatContent(stream,'[Using tool: bash | echo hello]\n',false);
 let toggle=stream.querySelector('.stream-activity-summary');
 toggle.focus();toggle.click();
 assert(document.activeElement===toggle,'fixture must focus the fold');
 await renderIncrementalChatContent(stream,'[Using tool: bash | echo hello]\n[Tool bash done]\nhello\n[/Tool]\n',false);
 assert(document.activeElement===stream.querySelector('.stream-activity-summary'),'Streaming update lost keyboard focus from expanded fold');
 return {};
 `)
}
func TestBrowserFunctional_CompactFoldVirtualization(t *testing.T) {
	runIncrementalChatBrowser(t, `
 document.getElementById('messages').id='chat-messages';
 document.getElementById('pair').dataset.execStatus='completed';
 document.documentElement.setAttribute('data-output-detail','compact');
 let text='';for(let i=0;i<160;i++)text+='[Using tool: bash | echo row-'+i+']\n[Tool bash done]\n'+('output-'+i+' ').repeat(500)+'\n[/Tool]\n';
 await renderStreamingContent(stream,text,true);await paint();await paint();
 const count=stream.querySelectorAll('.stream-tool').length;
 assert(count===0,'Collapsed group must not mount tool cards: '+count);
 const summary=stream.querySelector('.stream-activity-summary');
 summary.click();await paint();await paint();
 const mounted=stream.querySelectorAll('.stream-tool').length;
 assert(mounted>0 && mounted<40,'Expanded group must retain bounded rendering: '+mounted);
 const messages=document.getElementById('chat-messages');messages.scrollTop=messages.scrollHeight;messages.dispatchEvent(new Event('scroll'));await paint();await paint();
 assert(messages.textContent.includes('row-159'),'Last tool must mount when scrolled into view');
 summary.click();await paint();
 assert(stream.querySelectorAll('.stream-tool').length===0,'Collapsing must release the expanded tool DOM');
 document.getElementById('pair').dataset.execStatus='running';
 await renderIncrementalChatContent(stream,text+'[Using tool: read_file]\n',true);await paint();
 assert(stream.querySelectorAll('.stream-tool').length===0,'Running compact groups must defer tool DOM too');
 let liveToggle=stream.querySelector('.stream-activity-summary');liveToggle.focus();liveToggle.click();await paint();
 await renderIncrementalChatContent(stream,text+'[Using tool: read_file]\n[Tool read_file done]\nfile contents\n[/Tool]\n',true);await paint();
 assert(document.activeElement===stream.querySelector('.stream-activity-summary'),'Virtualized live group must restore keyboard focus');
 return {};
 `)
}

func TestBrowserFunctional_CompactSnapshotExcludesExpandedLists(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='chat-messages';
 document.getElementById('pair').dataset.execStatus='completed';
 document.documentElement.setAttribute('data-output-detail','compact');
 const source='[Using tool: bash | echo first]\n[Tool bash done]\nfirst output\n[/Tool]\n[Using tool: bash | echo second]\n[Tool bash done]\nsecond output\n[/Tool]\n';
 await renderStreamingContent(stream,source,true);await paint();await paint();
 const outer=stream._chatVirtualData;
 assert(outer && typeof outer.source==='string' && typeof outer.normalized==='string','fixture must have source-backed transcript data');
 const summary=stream.querySelector('.stream-activity-summary');
 assert(summary && summary.getAttribute('aria-expanded')==='false','fixture must start collapsed');
 summary.click();await paint();await paint();
 const body=stream.querySelector('.stream-activity-body');
 assert(body._chatVirtualData?.ungrouped && body.hasAttribute('data-chat-virtual-owner'),'fixture must mount a nested virtual list');
 const nested=body._chatVirtualData;
 const captured=window.captureChatVirtualData(messages);
 assert(captured.length===1 && captured[0]===outer,'snapshot must retain only the source-backed outer transcript');
 assert(!captured.includes(nested),'snapshot must exclude derived expanded lists');
 const output=captured[0].models.flatMap(model=>model.segments).map(segment=>segment.resultOutput||'').join('\n');
 assert(output.includes('first output') && output.includes('second output'),'outer snapshot must retain every tool result');
 assert(summary.getAttribute('aria-expanded')==='true' && body._chatVirtualData===nested,'snapshot capture must not alter the expanded group');
 summary.click();await paint();
 const collapsed=window.captureChatVirtualData(messages);
 assert(collapsed.length===1 && collapsed[0]===outer,'collapsed snapshot must retain the same outer data');
 return {};
 `)
}

func TestBrowserFunctional_CompactDeferredToolRemount(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.id='chat-messages';messages.style.height='160px';
 document.getElementById('pair').dataset.execStatus='completed';
 document.documentElement.setAttribute('data-output-detail','compact');
 const source='[Using tool: bash]\n[Tool bash done]\nimportant output\n[/Tool]\n'+Array.from({length:150},(_,i)=>'Paragraph '+i+'\n\n').join('');
 await renderStreamingContent(stream,source,true);await paint();await paint();
 messages.scrollTop=0;messages.dispatchEvent(new Event('scroll'));await paint();await paint();
 const row=stream._chatVirtualRows[0];
 assert(row.node && row.node.isConnected,'fixture starts mounted');
 messages.scrollTop=messages.scrollHeight;messages.dispatchEvent(new Event('scroll'));await paint();await paint();
 assert(!row.node.isConnected,'fixture detached activity');
 document.documentElement.setAttribute('data-output-detail','full');document.dispatchEvent(new CustomEvent('openvibely:display-change',{detail:{name:'output',value:'full'}}));await paint();
 messages.scrollTop=0;messages.dispatchEvent(new Event('scroll'));await renderVisibleChatRows(messages);await paint();await paint();
 assert(row.node.isConnected,'activity remounted');
 const tool=row.node.querySelector('.stream-tool');
 assert(tool && tool.getBoundingClientRect().height>0 && tool.textContent.includes('important output'),'Full mode must show deferred tool content after remount');
 messages.scrollTop=messages.scrollHeight;messages.dispatchEvent(new Event('scroll'));await paint();await paint();
 assert(!row.node.isConnected,'Full row must detach again');
 document.documentElement.setAttribute('data-output-detail','compact');document.dispatchEvent(new CustomEvent('openvibely:display-change',{detail:{name:'output',value:'compact'}}));
 messages.scrollTop=0;messages.dispatchEvent(new Event('scroll'));await renderVisibleChatRows(messages);await paint();await paint();
 const summary=row.node.querySelector('.stream-activity-summary');
 assert(summary.getBoundingClientRect().height>0 && summary.getAttribute('aria-expanded')==='false','Returning in Compact must show collapsed summary');
 assert(!row.node.querySelector('.stream-tool'),'Collapsed remount must release tool content');
 summary.click();await paint();await paint();
 assert(row.node.querySelector('.stream-tool').getBoundingClientRect().height>0,'Remounted Compact group must still expand');
 return {};
 `)
}
