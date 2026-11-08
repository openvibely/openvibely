package handler

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/events"
	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/web/static"
	"github.com/openvibely/openvibely/web/templates/components"
	"github.com/openvibely/openvibely/web/templates/layout"
	"github.com/openvibely/openvibely/web/templates/pages"
)

//go:embed testdata/native_panel_scroll.js
var nativePanelScrollScenario string

// Uses the existing Wails host: WKWebView on macOS, WebKitGTK on Linux,
// WebView2 on Windows. Real templates, toast handlers and navigation execute
// against isolated HTTP fixtures; no user server or account is contacted.
func TestNativePanelScrollIsolationInWails(t *testing.T) {
	if testing.Short() || os.Getenv("OPENVIBELY_RUN_NATIVE_UI") != "1" {
		t.Skip("requires OPENVIBELY_RUN_NATIVE_UI=1 and a graphical session")
	}
	host := buildNativeChatHost(t)
	t.Setenv("OPENVIBELY_NATIVE_TEST_HOST", host)
	projects := []models.Project{{ID: "project", Name: "Project"}, {ID: "other", Name: "Other"}}
	task := &models.Task{ID: "audit", ProjectID: "project", Title: "Approval scrolling", Status: models.StatusCompleted, Category: models.CategoryCompleted}
	executions := []models.Execution{{ID: "exec", TaskID: task.ID, Status: models.ExecCompleted, PromptSent: "Check dependencies", Output: strings.Repeat("Response paragraph.\n\n", 60), StartedAt: time.Unix(1, 0)}}
	results := make(chan string, 1)
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if static.ServeAsset(w, r) {
			return
		}
		ctx := layout.WithDesktopMode(r.Context(), true)
		w.Header().Set("Content-Type", "text/html")
		switch r.URL.Path {
		case "/native-test-bridge":
			w.WriteHeader(204)
		case "/browser-stage":
			t.Log(r.URL.Query().Get("name"))
			select {
			case started <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
		case "/browser-result":
			select {
			case results <- r.URL.Query().Get("status"):
			default:
			}
			w.WriteHeader(204)
		case "/fixture":
			var out bytes.Buffer
			_ = layout.Base("Native scrolling", projects, "project").Render(ctx, &out)
			fmt.Fprint(w, strings.Replace(out.String(), "</body>", "<script>"+nativePanelScrollScenario+"</script></body>", 1))
		case "/analytics":
			_ = pages.AnalyticsContent(&projects[0]).Render(ctx, w)
		case "/api/analytics/usage":
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("projection") == "accounts" {
				fmt.Fprint(w, `{"accounts":[{"key":"first","provider":"openai"},{"key":"second","provider":"openai"},{"key":"anthropic","provider":"anthropic"}]}`)
			} else {
				fmt.Fprint(w, `{"account_limits":[],"totals":{},"usage_rate":[],"usage_rate_by_model":[],"model_breakdown":[]}`)
			}
		case "/api/analytics/dashboard":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"current":{},"definitions":[],"outcome_trend":[],"funnel":[],"agents":[],"workflows":[],"recent_outcomes":[]}`)
		case "/tasks/audit":
			thread := components.TaskThreadView(task, executions, nil, nil, nil, nil, false, 30)
			_ = pages.TaskDetailContent(task, nil, nil, nil, nil, nil, nil, "chat", nil, thread).Render(ctx, w)
		case "/chat/input-requests":
			raw, _ := json.Marshal(map[string]any{"identity": map[string]any{"skill": "sample"}, "platform": "test", "dependencies": []any{map[string]any{"name": "example", "version": "1.2.3"}}})
			_ = pages.ChatInputRequestCard(&events.ChatInputRequestEvent{ID: "request", ExecID: "exec", ProjectID: "project", Installation: raw, Questions: []events.ChatInputRequestQuestion{{ID: "install", Question: "Install dependencies?", Options: []events.ChatInputRequestOption{{Label: "Install dependencies"}, {Label: "Not now"}}}}}).Render(ctx, w)
		case "/tasks/sidebar":
			var tasks []models.Task
			for _, category := range []models.TaskCategory{models.CategoryActive, models.CategoryCompleted, models.CategoryBacklog, models.CategoryScheduled} {
				for i := 0; i < 25; i++ {
					tasks = append(tasks, models.Task{ID: fmt.Sprintf("%s-%d", category, i), ProjectID: "project", Title: fmt.Sprintf("Task %d", i), Category: category, Status: models.StatusCompleted})
				}
			}
			_ = layout.TaskListPanelContent("project", layout.TaskListGroups(tasks)).Render(ctx, w)
		case "/tasks/scheduled-24":
			fmt.Fprint(w, `<div id="task-detail-content" data-task-id="scheduled-24">Selected task</div>`)
		case "/chat":
			fmt.Fprint(w, `<div id="other-project">Other project</div>`)
		default:
			if strings.HasPrefix(r.URL.Path, "/api/analytics/") {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `[]`)
			} else {
				w.WriteHeader(204)
			}
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, host, server.URL+"/fixture?project_id=project")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := startHandlerBrowserProcess(t, cmd); err != nil {
		t.Fatal(err)
	}
	stopped := false
	defer func() {
		if !stopped {
			stopHandlerBrowserProcess(t, cmd)
		}
	}()
	var result string
	select {
	case result = <-results:
	case <-started:
		timer := time.NewTimer(45 * time.Second)
		defer timer.Stop()
		select {
		case result = <-results:
		case <-timer.C:
			result = "scenario timeout"
		case <-ctx.Done():
			result = "host deadline exceeded"
		}
	case <-ctx.Done():
		result = "native host startup timeout"
	}
	// Stop before reading the output buffer; the host may still be writing logs.
	stopHandlerBrowserProcess(t, cmd)
	stopped = true
	if result != "pass" {
		t.Fatalf("%s\nNative host output:\n%s", result, output.String())
	}
}
