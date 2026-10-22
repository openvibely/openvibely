//go:build darwin && openvibely_native_ui

package pages

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestWorktreeActionsInNativeWailsWebView(t *testing.T) {
	if os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires OPENVIBELY_RUN_NATIVE_UI=1")
	}
	results := make(chan string, 1)
	server := worktreeActionsFixture(t, worktreeMenuNativeRunner, results)
	defer server.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Env = append(os.Environ(), "OPENVIBELY_NATIVE_UI_HELPER=1", "OPENVIBELY_NATIVE_UI_URL="+server.URL+"/")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}()
	select {
	case result := <-results:
		if result != "pass" {
			t.Fatal(result)
		}
	case err := <-done:
		t.Fatalf("native host exited: %v %s", err, output.String())
	case <-time.After(45 * time.Second):
		t.Fatal("native menu test timed out")
	}
}

const worktreeMenuNativeRunner = `<script>
window.addEventListener('DOMContentLoaded',()=>{
 const wait=ms=>new Promise(r=>setTimeout(r,ms));
 (async()=>{
  for(let i=0;i<500 && !document.querySelector('#changes-actions-dropdown');i++)await wait(10);
  const root=document.querySelector('#changes-actions-dropdown');
  if(!root)throw Error('Missing Actions');
  root.parentElement.style.cssText='position:relative;z-index:0;transform:translateZ(0)';
  const button=root.querySelector('button[popovertarget]'),menu=root.querySelector('ul');
  for(const mode of ['inline','split']) {
   switchDiffView(mode,false); await wait(100);
   button.click(); await wait(100);
   if(!menu.matches(':popover-open'))throw Error('Menu not in top layer');
   const header=Array.from(document.querySelectorAll('.diff-file-header')).find(e=>e.getClientRects().length);
   const a=menu.getBoundingClientRect(),b=header.getBoundingClientRect();
   const x=(Math.max(a.left,b.left)+Math.min(a.right,b.right))/2,y=(Math.max(a.top,b.top)+Math.min(a.bottom,b.bottom))/2;
   if(Math.min(a.bottom,b.bottom)<=Math.max(a.top,b.top))throw Error('No header overlap');
   if(!menu.contains(document.elementFromPoint(x,y)))throw Error('Menu obscured in '+mode);
   button.click();await wait(50);
   if(menu.matches(':popover-open'))throw Error('Toggle did not close');
  }
  button.click();await wait(50);window.dispatchEvent(new Event('resize'));await wait(50);
  if(!menu.matches(':popover-open') || menu.getBoundingClientRect().right>innerWidth)throw Error('Resize lost menu position');
  await fetch('/menu-result?status=pass');
 })().catch(e=>fetch('/menu-result?status='+encodeURIComponent(String(e))));
});</script>`
