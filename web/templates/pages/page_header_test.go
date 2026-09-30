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
	for _, rule := range []string{"#main-content:has([data-page-header]) { padding-top: 0; }", "min-height: 48px; padding-top: 8px; padding-bottom: 8px; margin-bottom: 0", "#main-content [data-page-header] h2 { display: flex; align-items: center; min-height: 32px; }", "#main-content h2[data-page-header] { display: flex; }", "font-size: 16px; line-height: 22px; font-weight: 500;", "[data-page-header] [data-page-header] { min-height: 0; padding: 0; margin: 0; }"} {
		if !strings.Contains(string(source), rule) {
			t.Errorf("missing shared compact header rule: %s", rule)
		}
	}
}

func TestAutomationEditorActionsShareCompactHeaderRow(t *testing.T) {
	source, err := os.ReadFile("automations.templ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `<div class="flex flex-wrap items-center justify-between gap-3" data-page-header>`) {
		t.Fatal("automation editor breadcrumb and actions must share the centered compact header row")
	}
}

func TestNewTaskBreadcrumbUsesSharedTypography(t *testing.T) {
	source, err := os.ReadFile("task_new.templ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), `aria-label="Breadcrumb" data-page-header>`) {
		t.Fatal("nested new-task breadcrumb must apply shared typography to its direct links, separator, and title input")
	}
}

func TestPageHeaderSubtitlesRemoved(t *testing.T) {
	for _, page := range []string{"chat", "history", "upcoming", "analytics"} {
		source, err := os.ReadFile(page + ".templ")
		if err != nil {
			t.Fatal(err)
		}
		start := strings.Index(string(source), "data-page-header")
		header := string(source)[start:]
		end := strings.Index(header, "</h2>")
		afterTitle := strings.TrimSpace(header[end+len("</h2>"):])
		if strings.HasPrefix(afterTitle, "<p") {
			t.Errorf("%s still renders a page subtitle", page)
		}
	}
	source, err := os.ReadFile("../layout/base.templ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(source), "[data-page-header]:has(> div > h2) { align-items: flex-start;") {
		t.Fatal("header controls must remain vertically centered")
	}
}
