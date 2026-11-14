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
 let previewKey=JSON.stringify(['project','task']),retained=null,reports=0,closed=false,generation=1,frameTimer=null;
 let live=canvas();live.painted=true;
 const screen={style:{},append(){},querySelector(){return live}};
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
 for(let i=0;i<20;i++){previewKey=JSON.stringify(['project',i]);rememberFrame(live)}
 assert.equal(previewCache.size,12);assert.equal(previewCache.has(oldKey),false);
 `
	output, err := exec.Command(node, "-e", script).CombinedOutput()
	if err != nil {
		t.Fatalf("cache regression: %v\n%s", err, output)
	}
}
