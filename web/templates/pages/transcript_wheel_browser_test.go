package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestBrowserFunctional_TranscriptWheelAndPrependInChrome(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	source, err := os.ReadFile("../../assets/chat-virtual-list.js")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/virtual.js" {
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(source)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><div id="task-thread-messages" style="height:500px;width:600px;overflow:auto"><div id="spacer" style="height:10000px"></div><div id="tool" style="position:fixed;left:20px;top:20px;width:100px;height:100px;overflow:auto"><div style="height:2000px">nested</div></div></div><script src="/virtual.js"></script><script>
 document.dispatchEvent(new Event('htmx:afterSwap'));
 const m=document.getElementById('task-thread-messages');m.scrollTop=1000;
 window.events=[];
 m.addEventListener('wheel',e=>events.push({trusted:e.isTrusted,cancelled:e.defaultPrevented,dy:e.deltaY,top:m.scrollTop}));
 window.ready=true;
 </script>`))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "transcript-wheel", func(c *composerFocusCDP) {
		c.waitFor("wheel fixture", "String(window.ready)", "true")
		for i := 0; i < 20; i++ {
			if i == 5 {
				c.evaluate(`document.getElementById('spacer').style.height='11000px';m.scrollTop+=1000;'ok'`)
			}
			c.wheel("#task-thread-messages", -2.4)
			c.waitFor("synchronous trusted wheel", `String(events.length>0 && events.every(e=>e.trusted && e.cancelled))`, "true")
		}
		if got := c.evaluate(`String(Math.abs(m.scrollTop-(2000+events.reduce((n,e)=>n+e.dy,0)))<=1)`); got != "true" {
			t.Fatalf("wheel movement lost across prepend: %s", c.evaluate(`JSON.stringify({top:m.scrollTop,events})`))
		}
		c.evaluate(`window.outer=m.scrollTop;document.getElementById('tool').scrollTop=100;events=[];'ok'`)
		c.wheel("#tool", 40)
		c.waitFor("nested movement", `String(document.getElementById('tool').scrollTop>100)`, "true")
		if got := c.evaluate(`String(events.length>0 && events.every(e=>e.trusted && !e.cancelled) && m.scrollTop===outer)`); got != "true" {
			t.Fatal("nested wheel intercepted or moved transcript")
		}
	})
}
