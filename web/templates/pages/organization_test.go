package pages

import (
	"bytes"
	"context"
	"os/exec"
	"regexp"
	"testing"

	"github.com/a-h/templ"
)

func TestOrganizationInlineErrors(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is required for UI event tests")
	}
	var out bytes.Buffer
	if err := OrganizationContent(templ.NopComponent).Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	script := regexp.MustCompile(`(?s)<script>(.*?)</script>`).FindSubmatch(out.Bytes())
	if len(script) != 2 {
		t.Fatal("organization script missing")
	}
	harness := `
const assert = require('node:assert/strict');
const listeners = {};
const alert = {textContent:'', hidden:true, focused:false, classList:{remove(){alert.hidden=false}, add(){alert.hidden=true}},focus(){this.focused=true}};
global.window = {};
global.document = {getElementById(id){return id==='organization-error'?alert:null},addEventListener(name,fn){(listeners[name] ||= []).push(fn)}};
`
	checks := `
function fire(name,status,message,inside=true) {
 const event={detail:{elt:{closest(){return inside?{}:null}},xhr:{status,responseText:JSON.stringify({message})}}};
 for(const handler of listeners[name]||[])handler(event);
}
fire('htmx:responseError',409,'the last owner cannot be removed or demoted');
assert.equal(alert.textContent,'the last owner cannot be removed or demoted');
assert.equal(alert.hidden,false); assert.equal(alert.focused,true);
fire('htmx:beforeRequest',0,'');assert.equal(alert.hidden,true);
fire('htmx:responseError',403,'<img src=x onerror=alert(1)>');
assert.equal(alert.textContent,'<img src=x onerror=alert(1)>');
fire('htmx:responseError',500,'secret database details');
assert.equal(alert.textContent,'Could not save this change. Please try again.');
fire('htmx:sendError',0,'');assert.match(alert.textContent,/connection/);
const previous=alert.textContent;
fire('htmx:responseError',409,'unrelated form',false);assert.equal(alert.textContent,previous);
assert.equal(listeners['htmx:responseError'].length,1);
`
	// HTMX navigation executes the page script again; listeners must not accumulate.
	source := harness + string(script[1]) + string(script[1]) + checks
	if result, err := exec.Command(node, "-e", source).CombinedOutput(); err != nil {
		t.Fatalf("inline error behavior: %v\n%s", err, result)
	}
}
