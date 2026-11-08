package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
)

func TestBrowserFunctional_ChatQuestionPermissionPreservesAnswer(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	requests := []*events.ChatInputRequestEvent{{ID: "question", ProjectID: "project", Questions: []events.ChatInputRequestQuestion{{ID: "choice", Question: "Choose an option", Options: []events.ChatInputRequestOption{{Label: "Keep answer"}, {Label: "Other answer"}}}}}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		if r.URL.Path == "/chat/input-requests" {
			_ = ChatInputRequestCards(requests).Render(r.Context(), w)
			return
		}
		if r.URL.Path != "/chat" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if err := Chat([]models.Project{{ID: "project", Name: "Project"}}, "project", nil, nil, nil, nil, requests, false, false, 20).Render(r.Context(), w); err != nil {
			t.Error(err)
		}
	}))
	defer server.Close()
	runComposerFocusCDP(t, chrome, server.URL+"/chat", "question-permission", func(b *composerFocusCDP) {
		b.waitFor("chat initialized", `String(!!window._chatInputRequestClickBound && !!document.querySelector('[data-chat-input-option]') && !document.querySelector('#chat-messages[data-transcript-hydrating]'))`, "true")
		b.evaluate(`window.permissionToasts=[];window.showToast=(m)=>permissionToasts.push(m);window.realFetch=window.fetch;window.fetch=(url,opts)=>String(url).includes('/answer')?Promise.resolve(new Response(JSON.stringify({message:'Editor access required'}),{status:403,headers:{'X-OpenVibely-Permission-Denied':'true'}})):realFetch(url,opts);'ready'`)
		b.click(`[data-option-label="Keep answer"]`)
		b.evaluate(`window.questionCard=document.querySelector('[data-chat-input-request-id]');window.questionStatus=questionCard.querySelector('[role="status"]');window.oldQuestionStatus=questionStatus.textContent;window.oldQuestionClass=questionStatus.className;'ready'`)
		b.click(`[data-chat-input-nav="submit"]`)
		b.waitFor("permission toast", `String(permissionToasts.length)`, "1")
		b.waitFor("answer restored", `String(!questionCard.hasAttribute('data-submitted') && questionStatus.textContent===oldQuestionStatus && questionStatus.className===oldQuestionClass && !questionCard.querySelector('[data-chat-input-nav="submit"]').disabled)`, "true")
		if result := b.evaluate(`permissionToasts[0]==='Editor access required' && questionCard.querySelector('[data-option-label="Keep answer"]').getAttribute('aria-pressed')==='true'?'ok':'answer lost'`); result != "ok" {
			t.Fatal(result)
		}
	})
}
