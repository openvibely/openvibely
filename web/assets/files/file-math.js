(()=>{
 let load,ready=false;
 function library(){
  if(load)return load;
  load=Promise.all([
   new Promise((resolve,reject)=>{const css=document.createElement('link');css.rel='stylesheet';css.href='/static/vendor/katex.min.css';css.onload=resolve;css.onerror=reject;document.head.append(css)}),
   window.katex?Promise.resolve():new Promise((resolve,reject)=>{const script=document.createElement('script');script.src='/static/vendor/katex.min.js';script.onload=resolve;script.onerror=reject;document.head.append(script)})
  ]).then(()=>{ready=true;scan()}).catch(()=>{load=null});
  return load;
 }
 const key=node=>JSON.stringify([node.dataset.mathDisplay,node.dataset.mathTex]);
 function typeset(node){
  if(!ready||node.dataset.mathReady||node.closest('[data-thread-refresh-preview]'))return;
  try{window.katex.render(node.dataset.mathTex,node,{displayMode:node.dataset.mathDisplay==='block',throwOnError:false,trust:false,maxExpand:1000,maxSize:20});node.dataset.mathReady='true'}
  catch{node.textContent=node.dataset.mathTex;node.dataset.mathReady='failed'}
 }
 // Virtualization keeps existing row DOM where possible. Save equations from
 // the discarded DOM separately, so lazy rows can adopt them without retaining
 // the old response (including potentially large tool outputs).
 window.retainChatMathForVirtualRows=function(owner){
  const saved=new Map();
  owner.querySelectorAll('[data-math-ready="true"]').forEach(node=>{
   const id=key(node);if(!saved.has(id))saved.set(id,[]);saved.get(id).push(node);
   node.remove();
  });
  if(saved.size)owner._chatMathNodes=saved;else delete owner._chatMathNodes;
 };
 // Called after sanitization, immediately before committing a streamed fragment.
 // Prefer this row's own equations, then adopt equations from discarded DOM.
 window.prepareChatMath=function(fragment,previous){
  const saved=new Map();
  previous.querySelectorAll('[data-math-ready="true"]').forEach(node=>{const id=key(node);if(!saved.has(id))saved.set(id,[]);saved.get(id).push(node)});
  const owners=[];
  for(let owner=previous;owner;owner=owner.parentElement)if(owner._chatMathNodes)owners.push(owner);
  fragment.querySelectorAll('[data-math-tex]').forEach(node=>{
   const id=key(node);let old=saved.get(id)?.shift();
   if(!old)for(const owner of owners){
    const pool=owner._chatMathNodes;if(!pool)continue;
    old=pool.get(id)?.shift();
    if(!pool.get(id)?.length)pool.delete(id);
    if(!pool.size)delete owner._chatMathNodes;
    if(old)break;
   }
   if(old)node.replaceWith(old);else typeset(node);
  });
 };
 function scan(){
  const nodes=Array.from(document.querySelectorAll('[data-math-tex]:not([data-math-ready])')).filter(node=>!node.closest('[data-thread-refresh-preview]'));
  if(!nodes.length)return;
  if(!ready){library();return}
  nodes.forEach(typeset);
 }
 // Mutation observers run before paint; do not defer math to another animation frame.
 new MutationObserver(scan).observe(document.body,{subtree:true,childList:true});scan();
})();
