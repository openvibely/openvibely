package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_PageLifecycleCleanupInChrome(t *testing.T) {
	for _, name := range []string{"agents", "analytics"} {
		t.Run(name, func(t *testing.T) {
			var rendered bytes.Buffer
			var err error
			if name == "agents" {
				err = AgentsContent(nil, nil).Render(context.Background(), &rendered)
			} else {
				err = AnalyticsContent(&models.Project{ID: "p", Name: "Project"}).Render(context.Background(), &rendered)
			}
			if err != nil {
				t.Fatal(err)
			}
			page := rendered.String()
			if name == "agents" {
				// Exercise the real installer independently of unrelated page globals.
				// Replaying the entire legacy script redeclares top-level let bindings.
				start := strings.Index(page, "(function installAgentErrorHandler()")
				if start < 0 {
					t.Fatal("missing Agents error handler installer")
				}
				length := strings.Index(page[start:], "})();")
				if length < 0 {
					t.Fatal("unterminated Agents error handler installer")
				}
				end := start + length + len("})();")
				installer := page[start:end]
				page = regexp.MustCompile(`(?s)<script\b[^>]*>.*?</script>`).ReplaceAllString(page, "") + "<script>" + installer + "</script>"
			}
			markup, _ := json.Marshal(page)
			kind, _ := json.Marshal(name)
			fixture := `<main id="reconnect-result"></main><main id="main-content"></main><script src="` + static.URL("vendor/htmx.min.js") + `"></script><script>
window.addEventListener('load', async function() {
 const result=document.getElementById('reconnect-result'),main=document.getElementById('main-content');
 const markup=` + string(markup) + `,kind=` + string(kind) + `;
 const wait=()=>new Promise(r=>setTimeout(r,50));
 let toasts=0,destroyed=0;const errors=[];window.addEventListener('error',e=>errors.push(e.message));
 window.showToast=()=>toasts++;
 window.Chart=function(){};
 window.fetch=()=>new Promise(()=>{});
 function check(ok,message){if(!ok)throw Error(message);}
 function error(target){document.body.dispatchEvent(new CustomEvent('htmx:responseError',{bubbles:true,cancelable:true,detail:{target,xhr:{status:403,responseText:'Denied'}}}));}
 try {
  for(let visit=0;visit<3;visit++) {
   history.replaceState({},'', '/'+kind+'?project_id=p');
   htmx.swap(main,markup,{swapStyle:'innerHTML',settleDelay:0});await wait();
   if(kind==='agents') {
    const target=document.getElementById('agents-container');
    error(target);check(toasts===2*visit+1,'duplicate or missing Agents error handler visit='+visit+' count='+toasts+' errors='+errors.join(';'));
    document.getElementById('agent_modal').close();
    // A card-list-only update must not remove the page's error handler.
    htmx.swap(document.getElementById('agents-card-list'),'<p>No agents</p>',{swapStyle:'innerHTML',settleDelay:0});
    error(target);check(toasts===2*(visit+1),'list refresh removed Agents handler');
    document.getElementById('agent_modal').close();
    htmx.swap(main,'<p>Tasks</p>',{swapStyle:'innerHTML',settleDelay:0});await wait();
    error(target);check(toasts===2*(visit+1),'Agents handler survived page cleanup');
   } else {
    check(document.querySelector('[data-analytics-preview]'),'Analytics did not initialize');
    const charts=window._analyticsCharts;
    const canvas=document.createElement('canvas');main.append(canvas);
    charts.test={canvas,destroy(){destroyed++;}};
    charts.throwing={destroy(){destroyed++;throw Error('simulated Chart cleanup failure');}};
    const link=document.createElement('a');link.href='/tasks';main.append(link);
    link.addEventListener('click',event=>event.preventDefault());
    for(const options of [{button:2},{button:1},{button:0,metaKey:true},{button:0,ctrlKey:true},{button:0}]) {
     link.dispatchEvent(new PointerEvent('pointerdown',{bubbles:true,cancelable:true,...options}));
     link.dispatchEvent(new MouseEvent('click',{bubbles:true,cancelable:true,...options}));
     check(destroyed===2*visit,'navigation intent destroyed charts before departure');
     check(!window._analyticsAbortController.signal.aborted,'navigation intent disposed Analytics');
    }
    link.target='_blank';link.click();
    check(destroyed===2*visit,'new-tab link destroyed charts');
    document.body.dispatchEvent(new CustomEvent('htmx:beforeRequest',{bubbles:true,detail:{target:main,pathInfo:{requestPath:'/tasks'}}}));
    check(destroyed===2*visit,'request start destroyed charts before a successful swap');
    check(!window._analyticsAbortController.signal.aborted,'request start disposed the still-visible page');
    if(visit===2) {
     window.dispatchEvent(new Event('pagehide'));
     check(destroyed===2*(visit+1),'document departure did not destroy charts');
    }
    htmx.swap(main,'<p>Tasks</p>',{swapStyle:'innerHTML',settleDelay:0});await wait();
    check(destroyed===2*(visit+1),'charts not destroyed exactly once on departure');
    check(Object.keys(charts).length===0,'departed chart map retains objects');
    check(!canvas.isConnected,'old chart canvas remains attached');
    check(!document.querySelector('[data-analytics-preview]'),'preview dialog survived departure');
   }
  }
  result.setAttribute('data-test-result','pass');
 } catch(e) {result.setAttribute('data-test-result','fail');result.setAttribute('data-test-error',String(e));}
});</script>`
			runReconnectChromeFixture(t, fixture)
		})
	}
}
