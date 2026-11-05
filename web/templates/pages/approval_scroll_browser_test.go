package pages

import (
	"encoding/json"
	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBrowserFunctional_ApprovalLinkKeepsTitlebarVisible(t *testing.T) {
	chrome := chatNavigationChromePath(t)
	for _, desktop := range []bool{true, false} {
		name := "web"
		if desktop {
			name = "desktop"
		}
		t.Run(name, func(t *testing.T) {
			task := &models.Task{ID: "audit", ProjectID: "project", Title: "Approval scrolling", Status: models.StatusCompleted, Category: models.CategoryCompleted}
			executions := []models.Execution{{ID: "exec", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "Check dependencies", Output: strings.Repeat("Response paragraph.\n\n", 60), StartedAt: time.Unix(1, 0)}}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if static.ServeAsset(w, r) {
					return
				}
				w.Header().Set("Content-Type", "text/html")
				switch r.URL.Path {
				case "/chat/input-requests":
					raw, _ := json.Marshal(map[string]any{"identity": map[string]any{"skill": "sample"}, "platform": "linux/amd64", "dependencies": []any{map[string]any{"name": "example", "version": "1.2.3"}}})
					_ = ChatInputRequestCard(&events.ChatInputRequestEvent{ID: "request", ExecID: "exec", ProjectID: "project", Installation: raw, Questions: []events.ChatInputRequestQuestion{{ID: "install", Question: "Install dependencies?", Options: []events.ChatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}}).Render(r.Context(), w)
				case "/tasks/audit":
					ctx := layout.WithDesktopMode(r.Context(), desktop)
					thread := components.TaskThreadView(task, executions, nil, nil, nil, nil, false, 30)
					_ = TaskDetailPage([]models.Project{{ID: "project", Name: "Project"}}, task, nil, nil, nil, nil, nil, nil, "chat", nil, thread).Render(ctx, w)
				default:
					w.WriteHeader(http.StatusNoContent)
				}
			}))
			defer server.Close()
			runComposerFocusCDP(t, chrome, server.URL+"/tasks/audit?project_id=project&approval_execution_id=exec#chat-input-request-request", "approval-scroll", func(b *composerFocusCDP) {
				b.call("Emulation.setDeviceMetricsOverride", map[string]any{"width": 1440, "height": 900, "deviceScaleFactor": 1, "mobile": false}, nil)
				for attempt := 0; attempt < 2; attempt++ {
					if attempt > 0 {
						b.reload()
					}
					b.waitFor("approval card", `String(!!document.getElementById('chat-input-request-request'))`, "true")
					result := b.evaluateAwait(`(async()=>{
 await new Promise(r=>setTimeout(r,800));
 const card=document.getElementById('chat-input-request-request'),panel=document.getElementById('task-thread-messages');
 const box=card.getBoundingClientRect(),view=panel.getBoundingClientRect();
 if(document.getElementById('desktop-project-titlebar').getBoundingClientRect().top!==0||document.body.scrollTop||document.documentElement.scrollTop) return 'titlebar displaced';
 if(panel.scrollTop<=0||box.bottom<=view.top||box.top>=view.bottom)return 'approval not visible';
 return 'pass';
 })()`)
					if result != "pass" {
						t.Fatal(result)
					}
				}
			})
		})
	}
}
