package pages

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_PersonalGitHubIdentityLookup(t *testing.T) {
	view := defaultChannelsSettingsView("project-1")
	view.GitHubPersonal = true
	view.GitHubRevision = "3"
	view.GitHubAuthMode = "pat"
	var content bytes.Buffer
	if err := SettingsContent(view).Render(t.Context(), &content); err != nil {
		t.Fatal(err)
	}
	script := `<script>(async()=>{
 const result=document.getElementById('result');
 const assert=(ok,msg)=>{if(!ok)throw Error(msg)};
 try {
 toggleGitHubAuthMode('pat');
 document.getElementById('github_pat').value='entered-token';
 await lookupGitHubCommitIdentity();
 const name=document.getElementById('github_author_name'),email=document.getElementById('github_author_email');
 assert(name.value==='Alice GitHub'&&email.value==='alice@example.com','lookup did not prefill');
 name.value='Custom author';email.value='custom@example.com';
 await lookupGitHubCommitIdentity();
 assert(name.value==='Custom author'&&email.value==='custom@example.com','lookup overwrote editable identity');
 toggleGitHubAuthMode('app');
 assert(name.disabled&&email.disabled,'App mode must not submit personal identity');
 toggleGitHubAuthMode('pat');
 assert(!name.disabled&&!email.disabled,'PAT identity stayed disabled');
 const data=new FormData(document.getElementById('github_config_form'));
 assert(data.get('github_revision')==='3','missing concurrency revision');
 assert(data.get('github_author_name')==='Custom author','edited author not submitted');
 result.textContent='pass';
 }catch(e){result.textContent=String(e.stack)}
})();</script>`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/channels/github/identity" {
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				http.Error(w, "bad form", 400)
				return
			}
			if r.FormValue("github_pat") != "entered-token" {
				http.Error(w, "bad token field", 400)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"author_name":"Alice GitHub","author_email":"alice@example.com"}`))
			return
		}
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte("<!doctype html><html><body><div id='result'>pending</div>" + content.String() + script + "</body></html>"))
	}))
	defer server.Close()
	runComposerFocusCDP(t, chatNavigationChromePath(t), server.URL, "github-personal-identity", func(b *composerFocusCDP) {
		b.waitFor("GitHub identity lookup", `document.getElementById('result')?.textContent ?? ''`, "pass")
	})
}
