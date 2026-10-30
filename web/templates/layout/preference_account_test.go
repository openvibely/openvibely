package layout

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

func TestPendingPreferencesAreAccountScoped(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node required")
	}
	var out bytes.Buffer
	if err := Sidebar(nil, "").Render(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	html := out.String()
	marker := strings.Index(html, "if (window.openVibelyProjectSelectorChangeInstalled)")
	if marker < 0 {
		t.Fatal("preference script missing")
	}
	start := strings.LastIndex(html[:marker], "(function() {")
	end := strings.Index(html[start:], "let projectSwitchGeneration = 0;")
	if start < 0 || end < 0 {
		t.Fatal("preference script missing")
	}
	script, _ := json.Marshal(html[start:start+end] + "})();")
	harness := `
const assert=require('node:assert/strict'),vm=require('node:vm');
const storage=new Map();
const old='openvibely.project-preference-outbox.pinned_project_ids';
const key=id=>'openvibely.'+id+'.project-preference-outbox.pinned_project_ids';
const entry=value=>JSON.stringify({version:'v1',value});
const tick=()=>new Promise(resolve=>setImmediate(resolve));
function open(user,response={ok:true,status:204}) {
 const calls=[],events={};let timers=0;
 const context={window:{addEventListener(name,fn){events[name]=fn}},document:{documentElement:{getAttribute(){return user}},getElementById(){return {}}},localStorage:{getItem(k){return storage.get(k)||null},setItem(k,v){storage.set(k,v)},removeItem(k){storage.delete(k)}},setTimeout(){timers++;return timers},clearTimeout(){},AbortController,fetch:async(url,options)=>{calls.push({url,options});return response}};
 vm.runInNewContext(SCRIPT,context);
 return {context,calls,events,timers:()=>timers};
}
(async()=>{
 storage.set(old,entry(['legacy']));storage.set(key('alice'),entry(['alice-project']));
 const bob=open('bob');await tick();assert.equal(bob.calls.length,0);assert.equal(storage.has(old),false);assert.equal(storage.has(key('alice')),true);
 bob.context.window.openVibelySaveProjectPreferences({pinned_project_ids:['bob-project']});await tick();
 assert.equal(bob.calls[0].options.headers['X-Preference-User-ID'],'bob');assert.deepEqual(JSON.parse(bob.calls[0].options.body).pinned_project_ids,['bob-project']);
 assert.equal(storage.has(key('alice')),true);
 const alice=open('alice',{ok:false,status:409});await tick();
 assert.equal(alice.calls.length,1);assert.equal(alice.calls[0].options.headers['X-Preference-User-ID'],'alice');assert.equal(storage.has(key('alice')),true);
 alice.events.online();await tick();assert.equal(alice.calls.length,1,'409 stops replay');
 const pending=open('alice',{ok:false,status:503});await tick();
 open('bob');pending.events.storage({key:'openvibely.active-preference-user',newValue:'bob'});
 pending.events.online();pending.context.window.openVibelySaveProjectPreferences({pinned_project_ids:['bad']});await tick();
 assert.equal(pending.calls.length,1,'old account stops retries');assert.deepEqual(JSON.parse(storage.get(key('alice'))).value,['alice-project']);
 const returning=open('alice');await tick();assert.equal(returning.calls.length,1);assert.equal(storage.has(key('alice')),false);
 const disabled=open('local-owner');disabled.context.window.openVibelySaveProjectPreferences({pinned_project_ids:[]});await tick();assert.equal(disabled.calls[0].options.headers['X-Preference-User-ID'],'local-owner');
})().catch(err=>{console.error(err);process.exitCode=1});
`
	harness = "const SCRIPT=" + string(script) + ";\n" + harness
	if result, err := exec.Command(node, "-e", harness).CombinedOutput(); err != nil {
		t.Fatalf("account scoping: %v\n%s", err, result)
	}
}
