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
 await renderLiveChatContent(root,finalSource,true,true);
 await new Promise(resolve=>setTimeout(resolve,100));
 if(root.querySelector('iframe')!==previewFrame||previewFrame.closest('[hidden]')||previewLoads!==1)throw Error('finalization reloaded preview: '+previewLoads);
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
