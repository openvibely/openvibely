(()=>{
 const m=globalThis.marked;if(!m||globalThis.ovMathParser)return;globalThis.ovMathParser=true;
 const escape=value=>value.replaceAll('&','&amp;').replaceAll('<','&lt;').replaceAll('>','&gt;').replaceAll('"','&quot;');
 const render=token=>'<span data-math-tex="'+escape(token.text)+'" data-math-display="'+(token.display?'block':'inline')+'">'+escape(token.text)+'</span>';
 m.use({extensions:[
 {name:'ovBlockMath',level:'block',start:src=>{const at=src.search(/(?:^|\n)(?:\$\$|\\\[)/);return at<0?undefined:at},tokenizer(src){const match=/^(?:\$\$\s*\n?([\s\S]+?)\n?\$\$|\\\[([\s\S]+?)\\\])(?:\n|$)/.exec(src);if(match)return{type:'ovBlockMath',raw:match[0],text:match[1]??match[2],display:true}},renderer:render},
 {name:'ovInlineMath',level:'inline',start:src=>{const at=src.search(/\$|\\\(/);return at<0?undefined:at},tokenizer(src){const match=/^(?:\\\(([^\n]+?)\\\)|\$([^\s$](?:[^$\n]*?[^\s$])?)\$(?!\d))/.exec(src);if(match)return{type:'ovInlineMath',raw:match[0],text:match[1]??match[2],display:false}},renderer:render}
 ]});
})();
