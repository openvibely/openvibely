(()=>{
 if(window.OVFileViewer)return;
 const dialog=document.getElementById('image-gallery'),content=dialog.querySelector('[data-result-content]');
 const title=dialog.querySelector('[data-result-title]'),download=dialog.querySelector('[data-gallery-download]');
 const stage=dialog.querySelector('[data-gallery-stage]'),imageControls=dialog.querySelector('[data-image-controls]');
 const prev=dialog.querySelector('[data-gallery-prev]'),next=dialog.querySelector('[data-gallery-next]');
 const selector='[data-image-gallery-item],[data-file-preview]';
 let controller,opener,group,items=[],index=0,ticket=0,currentKind='';
 function mode(raw,key){const url=new URL(raw,location.href);for(const k of ['view','raw','download','thumbnail'])url.searchParams.delete(k);url.searchParams.set(key,'1');return url.href}
 function original(raw){const url=new URL(raw,location.href);return url.origin===location.origin&&/\/(attachments|files)\//.test(url.pathname)?mode(raw,'download'):raw}
 function clear(){
  dialog.querySelectorAll('header [data-sheet-tabs],header [data-sheet-count]').forEach(node=>node.remove());
  content.dispatchEvent(new CustomEvent('ov:file-replace',{bubbles:true}));
  window.OVFileImage?.clear();
  content.querySelectorAll('video,audio').forEach(m=>{m.pause();m.removeAttribute('src');m.load()});
  content.replaceChildren();content.hidden=true;stage.hidden=true;imageControls.hidden=true;
 }
 function itemFrom(element,url){
  const raw=url||element?.href||element?.dataset.imageUrl||element?.dataset.filePreview;
  let kind=element?.hasAttribute('data-image-gallery-item')?(element.dataset.mediaKind||'image'):'';
  const name=element?.dataset.imageName||element?.dataset.fileName||element?.textContent.trim()||'File preview';
  let preview=raw;
  if(kind){const u=new URL(raw,location.href);if(u.origin===location.origin&&/\/(attachments|files)\//.test(u.pathname))preview=mode(raw,kind==='video'?'raw':'thumbnail')}
  return {element,raw,url:preview,kind,name,download:original(raw)};
 }
 async function show(at){
  if(at<0||at>=items.length)return;
  index=at;const item=items[index],id=++ticket;controller?.abort();controller=new AbortController();
  if(dialog.contains(document.activeElement))dialog.focus({preventScroll:true});
  clear();
  currentKind=item.kind;previewKind(currentKind);title.textContent=item.name;download.href=item.download;download.download=item.name;download.hidden=false;
  dialog.querySelector('[data-gallery-count]').textContent=(index+1)+' / '+items.length;
  prev.disabled=index===0;next.disabled=index===items.length-1;
  if(item.kind){stage.hidden=false;imageControls.hidden=false;window.OVFileImage.render(item);return}
  content.hidden=false;content.textContent='Loading preview…';
  try{
   const response=await fetch(mode(item.raw,'view'),{signal:controller.signal});
   if(!response.ok){let message='Preview unavailable';try{message=(await response.json()).message||message}catch{}throw Error(message)}
   const html=await response.text();if(id!==ticket||!dialog.open)return;
   content.innerHTML=html;
   const header=content.querySelector('[data-file-header],[data-document-header]');
   if(header){title.textContent=header.querySelector('h3')?.textContent||item.name;download.download=title.textContent;const link=header.querySelector('a');if(link)download.href=link.href;header.remove()}
   // A Files-panel link may not have media metadata. Use the server's renderer kind.
   const media=content.querySelector('[data-file-viewer="image"] img,[data-file-viewer="video"] video');
   if(media){currentKind=media.tagName==='VIDEO'?'video':'image';previewKind(currentKind);const rendered={...item,name:title.textContent,kind:currentKind,url:media.src};content.replaceChildren();content.hidden=true;stage.hidden=false;imageControls.hidden=false;window.OVFileImage.render(rendered);return}
   if(content.querySelector('[data-files-list]')){title.textContent='Files';download.hidden=true}
   window.OVFilesPDF?.render(content);
   content.querySelectorAll('[data-file-code]').forEach(code=>window.hljs?.highlightElement(code));
   content.querySelectorAll('[data-file-markdown]').forEach(node=>{node.innerHTML=window.renderChatMarkdown?window.renderChatMarkdown(node.textContent):window.sanitizeChatHTML(window.marked.parse(node.textContent))});
  }catch(err){
   if(err.name==='AbortError'||id!==ticket)return;
   content.replaceChildren();const status=document.createElement('p');status.role='status';status.textContent=err.message;
   const retry=document.createElement('button');retry.textContent='Retry';retry.onclick=()=>show(index);content.append(status,retry);
  }
 }
 function open(url,trigger){
  // Snapshot a list before replacing an in-dialog Files panel.
  group=trigger?.closest('[data-image-gallery-group],[data-files-list],.chat-markdown');
  const links=group?Array.from(group.querySelectorAll(selector)):[];
  const at=links.indexOf(trigger);
  items=at<0?[itemFrom(trigger,url)]:links.map(link=>itemFrom(link));
  if(!dialog.contains(trigger)){opener=trigger}else group=null;
  if(!dialog.open)dialog.showModal();show(at<0?0:at);
  dialog.focus({preventScroll:true});
 }
 function previewKind(kind){
  dialog.dataset.previewKind=kind;
  const viewer=dialog.querySelector('.image-gallery-viewer');
  [prev,next].forEach(button=>viewer.append(button));
  dialog.querySelector('[data-result-actions]').hidden=true;
  dialog.querySelector('.image-gallery-tools').append(download);
  imageControls.hidden=false;
  ['minus','plus','fit'].forEach(name=>dialog.querySelector('[data-gallery-'+name+']').hidden=kind!=='image');
 }
 function close(){if(dialog.open)dialog.close()}
 document.addEventListener('click',event=>{
  const link=event.target.closest(selector);
  if(!link||event.defaultPrevented||event.ctrlKey||event.metaKey||event.shiftKey||event.altKey)return;
  event.preventDefault();event.stopPropagation();open(link.href||link.dataset.imageUrl||link.dataset.filePreview,link);
 });
 prev.onclick=()=>show(index-1);next.onclick=()=>show(index+1);
 dialog.querySelector('[data-gallery-close]').onclick=close;
 dialog.addEventListener('close',()=>{if(dialog.open)return;ticket++;controller?.abort();clear();title.textContent='File preview';download.hidden=true;download.removeAttribute('href');download.removeAttribute('download');items=[];group=null;if(opener?.isConnected)opener.focus({preventScroll:true});opener=null});
 dialog.addEventListener('keydown',event=>{
  if(event.defaultPrevented||event.isComposing||event.altKey||event.metaKey||event.ctrlKey||event.target.closest('input,textarea,select,[contenteditable],[data-sheet-grid]'))return;
  if(event.key==='Escape'){event.preventDefault();close();return}
  if(event.key==='ArrowLeft'||event.key==='ArrowRight'){event.preventDefault();show(index+(event.key==='ArrowRight'?1:-1));return}
  if(currentKind==='image'&&['+','=','-','0'].includes(event.key)){event.preventDefault();window.OVFileImage.key(event.key)}
 });
 let backdropPress=false;
 const isBackdrop=target=>!target.closest('img,video,button,a,[data-result-content],[data-gallery-name],[data-gallery-count]');
 dialog.addEventListener('pointerdown',event=>{backdropPress=event.button===0&&isBackdrop(event.target)},true);
 dialog.addEventListener('pointerup',event=>{const released=document.elementFromPoint(event.clientX,event.clientY);backdropPress=backdropPress&&event.button===0&&released&&dialog.contains(released)&&isBackdrop(released)},true);
 dialog.addEventListener('pointercancel',()=>{backdropPress=false});
 dialog.addEventListener('click',event=>{if(backdropPress&&isBackdrop(event.target))close();backdropPress=false;const toggle=event.target.closest('[data-file-html-mode]');if(toggle){const root=toggle.closest('[data-file-viewer]'),source=toggle.dataset.fileHtmlMode==='source';root.querySelector('iframe').hidden=source;root.querySelector('[data-file-source]').hidden=!source}});
 document.addEventListener('htmx:beforeSwap',event=>{const target=event.detail.target;if(event.detail.shouldSwap&&dialog.open&&target&&group&&(target===group||target.contains(group))){if(target.id==='attachment-list'&&target.closest('[data-task-files]')){group=target.closest('[data-task-files]');opener=group}else close()}});
 new MutationObserver(()=>{if(dialog.open&&group&&(!group.isConnected||!opener?.isConnected))close()}).observe(document.body,{childList:true,subtree:true});
 window.imageGallery={close,refreshItem(element){const at=items.findIndex(item=>item.element===element);if(at<0)return;items[at]=itemFrom(element);if(at===index){if(currentKind==='image'&&window.OVFileImage.refresh(items[at])){download.href=items[at].download}else show(at)}}};
 // Retain enhanced published images while the surrounding response is rebuilt.
 function prepareStream(fragment,previous){
  const saved=new Map();
  const key=link=>link.href+'\n'+link.textContent;
  previous.querySelectorAll('a[data-file-link-bound][data-image-gallery-item]').forEach(link=>{
   const identity=key(link);if(!saved.has(identity))saved.set(identity,[]);saved.get(identity).push(link);
  });
  fragment.querySelectorAll('a[href]').forEach(link=>{
   const retained=saved.get(key(link))?.shift();if(!retained)return;
   link.replaceWith(retained);
   const scope=retained.closest('.chat-markdown')||retained.parentElement;
   scope.dataset.imageGalleryGroup='';
   if(dialog.open&&retained===opener)group=scope;
  });
 }
 window.OVFileViewer={open,mode,prepareStream};
 // Events missed while disconnected are not replayed by the live stream.
 window.addEventListener('sse-live-connected',event=>{
  if(!event.detail?.reconnected)return;
  document.querySelectorAll('[data-task-files]').forEach(panel=>window.htmx.trigger(panel,'taskFilesChanged'));
 });
 window.addEventListener('sse-task-event',event=>{
  const update=event.detail;
  if(update?.type!=='task_files_changed')return;
  document.querySelectorAll('[data-task-files]').forEach(panel=>{
   if(panel.dataset.taskFiles===update.task_id&&panel.dataset.projectId===update.project_id)
    window.htmx.trigger(panel,'taskFilesChanged');
  });
 });
 document.addEventListener('click',async event=>{
 const expand=event.target.closest('.file-html-canvas button');if(expand){const frame=expand.closest('.file-html-canvas').querySelector('iframe');if(frame)open(frame.src,expand);return}
 const files=event.target.closest('[data-files-panel]');if(files){open(files.dataset.filesPanel,files);return}
 const attach=event.target.closest('[data-file-attach]');if(!attach)return;
 const status=dialog.querySelector('[data-file-attach-status]');attach.disabled=true;
 try{const input=document.querySelector('form input[name=attachment_session_id]')?.closest('form')?.querySelector('input[type=file]');if(!input)throw Error('Open a conversation composer to attach this file.');const response=await fetch(mode(attach.dataset.fileAttach,'download'));if(!response.ok)throw Error('Could not read this file');const blob=await response.blob(),transfer=new DataTransfer();transfer.items.add(new File([blob],attach.dataset.fileName,{type:blob.type}));input.files=transfer.files;input.dispatchEvent(new Event('change',{bubbles:true}));dialog.close()}catch(err){if(status)status.textContent=err.message}finally{attach.disabled=false}
 });
 // Server-rendered preview links already own their thumbnail and gallery group.
 // Only enhance ordinary published links, such as links in model responses.
 function scanLinks(root=document){root.querySelectorAll('a[href]:not([data-file-link-bound]):not([data-image-gallery-item]):not([data-file-preview])').forEach(link=>{
 let url;try{url=new URL(link.href,location.href)}catch{return}
 if(url.origin!==location.origin||!/^\/files\/[a-f0-9]+$/.test(url.pathname)||url.searchParams.has('download')||link.hasAttribute('download'))return;
 link.dataset.fileLinkBound='';link.dataset.filePreview='';
 if(link.closest('[data-result-content]'))return;
 const meta=new URL(url);meta.searchParams.set('metadata','1');fetch(meta).then(r=>r.ok?r.json():null).then(file=>{
 if(!file||!link.isConnected)return;
 if(/^(image|video)\//.test(file.media_type)||/\.svg$/i.test(file.file_name)){
 const video=file.media_type.startsWith('video/');link.removeAttribute('data-file-preview');link.dataset.imageGalleryItem='';link.dataset.imageName=file.file_name;link.dataset.mediaKind=video?'video':'image';(link.closest('.chat-markdown')||link.parentElement).dataset.imageGalleryGroup='';const preview=document.createElement(video?'video':'img');preview.src=mode(url,video?'raw':'thumbnail');preview.alt=file.file_name;preview.style.cssText='max-width:100%;max-height:400px;object-fit:contain';if(video){preview.muted=true;preview.preload='metadata'}link.prepend(preview);return;
 }
 if(file.display!=='canvas')return;
 const canvas=document.createElement('div');canvas.className='file-html-canvas';const frame=document.createElement('iframe');frame.title=file.file_name;frame.setAttribute('sandbox','allow-scripts');frame.referrerPolicy='no-referrer';frame.src=mode(url,'raw');const expand=document.createElement('button');expand.type='button';expand.textContent='Expand';canvas.append(frame,expand);link.after(canvas);
 }).catch(()=>{});
 })}
 let scanQueued=false;new MutationObserver(()=>{if(scanQueued)return;scanQueued=true;requestAnimationFrame(()=>{scanQueued=false;scanLinks()})}).observe(document.body,{childList:true,subtree:true});scanLinks();
})();
