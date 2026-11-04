package handler

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/stretchr/testify/require"

	"github.com/openvibely/openvibely/web/static"
)

func TestProjectQuestionPickerInChrome(t *testing.T) { testProjectQuestionPickerInChrome(t, false) }
func TestAllProjectsChatQuestionPickerInChrome(t *testing.T) {
	testProjectQuestionPickerInChrome(t, true)
}
func testProjectQuestionPickerInChrome(t *testing.T, all bool) {
	for _, width := range []int{1024, 390} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			if testing.Short() {
				t.Skip("browser")
			}
			chrome := findChromeForBrowserTest(t)
			if chrome == "" {
				t.Skip("Chrome not found")
			}
			tc := NewStoreTestContext(t)
			project := tc.CreateProject().WithName("API").Build()
			second := tc.CreateProject().WithName("Website").Build()
			scope := project.ID
			if all {
				scope = models.AllProjectsID
			}
			pending, err := tc.handler.chatInputRequests.create(scope, "exec-picker", []chatInputRequestQuestion{
				{ID: "model", Question: "Which model?", Options: []chatInputRequestOption{{Label: "Default", Description: "Use default"}, {Label: "Fast", Description: "Use fast"}}},
				{ID: "scope", Kind: "projects", Question: "Which projects?", Options: []chatInputRequestOption{{Label: "All projects", Description: "All"}, {Label: "Specific projects", Description: "Choose"}}, Projects: []events.ChatInputProject{{ID: project.ID, Name: project.Name}, {ID: second.ID, Name: second.Name}}},
			})
			require.NoError(t, err)
			result := make(chan string, 1)
			runner := fmt.Sprintf(`<script>(async function(){
 const wait=async f=>{for(let i=0;i<200;i++){if(f())return;await new Promise(r=>setTimeout(r,25))}throw Error('timeout')};
 const check=(v,m)=>{if(!v)throw Error(m)};
 try{
 await wait(()=>document.querySelector('[data-chat-input-request-id="%s"]'));
 const card=document.querySelector('[data-chat-input-request-id="%s"]');
 await wait(()=>card.isConnected&&card.checkVisibility({visibilityProperty:true})&&!card.closest('[hidden],[inert]'));
 card.querySelector('[data-chat-input-nav="recommended"]').click();
 const group=card.querySelector('[data-chat-input-question-id="scope"]');
 check(!group.classList.contains('hidden'),'recommended did not stop at projects');
 check(!group.dataset.selectedLabel,'recommended selected project scope');
 check(!group.querySelector('[data-chat-input-custom-option]'),'unexpected text alternative');
 group.querySelector('[data-option-label="Specific projects"]').click();
 const picker=group.querySelector('[data-project-picker]'),search=group.querySelector('[data-project-search]'),list=group.querySelector('[data-project-results]');
 await wait(()=>search.checkVisibility({visibilityProperty:true})&&!search.closest('[hidden],[inert]'));
 const choices=Array.from(group.querySelectorAll('[data-project-choice]'));
 choices[0].click(); check(!list.classList.contains('hidden'),'picker closed after selection');
 choices[1].click(); check(!list.classList.contains('hidden'),'picker closed after second selection');
 check(group.querySelectorAll('[data-project-remove]').length===2,'missing selection chips');
 search.value='website';search.dispatchEvent(new Event('input',{bubbles:true}));
 check(choices[0].classList.contains('hidden')&&!choices[1].classList.contains('hidden'),'search failed');
 // Escape dismisses results before navigation; outside clicks only dismiss.
 search.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 check(list.classList.contains('hidden'),'Escape did not dismiss project results');
 card.querySelector('[data-chat-input-nav="previous"]').click();
 card.querySelector('[data-chat-input-nav="next"]').click();
 check(group.querySelectorAll('[data-project-choice][aria-pressed="true"]').length===2,'previous lost picks');
 group.querySelector('[data-project-remove]').click();
 check(group.querySelectorAll('[data-project-choice][aria-pressed="true"]').length===1,'remove failed');
 search.value='';search.dispatchEvent(new Event('input',{bubbles:true}));
 choices[0].click();
 search.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true,cancelable:true}));
 check(list.classList.contains('hidden'),'results remained open before Submit');
 card.querySelector('[data-chat-input-nav="submit"]').click();
 await wait(()=>!card.isConnected);
 await fetch('/browser-result',{method:'POST',headers:{'X-Browser-Status':'pass'},body:'picker flow passed'});
 }catch(e){await fetch('/browser-result',{method:'POST',headers:{'X-Browser-Status':'fail'},body:String(e.stack||e)})}
 })()</script>`, pending.ID, pending.ID)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				if r.URL.Path == "/browser-result" {
					body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
					select {
					case result <- r.Header.Get("X-Browser-Status") + ":" + string(body):
					default:
					}
					w.WriteHeader(http.StatusNoContent)
					return
				}
				recorder := httptest.NewRecorder()
				tc.echo.ServeHTTP(recorder, r)
				for key, values := range recorder.Header() {
					for _, value := range values {
						w.Header().Add(key, value)
					}
				}
				w.WriteHeader(recorder.Code)
				body := recorder.Body.String()
				if r.URL.Path == "/chat" {
					body = strings.Replace(body, "</body>", runner+"</body>", 1)
				}
				_, _ = io.WriteString(w, body)
			}))
			defer server.Close()

			cmd := exec.Command(chrome,
				"--headless=new", "--no-sandbox", "--disable-gpu", "--disable-software-rasterizer",
				"--disable-dev-shm-usage", "--disable-extensions", "--no-first-run", "--no-default-browser-check",

				fmt.Sprintf("--window-size=%d,844", width), server.URL+"/chat?project_id="+scope,
			)
			require.NoError(t, startHandlerBrowserProcess(t, cmd))
			stopped := false
			defer func() {
				if !stopped {
					stopHandlerBrowserProcess(t, cmd)
				}
			}()
			select {
			case outcome := <-result:
				stopHandlerBrowserProcess(t, cmd)
				stopped = true
				require.True(t, strings.HasPrefix(outcome, "pass:"), outcome)
			case <-time.After(45 * time.Second):
				t.Fatal("input request browser regression timed out")
			}
			resolved := tc.handler.chatInputRequests.requests[pending.ID]
			require.NotNil(t, resolved)
			require.Len(t, resolved.Answers, 2)
			require.ElementsMatch(t, []string{project.ID, second.ID}, resolved.Answers[1].ProjectIDs)
		})
	}
}
