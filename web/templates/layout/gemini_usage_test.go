package layout

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestGeminiUsageMeterStyles(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	raw, err := os.ReadFile("account_usage.templ")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	slice := func(start, end string) string {
		a := strings.Index(source, start)
		if a < 0 {
			t.Fatal(start)
		}
		b := strings.Index(source[a:], end)
		if b < 0 {
			t.Fatal(end)
		}
		return source[a : a+b]
	}
	helpers := slice("  const limits =", "  const nativePopover")
	renderer := slice("  const providerLogoPaths", "  document.dispatchEvent(new CustomEvent(\"openvibely:usage-meter-ready")
	script := `const assert=require('node:assert/strict');
 class Element {constructor(tag){this.tag=tag;this.attrs={};this.children=[];this.namespaceURI='svg';}setAttribute(k,v){this.attrs[k]=v;}append(n){this.children.push(n);}}
 const document={createElementNS:(_,tag)=>new Element(tag)};
 const window={};
 ` + helpers + renderer + `
 const account={provider:'gemini',data:{limits:[{label:'Gemini Models · weekly limit',used_percent:20},{label:'Gemini Models · 5-hour session',used_percent:80},{label:'Unknown'}]}};
 const others=[{label:'Claude and GPT models · weekly limit',used_percent:99},{label:'Gemini 4 Argon · 5-hour session',used_percent:100}];
 assert.equal(geminiMainLimit({data:{limits:others}},10080),undefined);
 assert.equal(geminiMainLimit({data:{limits:others}},300),undefined);
 for(const style of ['logos','circle-dots','circle-dots-number','fill','bars']){
  const svg=svgFor(account,style);
  assert(svg.children.length>0);
  logoSequence=0;
  const expected=svgFor(account,style);
  logoSequence=0;
  assert.deepEqual(svgFor({...account,data:{limits:[...others,...account.data.limits]}},style),expected);
  assert.equal(account.data.limits.length,3);
  const encoded=JSON.stringify(svg);
  assert(!encoded.includes('NaN'));
  if(style.startsWith('circle-dots')){
   assert.equal(svg.children.filter(n=>n.tag==='circle').length,4);
   assert(svg.children.some(n=>n.tag==='path'&&n.attrs.d.endsWith(' Z')));
   const weeklyOnly=svgFor({...account,data:{limits:account.data.limits.slice(0,1)}},style);
   assert(!weeklyOnly.children.some(n=>n.tag==='circle'));
   const tracks=weeklyOnly.children.filter(n=>n.attrs.stroke==='var(--ov-usage-used)');
   assert.equal(tracks.length,1);
   if(style==='circle-dots')assert(tracks[0].attrs.d.endsWith(' Z'));
   else {
    assert(tracks[0].attrs.d.includes('0 1 1'));
    assert.equal(weeklyOnly.children.find(n=>n.tag==='text').textContent,'80');
   }
   assert.equal(weeklyOnly.children.find(n=>n.attrs['stroke-dasharray'])?.attrs['stroke-dasharray'],'80 100');
   assert.equal(svg.children.find(n=>n.tag==='text')?.textContent,style==='circle-dots-number'?'80':undefined);
  }
  if(style==='bars')assert(svg.children.some(n=>n.tag==='rect'&&Number(n.attrs.width)<10));
 }
 const preview=window.openVibelyUsageMeterPreview('gemini','logos');
 assert.equal(preview.children.filter(n=>n.tag==='circle'&&!n.attrs['stroke-dasharray']).length,2);
 for(const provider of ['openai','anthropic']) for(const style of ['logos','circle-dots','circle-dots-number','fill','bars']){
  const svg=svgFor({provider,data:{limits:[{window_minutes:10080,used_percent:50},{window_minutes:300,used_percent:25}]}},style);
  assert(svg.children.length>0);
 }
 `
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
