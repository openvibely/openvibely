package pages

import (
	"bytes"
	"context"
	"fmt"
	"github.com/openvibely/openvibely/web/static"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBrowserFunctional_ServerPasswordReveal(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	var content bytes.Buffer
	if err := ServersContent(ServersPageData{}).Render(context.Background(), &content); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		fmt.Fprintf(w, `<!doctype html><html><head><link rel="stylesheet" href="%s"></head><body>%s</body></html>`, static.URL("app.css"), content.String())
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL, "servers-password", func(browser *composerFocusCDP) {
		browser.waitFor("server modal", `document.readyState+':'+typeof openServerModal`, "complete:function")
		browser.evaluate(`openServerModal();document.getElementById('server_password').value='test-password';'ok'`)
		browser.click("#server_modal .password-toggle-btn")
		browser.waitFor("revealed password", `document.getElementById('server_password').type`, "text")
		browser.click("#server_modal .password-toggle-btn")
		browser.waitFor("hidden password", `document.getElementById('server_password').type`, "password")
		browser.click("#server_modal .password-toggle-btn")
		browser.evaluate(`server_modal.close();openServerModal();'ok'`)
		browser.waitFor("reset password", `document.getElementById('server_password').type+':'+document.getElementById('server_password').value`, "password:")
	})
}
