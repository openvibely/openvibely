package components

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestBrowserFunctional_ChannelSenderSavesOnlySelectedRow(t *testing.T) {
	chrome := testChromePath(t)
	ctx := models.WithChannelSenderOptions(context.Background(), models.ChannelSenderOptions{Authenticated: true, Actor: models.Principal{UserID: "admin", Role: "admin"}, Users: []models.OrgUser{{Principal: models.Principal{UserID: "alice", Username: "alice"}}, {Principal: models.Principal{UserID: "bob", Username: "bob"}}}})
	var rendered bytes.Buffer
	err := TelegramAuthorizedUsersList([]models.TelegramAuthorizedUser{
		{ID: "one", TelegramUserID: 123, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: "alice"}},
		{ID: "two", TelegramUserID: 456, ChannelSenderMapping: models.ChannelSenderMapping{ApplicationUserID: "bob"}},
	}, "default").Render(ctx, &rendered)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/htmx.js" {
			w.Header().Set("Content-Type", "text/javascript")
			w.Write(htmx2011)
			return
		}
		if r.Method == "POST" {
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			want := "bob"
			if r.URL.Path == "/channels/telegram/authorized-users" {
				want = "alice"
				if r.Form.Get("user_id_or_username") != "789" {
					t.Errorf("wrong added sender: %v", r.Form)
				}
			}
			if got := r.Form["application_user_id"]; len(got) != 1 || got[0] != want {
				t.Errorf("row mapping = %v, want only %s", got, want)
			}
			if r.Form.Has("token") {
				t.Error("sender mutation included the bot credential")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprint(w, `<!doctype html><html><head><script src="/htmx.js"></script></head><body data-test-result="pending"><form><input name="token" value="fake-secret">`+rendered.String()+`</form><script>
 window.addEventListener('load',async()=>{try{
 const send=button=>new Promise(resolve=>{document.body.addEventListener('htmx:afterRequest',resolve,{once:true});button.click()});
 await send(document.querySelector('#telegram-authorized-users-two button[hx-post]'));
 const add=document.getElementById('telegram-authorized-users-add-controls');
 add.querySelector('[name="user_id_or_username"]').value='789';
 add.querySelector('[name="application_user_id"]').value='alice';
 await send(add.querySelector('button'));
 document.body.setAttribute('data-test-result','pass');
 }catch(e){document.body.setAttribute('data-test-result','fail');document.body.setAttribute('data-test-error',String(e))}});
 </script></body></html>`)
	}))
	defer server.Close()
	runHeadlessChromeCDPFixture(t, chrome, server.URL, "channel sender row isolation", 1000, 800, 20*time.Second)
}
