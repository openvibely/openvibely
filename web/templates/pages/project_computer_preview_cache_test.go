package pages

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestComputerPreviewCache(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "     function rememberFrame(")
	end := strings.Index(text, "     async function viewer(")
	if start < 0 || end < start {
		t.Fatal("preview functions missing")
	}
	script := `const assert=require('node:assert/strict');
 function canvas(){return {width:1280,height:800,dataset:{},style:{},painted:false,removed:false,setAttribute(){},remove(){this.removed=true},getContext(){const self=this;return {drawImage(src){self.painted=src.painted},getImageData(){return {data:[0,0,0,self.painted?255:0]}}}}}}
 const document={createElement:canvas},previewCache=new Map();
 let previewDeleted=false;
 let previewKey=JSON.stringify(['project','task']),retained=null,reports=0,closed=false,generation=1,frameTimer=null;
 let live=canvas();live.painted=true;let liveCanvas=live;
 const screen={style:{},append(){},querySelector(){return live}};
 let frameReady=false,connected=true,everConnected=false,connectTimer=null,rfb=null;
 const screenRetry={success(){}};function renderConnection(){};
 function reportConnection(){reports++}
 function clearTimeout(){};function setTimeout(){return 1}
 ` + text[start:end] + `
 retainFrame();assert.equal(previewCache.size,1);assert.equal(previewCache.get(previewKey).width,640);
 const oldKey=previewKey;
 retained=null;previewKey=JSON.stringify(['other-project','task']);restoreFrame();assert.equal(retained,null);
 previewKey=JSON.stringify(['project','other-task']);restoreFrame();assert.equal(retained,null);
 previewKey=oldKey;restoreFrame();assert.ok(retained.painted);
 live=canvas();revealFrame(1);assert.ok(retained,'blank live canvas must not replace cached frame');
 live.painted=true;revealFrame(1);assert.equal(retained,null);
 liveCanvas=live;live=null;retainFrame();assert.ok(retained?.painted,'detached live canvas must survive an unexpected disconnect');
 live=liveCanvas;
 for(let i=0;i<20;i++){previewKey=JSON.stringify(['project',i]);rememberFrame(live)}
 assert.equal(previewCache.size,12);assert.equal(previewCache.has(oldKey),false);
 previewDeleted=true;previewCache.clear();rememberFrame(live);assert.equal(previewCache.size,0);
 `
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("cache regression: %v\n%s", err, output)
	}
}

func TestComputerPreviewDeletionEvents(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	source, err := os.ReadFile("project_computer.templ")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "     if(!window.__ovComputerPreviewCache)")
	end := strings.Index(text, "     const previewCache=")
	if start < 0 || end < start {
		t.Fatal("cache initializer missing")
	}
	script := `const assert=require('node:assert/strict');
 const listeners={};const window={addEventListener(name,fn){listeners[name]=fn},dispatchEvent(){}};
 const document={body:{addEventListener(name,fn){listeners[name]=fn}}};
 class CustomEvent{constructor(type,options){this.type=type;this.detail=options.detail}}
 ` + text[start:end] + `
 const cache=window.__ovComputerPreviewCache;
 const key=(p,t)=>JSON.stringify([p,t]);
 cache.set(key('a','task'),1);cache.set(key('b','task'),2);cache.set(key('a','other'),3);
 listeners['sse-task-event']({type:'sse-task-event',detail:{type:'task_deleted',project_id:'a',task_id:'task'}});
 assert.equal(cache.has(key('a','task')),false);assert.equal(cache.size,2);
 listeners.taskDeleted({type:'taskDeleted',detail:{project_id:'b',task_id:'task'}});
 assert.equal(cache.has(key('b','task')),false);assert.equal(cache.size,1);
 listeners['sse-task-event']({type:'sse-task-event',detail:{type:'task_status_changed',project_id:'a',task_id:'other'}});
 assert.equal(cache.size,1);
 `
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("deletion regression: %v\n%s", err, output)
	}
}
