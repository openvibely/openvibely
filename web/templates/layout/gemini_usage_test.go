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
	renderer := slice("  const providerLogoPaths", "  window.openVibelyUsageMeterPreview")
	script := `const assert=require('node:assert/strict');
 class Element {constructor(tag){this.tag=tag;this.attrs={};this.children=[];this.namespaceURI='svg';}setAttribute(k,v){this.attrs[k]=v;}append(n){this.children.push(n);}}
 const document={createElementNS:(_,tag)=>new Element(tag)};
 const window={};
 ` + helpers + renderer + `
 const account={provider:'gemini',data:{limits:[{label:'Group A',used_percent:20},{label:'Group B',used_percent:80},{label:'Unknown'}]}};
 for(const style of ['logos','letters','fill','bars']){
  const svg=svgFor(account,style);
  assert(svg.children.length>0);
  const encoded=JSON.stringify(svg);
  assert(!encoded.includes('NaN'));
  if(style==='letters')assert(encoded.includes('"textContent":"G"'));
  if(style==='bars')assert(svg.children.some(n=>n.tag==='rect'&&Number(n.attrs.width)<10));
 }
 for(const provider of ['openai','anthropic']) for(const style of ['logos','letters','fill','bars']){
  const svg=svgFor({provider,data:{limits:[{window_minutes:10080,used_percent:50},{window_minutes:300,used_percent:25}]}},style);
  assert(svg.children.length>0);
 }
 `
	out, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
}
