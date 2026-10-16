// Image zoom/pan is independent of gallery navigation and document controls.
(()=>{
 const dialog=document.getElementById('image-gallery'),stage=dialog.querySelector('[data-gallery-stage]');
 const q=s=>dialog.querySelector('[data-gallery-'+s+']');
 let image=stage.querySelector('img'),scale=1,x=0,y=0,drag=null;
  function transform(){
 if(image.tagName==='VIDEO')return;
   const maxX=Math.max(0,(image.width*scale-stage.clientWidth)/2), maxY=Math.max(0,(image.height*scale-stage.clientHeight)/2);
   x=Math.max(-maxX,Math.min(maxX,x)); y=Math.max(-maxY,Math.min(maxY,y));
   image.style.transform=`translate(${x}px, ${y}px) scale(${scale})`; stage.style.cursor=scale>1?'grab':'default';
   q('fit').ariaLabel='Fit image ('+Math.round(scale*100)+'%)';
  }
  function zoom(value){ if(image.tagName==='VIDEO')return; scale=Math.max(1,Math.min(8,value)); transform(); }

 function clear(){if(image.tagName==='VIDEO'){image.pause();image.removeAttribute('src');image.load()}else image.removeAttribute('src');image.onload=image.onerror=image.onloadedmetadata=null;drag=null;}
 function render(item){
  clear();scale=1;x=0;y=0;
  const old=image;image=document.createElement(item.kind==='video'?'video':'img');
  if(item.kind==='video'){image.controls=true;image.playsInline=true;image.preload='metadata';image.style.cssText='max-width:100%;max-height:100%;object-fit:contain'}
  image.dataset.galleryImage='';image.draggable=false;image.alt=item.name;old.replaceWith(image);
  const current=image;q('status').textContent='Loading…';
  image.onload=image.onloadedmetadata=()=>{if(image!==current)return;q('status').textContent='';transform()};
  image.onerror=()=>{if(image!==current)return;q('status').textContent='Image unavailable';image.style.visibility='hidden'};
  image.src=item.url;transform();
 }
 function refresh(item){
  if(image.tagName!=='IMG'||item.kind!=='image')return false;
  image.style.visibility='';image.alt=item.name;image.src=item.url;
  return true;
 }
 q('minus').onclick=()=>zoom(scale/1.5);q('plus').onclick=()=>zoom(scale*1.5);q('fit').onclick=()=>{x=0;y=0;zoom(1)};
 stage.addEventListener('wheel',e=>{if(image.tagName==='VIDEO')return;e.preventDefault();zoom(scale*(e.deltaY<0?1.15:1/1.15))},{passive:false});
 stage.addEventListener('pointerdown',e=>{if(image.tagName==='VIDEO'||scale<=1||e.target!==image||e.button!==0)return;drag={id:e.pointerId,x:e.clientX,y:e.clientY};stage.setPointerCapture(e.pointerId)});
 stage.addEventListener('pointermove',e=>{if(!drag||drag.id!==e.pointerId)return;x+=e.clientX-drag.x;y+=e.clientY-drag.y;drag.x=e.clientX;drag.y=e.clientY;transform()});
 stage.addEventListener('pointerup',()=>drag=null);stage.addEventListener('pointercancel',()=>drag=null);
 window.addEventListener('resize',transform);
 window.OVFileImage={render,refresh,clear,key(key){if(key==='-')zoom(scale/1.5);else if(key==='0'){x=0;y=0;zoom(1)}else zoom(scale*1.5)}};
})();
