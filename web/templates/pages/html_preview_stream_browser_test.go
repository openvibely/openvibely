package pages

import "testing"

func TestBrowserFunctional_StreamingHTMLPreview(t *testing.T) {
	for _, mode := range []string{"full", "compact"} {
		for _, messages := range []string{"chat-messages", "task-thread-messages"} {
			t.Run(mode+"/"+messages, func(t *testing.T) {
				runStreamingMathBrowser(t, func(b *composerFocusCDP) {
					b.waitFor("renderer ready", `String(document.readyState === 'complete' && !!window.OVFileViewer && !!window.renderLiveChatContent)`, "true")
					b.evaluate(`document.getElementById('chat-messages').id='` + messages + `'; document.documentElement.setAttribute('data-output-detail', '` + mode + `'); 'ready'`)
					b.evaluateAwait(`(async()=>{
 const root=document.getElementById('math-stream');
 window.previewSource='Published [Preview](/files/abcdef).';
 await renderLiveChatContent(root,previewSource);
 const link=root.querySelector('a');
 link.setAttribute('data-file-link-bound','true');link.setAttribute('data-file-preview','true');
 const canvas=document.createElement('div');canvas.className='file-html-canvas';
 window.previewFrame=document.createElement('iframe');previewFrame.setAttribute('sandbox','allow-scripts');
 window.previewLoads=0;previewFrame.addEventListener('load',()=>previewLoads++);
 previewFrame.srcdoc='<html><body>Interactive preview</body></html>';
 canvas.append(previewFrame);link.after(canvas);
 return 'ready';
})()`)
					b.waitFor("preview loaded", `String(window.previewLoads)`, "1")
					result := b.evaluateAwait(`(async()=>{
 const root=document.getElementById('math-stream');
 for(const suffix of [' More',' More response text.',' More response text.\n\nAnother paragraph.']){
  await renderLiveChatContent(root,previewSource+suffix);
  await new Promise(resolve=>setTimeout(resolve,100));
  if(root.querySelector('iframe')!==previewFrame||previewFrame.closest('[hidden]')||previewLoads!==1)throw Error('stream reloaded preview: '+previewLoads);
 }
 window.finalSource=previewSource+' More response text.\n\nAnother paragraph.';
 root.parentElement.setAttribute('data-exec-status','completed');
 await renderLiveChatContent(root,finalSource,true,true);
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe')!==previewFrame||previewFrame.closest('[hidden]')||previewLoads!==1)throw Error('finalization reloaded preview: '+previewLoads);
 root.parentElement.setAttribute('data-exec-status','running');
 await renderLiveChatContent(root,finalSource+' Additional text.');
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe')!==previewFrame||previewFrame.closest('[hidden]')||previewLoads!==1)throw Error('resume reloaded preview: '+previewLoads);
 const messages=root.closest('#chat-messages, #task-thread-messages');
 messages.setAttribute('data-transcript-hydrating','true');
 const tools=Array.from({length:160},(_,i)=>'[Using tool: bash | echo '+i+']\n[Tool bash done]\nresult '+i+'\n[/Tool]\n').join('');
 const pendingSource=finalSource+' Additional text.\n\n'+tools;
 await renderStreamingContent(root,pendingSource,false,{stable:true});
 if(!root._filePreviewPool?.contains(previewFrame))throw Error('missing pending preview');
 const snapshot=root.cloneNode(true);cloneVirtualChatRows(root,snapshot);
 if(snapshot.querySelector('[data-chat-preview-pool]'))throw Error('pending preview leaked into snapshot');
 await renderStreamingContent(root,pendingSource,false,{stable:true});
 await renderStreamingContent(root,pendingSource+' Done.',false,{stable:true});
 if(!root._filePreviewPool?.contains(previewFrame))throw Error('refresh discarded pending preview');
 messages.removeAttribute('data-transcript-hydrating');
 await renderVisibleChatRows(messages);
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe')!==previewFrame||previewFrame.closest('[hidden]')||previewLoads!==1)throw Error('pending preview reloaded: '+previewLoads);
 if(root._filePreviewPool||root.querySelector('[data-chat-preview-pool]'))throw Error('consumed preview pool retained');
 if(document.documentElement.getAttribute('data-output-detail')==='compact'&&root.querySelector('.stream-tool'))throw Error('collapsed tools rendered eagerly');
 await renderLiveChatContent(root,'Preview removed.',true,true);
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe'))throw Error('removed preview retained');
 return 'pass';
})()`)
					if result != "pass" {
						t.Fatal(result)
					}
				})
			})
		}
	}
}

func TestBrowserFunctional_StreamingHTMLPreviewWithoutMoveBefore(t *testing.T) {
	runStreamingMathBrowser(t, func(b *composerFocusCDP) {
		b.waitFor("renderer ready", `String(document.readyState==='complete' && !!window.OVFileViewer && !!window.renderLiveChatContent)`, "true")
		b.evaluate(`Element.prototype.moveBefore=undefined;document.getElementById('math-stream').parentElement.setAttribute('data-exec-status','running');'ready'`)
		b.evaluateAwait(`(async()=>{
 const root=document.getElementById('math-stream');window.source='[Using tool: publish_file | mockup.html]\n[Tool publish_file done]\n{"url":"/files/abcdef"}\n[/Tool]\nPublished [Mockup](/files/abcdef).';
 await renderLiveChatContent(root,source);
 const link=root.querySelector('a');link.dataset.fileLinkBound='';link.dataset.filePreview='';
 const canvas=document.createElement('div');canvas.className='file-html-canvas';window.frame=document.createElement('iframe');frame.setAttribute('sandbox','allow-scripts');window.loads=0;frame.addEventListener('load',()=>loads++);frame.srcdoc='<html><body>Mockup</body></html>';canvas.append(frame);link.after(canvas);return 'ready';
 })()`)
		b.waitFor("preview loaded", `String(loads)`, "1")
		result := b.evaluateAwait(`(async()=>{
 const root=document.getElementById('math-stream');
 for(const suffix of [' More',' More explanation.',' More explanation.\n\nSecond paragraph.']){
 await renderLiveChatContent(root,source+suffix);
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe')!==frame||loads!==1)return 'preview reloads: '+loads;
 if(!root.textContent.includes(suffix.split('\n\n').pop().trim()))return 'response text not updated';
 }
 root.parentElement.setAttribute('data-exec-status','completed');
 await renderLiveChatContent(root,source+' More explanation.\n\nSecond paragraph.',true,true);
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe')!==frame||loads!==1)return 'completion reloads: '+loads;
 root.parentElement.setAttribute('data-exec-status','running');
 await renderLiveChatContent(root,source+' More explanation.\n\nSecond paragraph. Continuing.');
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe')!==frame||loads!==1)return 'resume reloads: '+loads;
 await renderLiveChatContent(root,'Preview removed.',true,true);
 if(root.querySelector('iframe'))return 'removed preview retained';
 return 'pass';
 })()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}

func TestBrowserFunctional_HTMLPreviewCompactTools(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		label := "modern"
		if legacy {
			label = "fallback"
		}
		t.Run(label, func(t *testing.T) {
			runStreamingMathBrowser(t, func(b *composerFocusCDP) {
				b.waitFor("ready", `String(document.readyState==='complete' && !!renderLiveChatContent)`, "true")
				if legacy {
					b.evaluate(`Element.prototype.moveBefore=undefined;'ready'`)
				}
				result := b.evaluateAwait(`(async()=>{
    document.documentElement.setAttribute('data-output-detail','compact');
    const root=document.getElementById('math-stream');
    root.parentElement.style.cssText='height:280px;overflow:auto';
    const source='Published [Mockup](/files/abcdef).';
    await renderLiveChatContent(root,source);
    const link=root.querySelector('a');link.dataset.fileLinkBound='';link.dataset.filePreview='';
    const canvas=document.createElement('div');canvas.className='file-html-canvas';
    window.compactPreview=document.createElement('iframe');compactPreview.setAttribute('sandbox','allow-scripts');window.compactLoads=0;compactPreview.addEventListener('load',()=>compactLoads++);
    const loaded=new Promise(resolve=>compactPreview.addEventListener('load',resolve,{once:true}));
    compactPreview.srcdoc='<p>Preview</p>';canvas.append(compactPreview);link.after(canvas);await loaded;
    const tools=Array.from({length:160},(_,i)=>'[Using tool: bash | echo '+i+']\n[Tool bash done]\nresult '+i+'\n[/Tool]\n').join('');
    root.parentElement.setAttribute('data-exec-status','completed');
    await renderLiveChatContent(root,source+'\n\n'+tools,true,true);
    return JSON.stringify({renderedTools:root.querySelectorAll('.stream-tool').length,expanded:root.querySelector('.stream-activity-summary')?.getAttribute('aria-expanded')});
   })()`)
				if result != `{"renderedTools":0,"expanded":"false"}` {
					t.Fatalf("collapsed tools rendered eagerly: %s", result)
				}
				b.evaluate(`document.querySelector('#math-stream .stream-activity-summary').click();'expanded'`)
				b.waitFor("visible tools materialized", `String(document.querySelectorAll('#math-stream .stream-tool').length>0)`, "true")
				if got := b.evaluate(`String(document.querySelectorAll('#math-stream .stream-tool').length<160)`); got != "true" {
					t.Fatal("expanded tools were not virtualized")
				}
				b.evaluate(`document.querySelector('#math-stream .stream-activity-summary').click();'collapsed'`)
				b.waitFor("collapsed tools discarded", `String(document.querySelectorAll('#math-stream .stream-tool').length)`, "0")
				if got := b.evaluate(`String(document.querySelector('#math-stream iframe')===compactPreview && compactLoads===1)`); got != "true" {
					t.Fatal("tool disclosure reloaded the preview")
				}
			})
		})
	}
}
func TestBrowserFunctional_HTMLPreviewMultipleWithoutMoveBefore(t *testing.T) {
	runStreamingMathBrowser(t, func(b *composerFocusCDP) {
		b.waitFor("ready", `String(document.readyState==='complete' && !!renderLiveChatContent)`, "true")
		b.evaluate(`Element.prototype.moveBefore=undefined;'ready'`)
		b.evaluateAwait(`(async()=>{
   window.root=document.getElementById('math-stream');
   window.source='First [One](/files/aaaaaa).\n\n[Using tool: publish_file | second.html]\n[Tool publish_file done]\n{"url":"/files/bbbbbb"}\n[/Tool]\nSecond [Two](/files/bbbbbb).';
   await renderLiveChatContent(root,source);
   window.counts=[0,0];window.frames=[];
   Array.from(root.querySelectorAll('a')).forEach((link,i)=>{
    link.dataset.fileLinkBound='';link.dataset.filePreview='';
    const canvas=document.createElement('div');canvas.className='file-html-canvas';
    const frame=document.createElement('iframe');frame.setAttribute('sandbox','allow-scripts');frame.addEventListener('load',()=>counts[i]++);frame.srcdoc='<p>Preview</p>';frames.push(frame);canvas.append(frame);link.after(canvas);
   });return 'ready';
  })()`)
		b.waitFor("both loaded", `JSON.stringify(counts)`, "[1,1]")
		b.evaluateAwait(`(async()=>{await renderLiveChatContent(root,source+' More text.');await new Promise(resolve=>setTimeout(resolve,150));return 'ready'})()`)
		if got := b.evaluate(`JSON.stringify(counts)`); got != "[1,1]" {
			t.Fatal("preview reloaded during update: " + got)
		}
		result := b.evaluateAwait(`(async()=>{
   root.parentElement.setAttribute('data-exec-status','completed');
   await renderLiveChatContent(root,source+' More text.',true,true);
   await new Promise(resolve=>setTimeout(resolve,100));
   if(JSON.stringify(counts)!=='[1,1]'||root.querySelectorAll('iframe').length!==2)return 'completion reloaded previews';
   root.parentElement.setAttribute('data-exec-status','running');
   await renderLiveChatContent(root,source+' More text. Still streaming.');
   await new Promise(resolve=>setTimeout(resolve,100));
   if(JSON.stringify(counts)!=='[1,1]'||!root.textContent.includes('Still streaming.'))return 'resume failed';
   await renderLiveChatContent(root,'First [One](/files/aaaaaa). Only one remains.');
   await new Promise(resolve=>setTimeout(resolve,100));
   if(root.querySelectorAll('iframe').length!==1||root.querySelector('iframe')!==frames[0]||counts[0]!==1)return 'partial removal failed';
   if(root.textContent.includes('Second')||root.querySelector('.stream-activity'))return 'obsolete rows retained';
   await renderLiveChatContent(root,'Preview removed.',true,true);
   if(root.querySelector('iframe'))return 'removed preview retained';
   return 'pass';
  })()`)
		if result != "pass" {
			t.Fatal(result)
		}
	})
}

func TestBrowserFunctional_HTMLPreviewFallbackOwnership(t *testing.T) {
	for _, scenario := range []string{"quoted-url", "plain-url", "fenced-url", "different-label", "detail-toggle"} {
		t.Run(scenario, func(t *testing.T) {
			runStreamingMathBrowser(t, func(b *composerFocusCDP) {
				b.waitFor("renderer", `String(document.readyState==='complete' && !!renderLiveChatContent)`, "true")
				b.evaluate(`Element.prototype.moveBefore=undefined;window.root=document.getElementById('math-stream');'ready'`)
				if scenario != "detail-toggle" {
					b.evaluate(`window.ownershipScenario='` + scenario + `';'ready'`)
					b.evaluateAwait(`(async()=>{const prefix=ownershipScenario==='plain-url'?'The URL is /files/abcdef.':ownershipScenario==='fenced-url'?'\x60\x60\x60text\n/files/abcdef\n\x60\x60\x60':ownershipScenario==='different-label'?'[Earlier reference](/files/abcdef).':'The URL is \x60/files/abcdef\x60.';window.source=prefix+' \n\n[Using tool: shell | echo done]\n[Tool shell done]\ndone\n[/Tool]\nPublished [Mockup](/files/abcdef).';await renderLiveChatContent(root,source);return 'ready'})()`)
				} else {
					b.evaluateAwait(`(async()=>{document.documentElement.setAttribute('data-output-detail','compact');root.parentElement.setAttribute('data-exec-status','completed');window.source=Array.from({length:3},(_,i)=>'[Using tool: shell | echo '+i+']\n[Tool shell done]\ndone\n[/Tool]\n').join('')+'Published [Mockup](/files/abcdef).';await renderLiveChatContent(root,source,true,true);return 'ready'})()`)
				}
				b.evaluate(`const link=Array.from(root.querySelectorAll('a')).find(link=>link.textContent==='Mockup');link.dataset.fileLinkBound='';link.dataset.filePreview='';const canvas=document.createElement('div');canvas.className='file-html-canvas';window.frame=document.createElement('iframe');frame.setAttribute('sandbox','allow-scripts');window.loads=0;frame.addEventListener('load',()=>loads++);frame.srcdoc='<p>Preview</p>';canvas.append(frame);link.after(canvas);'ready'`)
				b.waitFor("loaded", `String(loads)`, "1")
				if scenario != "detail-toggle" {
					b.evaluateAwait(`(async()=>{await renderLiveChatContent(root,source+' Extra text.');await new Promise(resolve=>setTimeout(resolve,150));return 'ready'})()`)
				} else {
					b.evaluateAwait(`(async()=>{document.documentElement.setAttribute('data-output-detail','full');regroupChatActivity();while(root._chatVirtualRows.length!==4)await new Promise(requestAnimationFrame);await renderVisibleChatRows(root.parentElement);await new Promise(resolve=>setTimeout(resolve,150));return 'ready'})()`)
				}
				if got := b.evaluate(`String(frame.isConnected && loads===1 && root.querySelectorAll('iframe').length===1)`); got != "true" {
					t.Fatal("preview ownership lost: " + b.evaluate(`JSON.stringify({connected:frame.isConnected,loads:loads,previews:root.querySelectorAll('iframe').length})`))
				}
				if scenario == "detail-toggle" {
					result := b.evaluateAwait(`(async()=>{document.documentElement.setAttribute('data-output-detail','compact');regroupChatActivity();while(root._chatVirtualRows.length!==2)await new Promise(requestAnimationFrame);await renderVisibleChatRows(root.parentElement);await new Promise(resolve=>setTimeout(resolve,100));return String(frame.isConnected&&loads===1&&root.querySelectorAll('.stream-tool').length===0)})()`)
					if result != "true" {
						t.Fatal("return to compact mode lost preview or lazy tools")
					}
				}
			})
		})
	}
}

func TestBrowserFunctional_HTMLPreviewStaleOwnershipLookup(t *testing.T) {
	runStreamingMathBrowser(t, func(b *composerFocusCDP) {
		b.waitFor("renderer", `String(document.readyState==='complete' && !!renderLiveChatContent)`, "true")
		b.evaluateAwait(`(async()=>{
   Element.prototype.moveBefore=undefined;
   window.root=document.getElementById('math-stream');window.source='Published [Mockup](/files/abcdef).';
   await renderLiveChatContent(root,source);
   const link=root.querySelector('a');link.dataset.fileLinkBound='';link.dataset.filePreview='';
   const canvas=document.createElement('div');canvas.className='file-html-canvas';canvas.append(document.createElement('iframe'));link.after(canvas);
   const original=window.renderChatMarkdownAsync;
   window.renderChatMarkdownAsync=function(text,...args){
    if(text.includes(' delayed'))return new Promise(resolve=>{window.releaseLookup=()=>resolve(original(text,...args))});
    return original(text,...args);
   };
   window.pendingUpdate=renderLiveChatContent(root,source+' delayed');return 'ready';
  })()`)
		b.waitFor("lookup pending", `typeof window.releaseLookup`, "function")
		result := b.evaluateAwait(`(async()=>{
   await renderLiveChatContent(root,'Replacement without preview.',true,true);
   releaseLookup();await pendingUpdate;await new Promise(resolve=>setTimeout(resolve,100));
   return String(root.textContent.trim()==='Replacement without preview.'&&!root.querySelector('iframe'));
  })()`)
		if result != "true" {
			t.Fatal("stale preview lookup overwrote the newer response")
		}
	})
}

func TestBrowserFunctional_HTMLPreviewDuplicateLinkSections(t *testing.T) {
	for _, identical := range []bool{false, true} {
		label := "different-sections"
		if identical {
			label = "identical-sections"
		}
		t.Run(label, func(t *testing.T) {
			runStreamingMathBrowser(t, func(b *composerFocusCDP) {
				b.waitFor("renderer", `String(document.readyState==='complete' && !!renderLiveChatContent)`, "true")
				first := "Earlier"
				if identical {
					first = "Final"
				}
				b.evaluate(`window.firstSection='` + first + `';'ready'`)
				b.evaluateAwait(`(async()=>{
    Element.prototype.moveBefore=undefined;document.documentElement.setAttribute('data-output-detail','full');
    window.root=document.getElementById('math-stream');window.messages=root.parentElement;
    messages.style.cssText='height:240px;overflow:auto';messages.setAttribute('data-exec-status','completed');
    window.source=firstSection+' [Mockup](/files/abcdef).\n\n'+Array.from({length:30},(_,i)=>'[Using tool: shell | echo '+i+']\n[Tool shell done]\ndone\n[/Tool]\n').join('')+'Final [Mockup](/files/abcdef).';
    await renderLiveChatContent(root,source,true,true);
    messages.scrollTop=messages.scrollHeight;await renderVisibleChatRows(messages);
    return 'ready';
   })()`)
				b.waitFor("final row visible", `String(!!root._chatVirtualRows.at(-1).node?.querySelector('a'))`, "true")
				b.evaluate(`const link=root._chatVirtualRows.at(-1).node.querySelector('a');link.dataset.fileLinkBound='';link.dataset.filePreview='';const canvas=document.createElement('div');canvas.className='file-html-canvas';window.frame=document.createElement('iframe');frame.setAttribute('sandbox','allow-scripts');window.loads=0;frame.addEventListener('load',()=>loads++);frame.srcdoc='<p>Final preview</p>';canvas.append(frame);link.after(canvas);'ready'`)
				b.waitFor("loaded", `String(loads)`, "1")
				result := b.evaluateAwait(`(async()=>{
    if(root._chatVirtualRows[0].node?.isConnected)return 'earlier row must be offscreen';
    for(const suffix of [' Extra text.',' Extra text. Still updating.']){
     await renderLiveChatContent(root,source+suffix,true,true);
     await new Promise(resolve=>setTimeout(resolve,100));
     if(loads!==1||!root._chatVirtualRows.at(-1).node?.contains(frame))return 'preview moved to another section';
    }
    document.documentElement.setAttribute('data-output-detail','compact');regroupChatActivity();
    while(root._chatVirtualRows.length!==3)await new Promise(requestAnimationFrame);
    await renderVisibleChatRows(messages);await new Promise(resolve=>setTimeout(resolve,100));
    return String(loads===1&&root._chatVirtualRows.at(-1).node?.contains(frame));
   })()`)
				if result != "true" {
					t.Fatal(result)
				}
			})
		})
	}
}
