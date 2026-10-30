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
 stream.querySelector('button.stream-tool-summary').click();
 assert(body.getBoundingClientRect().height>0,'Tool must expand');
 await renderStreamingContent(stream,source+' More response.',false);
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height>0,'Expansion must survive rerender');
 stream.querySelector('button.stream-tool-summary').click();
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
 stream.querySelector('button.stream-tool-summary').click();
 await renderStreamingContent(stream,partial+' continues\n[/Tool]\nFinished.',true);
 assert(stream.querySelector('.stream-tool-body').getBoundingClientRect().height>0,'Manual expansion survives streaming completion');
 return {};
 `)
}
