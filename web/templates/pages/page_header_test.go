package pages

import (
	"os"
	"strings"
	"testing"
)

func TestPageHeadersUseCompactShellStyle(t *testing.T) {
	for _, page := range []string{"skills", "alerts", "tasks", "insights", "worker_settings", "task_new", "execution_detail", "app_settings", "history", "schedule", "agents", "models", "chat", "analytics", "upcoming", "automations", "settings"} {
		t.Run(page, func(t *testing.T) {
			source, err := os.ReadFile(page + ".templ")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(source), "data-page-header") {
				t.Fatal("page missing compact header marker")
			}
		})
	}
	source, err := os.ReadFile("../layout/base.templ")
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{"#main-content:has([data-page-header]) { padding-top: 0; }", "min-height: 48px; padding-top: 8px; padding-bottom: 8px; margin-bottom: 8px", "font-size: 16px; line-height: 22px; font-weight: 500;", "[data-page-header] [data-page-header] { min-height: 0; padding: 0; margin: 0; }"} {
		if !strings.Contains(string(source), rule) {
			t.Errorf("missing shared compact header rule: %s", rule)
		}
	}
}
