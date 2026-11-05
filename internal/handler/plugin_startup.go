package handler

import (
	"encoding/json"
	"strings"
)

// Install the observer before plugin scripts, without changing installed files or
// weakening the sandbox. A successful startup still requires explicit readiness.
func instrumentPluginHTML(html, handle string) string {
	token, _ := json.Marshal(handle)
	script := `<script>(function(){var h=` + string(token) + `,n=0;function fail(message){try{parent.postMessage({v:1,type:'page.failed',id:'startup-error-'+(++n),handle:h,payload:{error:String(message).slice(0,500)}},'*');}catch(_){}}window.addEventListener('error',function(e){fail(e.message||'A page resource failed to load.');},true);window.addEventListener('unhandledrejection',function(e){fail(e.reason&&e.reason.message||e.reason||'Unhandled page error.');});})();</script>`
	// Keep the doctype first so the browser retains standards mode.
	trimmed := strings.TrimLeft(html, " \t\r\n\ufeff")
	if strings.HasPrefix(strings.ToLower(trimmed), "<!doctype") {
		if end := strings.Index(trimmed, ">"); end >= 0 {
			at := len(html) - len(trimmed) + end + 1
			return html[:at] + script + html[at:]
		}
	}
	return script + html
}
