package pages

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestModelOAuthCardCompletion(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	raw, err := os.ReadFile("models.templ")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "function launchOAuthInSystemBrowser")
	end := strings.Index(source[start:], "function syncModelAPIKeySubmitValue")
	script := source[start:start+end] + `
const assert = require('node:assert/strict');
const badge = {textContent:'Token expired',className:'badge-warning'};
let calls = 0, timers = [], removed = false;
global.window = {showToast:()=>{}};
global.modelMutationURL = path => path + '?project_id=test';
global.setTimeout = resolve => timers.push(resolve);
const anchor = {dataset:{oauthExternal:'true'},textContent:'Connect OAuth',isConnected:true,
 setAttribute(){},removeAttribute(){},parentElement:{querySelector:()=>badge},
 remove(){removed=true;this.isConnected=false;}};
global.fetch = async (path, options) => {
 calls++;
 if(calls === 1) {
  assert.equal(path, '/models/test/oauth/initiate?project_id=test');
  assert.equal(options.headers.Accept, 'application/json');
  return {ok:true,json:async()=>({opened:true})};
 }
 assert.equal(path, '/models/test/oauth/status?project_id=test');
 return {ok:true,json:async()=>({status:calls===2?'expired':'connected'})};
};
(async()=>{
 // Browser links navigate normally and preserve the selected project without fetching.
 const browserAnchor = {dataset:{oauthExternal:'false'},href:''};
 assert.equal(launchOAuthInSystemBrowser('/models/test/oauth/initiate',browserAnchor),true);
 assert.equal(browserAnchor.href,'/models/test/oauth/initiate?project_id=test');
 assert.equal(calls,0);
 assert.equal(launchOAuthInSystemBrowser('/models/test/oauth/initiate',anchor),false);
 launchOAuthInSystemBrowser('/models/test/oauth/initiate',anchor);
 await new Promise(setImmediate);
 assert.equal(calls,2);
 assert.equal(removed,false);
 assert.equal(badge.textContent,'Token expired');
 timers.shift()();
 await new Promise(setImmediate);
 assert.equal(badge.textContent,'Connected');
 assert.equal(removed,true);
 assert.equal(anchor.dataset.oauthWaiting,undefined);
 // A removed card stops polling without mutating a replacement page.
 anchor.isConnected=true; removed=false; calls=0;
 launchOAuthInSystemBrowser('/models/test/oauth/initiate',anchor);
 await new Promise(setImmediate);
 anchor.isConnected=false;
 timers.shift()();
 await new Promise(setImmediate);
 assert.equal(calls,2);
 assert.equal(removed,false);
 // A cancelled/failed browser flow leaves status expired. Retry immediately,
 // including while the old status request is still in flight.
 anchor.isConnected=true; badge.textContent='Token expired'; timers=[];
 let starts=0, polls=0, finishOld;
 global.fetch=async(path)=>{
  if(path.includes('/initiate')) {starts++; return {ok:true,json:async()=>({opened:true})};}
  polls++;
  if(polls===1) return new Promise(resolve=>{finishOld=()=>resolve({ok:true,json:async()=>({status:'connected'})});});
  return {ok:true,json:async()=>({status:polls===2?'expired':'connected'})};
 };
 launchOAuthInSystemBrowser('/models/test/oauth/initiate',anchor);
 await new Promise(setImmediate);
 assert.equal(anchor.textContent,'Retry OAuth');
 assert.equal(anchor.dataset.oauthWaiting,undefined);
 launchOAuthInSystemBrowser('/models/test/oauth/initiate',anchor);
 await new Promise(setImmediate);
 assert.equal(starts,2);
 finishOld();
 await new Promise(setImmediate);
 assert.equal(removed,false);
 assert.equal(badge.textContent,'Token expired');
 assert.equal(anchor.textContent,'Retry OAuth');
 timers.shift()();
 await new Promise(setImmediate);
 assert.equal(removed,true);
 assert.equal(badge.textContent,'Connected');
})().catch(error=>{console.error(error);process.exitCode=1;});
`
	cmd := exec.Command(node, "-e", script)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}
