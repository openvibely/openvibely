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
 assert(stream.querySelectorAll('.chat-markdown').length===2,'Progress and response remain visible');
 await setMode('full');await paint();await paint();
 await setMode('compact');await paint();await paint();
 assert(stream.querySelectorAll('.stream-activity-summary').length===1,'Switching live output modes must regroup sealed sections');
 await renderIncrementalChatContent(stream,'[Using tool: read_file]\n',true);
 applyChatExecutionTerminalStatus(pair,'cancelled');
 assert(!stream.querySelector('.stream-activity-summary').textContent.includes('Running'),'Cancelled activity must not claim to be running');
 return {};
 `)
}
