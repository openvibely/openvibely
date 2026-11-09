package handler

import (
	"encoding/json"
	"strings"
)

// pluginHTMLPrefixBytes is enough to find a leading doctype; the rest of the
// entry file is streamed unchanged.
const pluginHTMLPrefixBytes = 1024

// Install the observer before plugin scripts, without changing installed files or
// weakening the sandbox. A successful startup still requires explicit readiness.
func instrumentPluginHTML(html, handle string) string {
	token, _ := json.Marshal(handle)
	script := `<script>(function(){var h=` + string(token) + `,n=0;function fail(message){try{parent.postMessage({v:1,type:'page.failed',id:'startup-error-'+(++n),handle:h,payload:{error:String(message).slice(0,500)}},'*');}catch(_){}}window.addEventListener('error',function(e){fail(e.message||'A page resource failed to load.');},true);window.addEventListener('unhandledrejection',function(e){fail(e.reason&&e.reason.message||e.reason||'Unhandled page error.');});document.addEventListener('click',externalLink,true);document.addEventListener('auxclick',externalLink,true);function externalLink(e){var a=e.target&&e.target.closest&&e.target.closest('a[href]');if(!a)return;try{var u=new URL(a.href,location.href);if((u.protocol==='https:'||u.protocol==='http:')&&u.origin!==location.origin){a.target='_blank';a.relList.add('noopener','noreferrer');}}catch(_){}}})();</script>`
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
