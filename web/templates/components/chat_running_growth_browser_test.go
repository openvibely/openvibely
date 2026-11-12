package components

import (
	"fmt"
	"testing"
)

// Diagnostics use the actual renderer and wall time in Chrome. Timing is reported,
// not asserted: deterministic work counters catch regressions without CI jitter.
func TestBrowserFunctional_RunningGrowthMeasurements(t *testing.T) {
	for _, mode := range []string{"compact", "full"} {
		t.Run(mode, func(t *testing.T) {
			report := runIncrementalChatBrowser(t, fmt.Sprintf(`
 document.documentElement.setAttribute('data-output-detail',%q);
 const messages=document.getElementById('messages');messages.setAttribute('data-chat-transcript','');messages.style.width='640px';
 const normalizer=window.normalizeTranscriptMarkers, linker=window.linkStreamingToolResults;
 let normalized=0, linked=0;
 window.normalizeTranscriptMarkers=function(text,...rest){normalized+=text.length;return normalizer(text,...rest)};
 window.linkStreamingToolResults=function(segments,...rest){linked+=segments.length;return linker(segments,...rest)};
 const reports=[];
 for(const scenario of ['tools','unmatched','thinking','legacy','unfinished']) {
  let node=document.createElement('div');stream.replaceChildren(node);
  let text=scenario==='unmatched'?'[Using tool: read_file | pending]\n':'';
  const call=i=>scenario==='thinking'?'[Thinking]\nReason '+i+'\n[/Thinking]\n':scenario==='legacy'?'<thinking>Reason '+i+'</thinking>\n':'[Using tool: shell | echo '+i+']\n[Tool shell done]\nresult '+i+'\n[/Tool]\n';
  let count=0;
  for(const size of [200,1000,5000]) {
   while(count<size)text+=call(count++);
   if(scenario==='unfinished')text+='[Using tool: shell | long]\n[Tool shell done]\n'+('output\n').repeat(10000);
   let start=performance.now();await renderLiveChatContent(node,text,false);const initialMS=performance.now()-start;
   if(scenario==='unfinished'){text+='warm output\n';await renderLiveChatContent(node,text,false);}
   normalized=0;linked=0;start=performance.now();
   text+=scenario==='unfinished'?'more output\n':call(count++);
   await renderLiveChatContent(node,text,false);
   reports.push({scenario,size,initialMS,updateMS:performance.now()-start,normalized,linked,dom:node.querySelectorAll('*').length,tools:node.querySelectorAll('.stream-tool').length});
   assert(normalized<256,'completed history was normalized again: '+scenario+' '+normalized);
   assert(linked<12,'completed history was relinked: '+scenario+' '+linked);
   assert(node.querySelectorAll('.stream-tool').length<20,'unbounded mounted tools');
   if(scenario==='unfinished')text+='[/Tool]\n';
  }
 }
 return reports;
 `, mode))
			t.Log(string(report))
		})
	}
}

func TestBrowserFunctional_RunningCheckpointEquivalence(t *testing.T) {
	for _, mode := range []string{"compact", "full"} {
		t.Run(mode, func(t *testing.T) {
			runIncrementalChatBrowser(t, fmt.Sprintf(`
 document.documentElement.setAttribute('data-output-detail',%q);
 const messages=document.getElementById('messages');messages.setAttribute('data-chat-transcript','');

 const referencePair=document.createElement('div');referencePair.dataset.execStatus='completed';messages.appendChild(referencePair);
 const reference=document.createElement('div');referencePair.appendChild(reference);
 const canonical=node=>node._chatVirtualData.sourceModels.map(row=>({kind:row.kind,segments:row.segments.map(seg=>Object.fromEntries(Object.entries(seg).filter(([key])=>key!=='_version').sort(([a],[b])=>a.localeCompare(b))))}));
 let text='';
 const chunks=['Intro **Markdown**.\n\n','[Using tool: Read | pending]\n','[Using tool: shell | next]\n','[Tool shell done]\nopaque '+fence+' [Thinking]\n','[/To','ol]\n','[Thinking]\nReason\n[/Thinking]\n','[Thinking]\nMore\n[/Thinking]\n','<thinking>Legacy</thinking>\n','[Using tool: shell"> <parameter name="command">echo test</parameter></invoke>\n','[Tool shell done: okay]\n','[Tool read_file done]\nlate result\n[/Tool]\n','[Compaction started]\n','[Using tool: shell | during compaction]\n','[Compaction done | 1000]\n','Final [reference][id].\n\n[id]: https://example.com\n',fence+'text\n[Using tool: shell | literal]\n',fence+'\n'];
 for(const chunk of chunks) {
  text+=chunk;
  await renderLiveChatContent(stream,text,false);
  await renderStreamingContent(reference,text,false);
  const actual=JSON.stringify(canonical(stream)),expected=JSON.stringify(canonical(reference));
  assert(actual===expected,'checkpoint differs after '+JSON.stringify(chunk)+'\nactual '+actual+'\nexpected '+expected);
 }
 // Revisions are not always appends; a changed provisional result must not leak.
 text='[Using tool: shell | rewrite]\n[Tool shell done]\nold';
 await renderLiveChatContent(stream,text,false);
 text='[Using tool: shell | rewrite]\n[Tool shell done]\nnew\n[/Tool]\n';
 await renderLiveChatContent(stream,text,false);await renderStreamingContent(reference,text,false);
 assert(JSON.stringify(canonical(stream))===JSON.stringify(canonical(reference)),'source replacement reused stale tool state');
 await renderLiveChatContent(stream,text,false,true);
 assert(JSON.stringify(canonical(stream))===JSON.stringify(canonical(reference)),'completion changed output');
 return {};
 `, mode))
		})
	}
}

func TestBrowserFunctional_RunningHydrationAndMaterialization(t *testing.T) {
	for _, mode := range []string{"compact", "full"} {
		t.Run(mode, func(t *testing.T) {
			runIncrementalChatBrowser(t, fmt.Sprintf(`
 document.documentElement.setAttribute('data-output-detail',%q);
 const messages=document.getElementById('messages');messages.setAttribute('data-chat-transcript','');messages.style.width='640px';
 messages.setAttribute('data-transcript-hydrating','true');
 let text='';for(let i=0;i<1000;i++)text+='[Using tool: shell | echo '+i+']\n[Tool shell done]\nresult '+i+'\n[/Tool]\n';
 text+='[Thinking]\nHidden reasoning\n[/Thinking]\n\nCurrent tail';
 const initial=window.scheduleChatContentRender(stream,text,true);
 const latest=text+' UPDATED DURING HYDRATION';
 const incoming=window.renderLiveChatContent(stream,latest,true);
 await restoreChatTranscriptScroll({messages,defaultPinned:true,renderPromise:Promise.all([initial,incoming])});
 const deadline=performance.now()+10000;
 while(!stream.textContent.includes('UPDATED DURING HYDRATION') && performance.now()<deadline)await pause(20);
 assert(stream.textContent.includes('UPDATED DURING HYDRATION'),'incoming hydration update missing');
 assert(getComputedStyle(messages).visibility==='visible','running transcript stayed hidden');
 assert(stream.querySelectorAll('.stream-tool').length<20,'running hydration eagerly mounted history');
 assert(Math.abs(messages.scrollHeight-messages.scrollTop-messages.clientHeight)<3,'running initial tail not pinned');
 const snapshot=window.captureChatVirtualData(messages)[0];
 const reopened=stream.cloneNode(true);window.cloneVirtualChatRows(stream,reopened);stream.replaceWith(reopened);
 window.findCachedChatVirtualData=()=>snapshot;
 await renderStreamingContent(reopened,latest,false);
 messages.scrollTop=messages.scrollHeight;await renderVisibleChatRows(messages);
 assert(reopened.textContent.includes('UPDATED DURING HYDRATION'),'reopen lost latest source');
 assert(reopened.querySelectorAll('.stream-tool').length<20,'reopen eagerly mounted history');
 // Find materializes the chosen presentation, including an expanded Compact group.
 if(document.documentElement.getAttribute('data-output-detail')==='compact') {
  messages.scrollTop=0;await renderVisibleChatRows(messages);
  reopened.querySelector('.stream-activity-summary').click();await paint();
 }
 document.dispatchEvent(new KeyboardEvent('keydown',{key:'f',ctrlKey:true,bubbles:true}));
 await materializeVirtualChatRows(messages);
 assert(reopened.querySelectorAll('.stream-tool').length===1000,'Find omitted completed calls');
 assert(reopened.textContent.includes('Hidden reasoning'),'Find omitted deferred thinking');
 document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}));await paint();
 document.getElementById('pair').dataset.execStatus='completed';
 await renderLiveChatContent(reopened,latest+'\nFinished.',false,true);
 messages.scrollTop=messages.scrollHeight;await renderVisibleChatRows(messages);
 assert(reopened.textContent.includes('Finished.'),'terminal reconciliation missing');
 assert(reopened.querySelectorAll('.stream-tool').length<20,'terminal reconciliation lost row bound');
 return {};
 `, mode))
		})
	}
}

func TestBrowserFunctional_RunningPartialControls(t *testing.T) {
	runIncrementalChatBrowser(t, `
 const messages=document.getElementById('messages');messages.setAttribute('data-chat-transcript','');messages.setAttribute('data-transcript-hydrating','true');
 const pair=document.createElement('div');pair.dataset.execStatus='completed';messages.appendChild(pair);
 const reference=document.createElement('div');pair.appendChild(reference);
 const shape=node=>JSON.stringify(node._chatVirtualData.sourceModels.map(row=>({kind:row.kind,segments:row.segments.map(seg=>Object.fromEntries(Object.entries(seg).filter(([key])=>key!=='_version').sort(([a],[b])=>a.localeCompare(b))))})));
 const source='Before.\n\n'+fence+'text\n[Using tool: shell | literal]\n'+fence+'\n'+
 '<thinking>Legacy thought</thinking>\n[Thinking]\nAnother thought\n[/Thinking]\n'+
 '[Using tool: shell | first]\n[Using tool: Shell | second]\n[Tool shell done]\nfirst result\n[/Tool]\n[Tool Shell error: second result]\n'+
 '[Using tool: shell"> <parameter name="command">echo test</parameter></invoke>\n[Tool shell done]\n'+('opaque output\n').repeat(100)+'[/Tool]\nFinal paragraph.';
 for(const step of [1,7,31]) {
  const node=document.createElement('div');stream.replaceChildren(node);
  for(let end=step;end<source.length+step;end+=step) {
   const text=source.slice(0,end);await renderStreamingContent(node,text,false);await renderStreamingContent(reference,text,false);
   assert(shape(node)===shape(reference),'partial control mismatch at '+step+':'+end+' '+JSON.stringify(text.slice(-50))+'\nactual '+shape(node)+'\nexpected '+shape(reference));
  }
 }
 // Once an opaque large result is cached, a split closing marker must invalidate
 // only that suffix and still pair with the original invocation.
 const node=document.createElement('div');stream.replaceChildren(node);
 let text='[Using tool: shell | large]\n[Tool shell done]\n'+('opaque\n').repeat(20000);
 await renderStreamingContent(node,text,false);text+='warm\n';await renderStreamingContent(node,text,false);
 const snapshot=captureChatVirtualData(stream)[0], snapshotBefore=JSON.stringify(snapshot);
 for(const chunk of ['more\n','[/T','ool]\n','[Using tool: shell | after]\n']) {
  text+=chunk;await renderStreamingContent(node,text,false);await renderStreamingContent(reference,text,false);
  assert(shape(node)===shape(reference),'cached opaque result changed pairing at '+chunk);
  assert(JSON.stringify(snapshot)===snapshotBefore,'live result mutated an older navigation snapshot');
 }
 return {};
 `)
}

// A later closing backtick can make an apparent tool opener literal Markdown.
// Never seal that opener into the pending-result ledger before it is stable.
func TestBrowserFunctional_RunningCheckpointUnclosedCodeSpan(t *testing.T) {
	for _, mode := range []string{"compact", "full"} {
		t.Run(mode, func(t *testing.T) {
			runIncrementalChatBrowser(t, fmt.Sprintf(`
 document.documentElement.setAttribute('data-output-detail',%q);
 const messages=document.getElementById('messages');messages.setAttribute('data-chat-transcript','');
 const pair=document.createElement('div');pair.dataset.execStatus='completed';messages.appendChild(pair);
 const reference=document.createElement('div');pair.appendChild(reference);
 const tools=node=>node._chatVirtualData.sourceModels.flatMap(row=>row.segments).filter(seg=>seg.type==='using').map(seg=>({name:seg.secondary,output:seg.resultOutput}));
 const tick=String.fromCharCode(96);
 for(const delimiter of [tick,tick+tick]) {
  const node=document.createElement('div');stream.replaceChildren(node);
  const prefix='[Using tool: shell | earlier]\n[Tool shell done]\nearlier output\n[/Tool]\n';
  await renderLiveChatContent(node,prefix,false);
  const checkpoint=node._chatParseCheckpoint.raw;
  let text=prefix+delimiter+'example\n[Using tool: shell | fake]\n';
  await renderLiveChatContent(node,text,false);
  assert(node._chatParseCheckpoint.raw===checkpoint,'unfinished code span advanced checkpoint');
  text+=delimiter+'\n[Using tool: shell | real]\n[Tool shell done]\nreal output\n[/Tool]\n';
  await renderLiveChatContent(node,text,false);await renderStreamingContent(reference,text,false);
  assert(JSON.stringify(tools(node))===JSON.stringify(tools(reference)),'result attached to code example: '+JSON.stringify(tools(node)));
  assert(tools(node).length===2 && tools(node)[1].output==='real output\n','real tool lost its result');
  assert(node._chatParseCheckpoint.raw.length>checkpoint.length,'closed code span prevented checkpoint progress');
 }
 // Backticks in tool data, complete code spans, and escaped literals must not
 // keep ordinary completed history in the provisional suffix.
 for(const prose of ['Complete '+tick+'code'+tick+'.\n','Escaped '+String.fromCharCode(92)+tick+'.\n']) {
  const node=document.createElement('div');stream.replaceChildren(node);
  const text=prose+'[Using tool: shell | echo '+tick+']\n[Tool shell done]\nopaque '+tick+'\n[/Tool]\n';
  await renderLiveChatContent(node,text,false);
  assert(node._chatParseCheckpoint?.raw===text,'safe Markdown or opaque tool data blocked checkpoint');
 }
 return {};
 `, mode))
		})
	}
}
