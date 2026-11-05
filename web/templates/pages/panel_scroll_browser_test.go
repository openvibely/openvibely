package pages

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestAppScrollingUsesPanelHelper(t *testing.T) {
	// Vendor libraries and browser-test drivers are not application call sites.
	direct := regexp.MustCompile(`\.scrollIntoView(?:IfNeeded)?\s*\(`)
	for _, root := range []string{"..", "../../assets"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "file-vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".templ") && !strings.HasSuffix(path, ".js") {
				return nil
			}
			if strings.HasSuffix(path, ".min.js") {
				return nil
			}
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if direct.Match(content) {
				t.Errorf("%s: use openVibelyScrollIntoPanel so reveal actions cannot scroll the app shell", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestBrowserFunctional_PanelScrollIsolation(t *testing.T) {
	fixture := `<style>
html,body{margin:0;height:100%;overflow:hidden}
#shell{height:400px;overflow:hidden}header{height:54px}
#panel{height:400px;width:300px;overflow:auto;border:3px solid transparent}
#space{height:1200px;width:900px;position:relative}
#target{position:absolute;top:700px;left:600px;width:100px;height:80px}
</style><main id="reconnect-result"></main><div id="shell"><header>Tabs</header><div id="panel"><div id="space"><div id="target">Target</div></div></div></div>
<script>
try {
 const panel=document.getElementById('panel'),target=document.getElementById('target');
 const check=(ok,msg)=>{if(!ok)throw Error(msg)};
 const stable=()=>{check(document.querySelector('header').getBoundingClientRect().top===0,'title bar moved');check(!document.body.scrollTop&&!document.documentElement.scrollTop&&!document.getElementById('shell').scrollTop,'shell scrolled')};
 check(openVibelyScrollIntoPanel(target,{block:'center',inline:'center'}),'panel not discovered');
 check(panel.scrollTop===540&&panel.scrollLeft===500,'centering must account for borders');stable();
 openVibelyScrollIntoPanel(target);check(panel.scrollTop===540&&panel.scrollLeft===500,'nearest moved a visible target');stable();
 panel.scrollTop=0;panel.scrollLeft=0;openVibelyScrollIntoPanel(target);
 check(panel.scrollTop===380&&panel.scrollLeft===400,'nearest reveal failed');stable();
 target.style.height='600px';openVibelyScrollIntoPanel(target,{block:'center'});
 check(panel.scrollTop===700,'oversized target should start at panel top');stable();
 check(!openVibelyScrollIntoPanel(target,{container:document.body}),'body must not be scrollable');
 check(!openVibelyScrollIntoPanel(target,{container:document.querySelector('header')}),'unrelated container accepted');
 check(!openVibelyScrollIntoPanel(document.createElement('div')),'detached target accepted');
 check(!openVibelyScrollIntoPanel(null),'missing target accepted');stable();
 document.getElementById('reconnect-result').dataset.testResult='pass';
}catch(e){const r=document.getElementById('reconnect-result');r.dataset.testResult='fail';r.dataset.testError=e.message;}
</script>`
	runReconnectChromeFixture(t, fixture)
}
