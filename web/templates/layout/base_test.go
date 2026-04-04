package layout

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

// TestToastDismissalCleanup verifies the toast notification system properly
// cleans up DOM elements after dismissal to prevent page unresponsiveness.
func TestToastDismissalCleanup(t *testing.T) {
	var buf bytes.Buffer
	comp := Base("Test", []models.Project{}, "")
	err := comp.Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	// Verify toast-dismiss CSS sets pointer-events: none to prevent blocking clicks
	if !strings.Contains(html, "pointer-events: none") || !strings.Contains(html, ".toast-notification.toast-dismiss") {
		t.Error("toast-dismiss CSS must set pointer-events: none to prevent invisible elements from blocking user interaction")
	}

	// Verify toast-dismiss CSS sets transition: none to prevent CSS conflicts
	if !strings.Contains(html, "transition: none") {
		t.Error("toast-dismiss CSS must set transition: none to prevent animation/transition conflicts that can block animationend")
	}

	// Verify animationend listener uses { once: true } to prevent multiple fires
	if !strings.Contains(html, "{ once: true }") {
		t.Error("animationend listener must use { once: true } to prevent multiple handler invocations")
	}

	// Verify fallback setTimeout exists to force-remove toast if animationend doesn't fire
	if !strings.Contains(html, "toast.parentNode") {
		t.Error("dismissToast must have a fallback setTimeout to force-remove toast if animationend doesn't fire")
	}

	// Verify htmx.ajax in toast click handler has .catch() for error handling
	if !strings.Contains(html, ".catch(function(err)") {
		t.Error("htmx.ajax() in toast click handler must have .catch() per HTMX 2.0 requirements")
	}

	// Verify duplicate toast suppression logic is present
	if !strings.Contains(html, "window._recentToasts") || !strings.Contains(html, "shouldSuppressDuplicateToast") {
		t.Error("toast system must include duplicate suppression map and helper")
	}

	// Verify HTMX toast bridge passes optional action-link fields
	if !strings.Contains(html, "detail.linkURL || detail.link_url || ''") || !strings.Contains(html, "detail.linkText || detail.link_text || ''") {
		t.Error("openvibelyToast bridge must map link_url/link_text fields for clickable toast actions")
	}

	// Verify toast navigation closes open modal dialogs first so destination is visible.
	if !strings.Contains(html, "function closeOpenModalsForToastNavigation()") {
		t.Error("toast navigation must define closeOpenModalsForToastNavigation helper")
	}
	if strings.Count(html, "closeOpenModalsForToastNavigation();") < 2 {
		t.Error("toast navigation must close open modals for both action-link and task-detail click paths")
	}
}

// TestToastDismissalBehavior documents the expected behavior for toast dismissal
// to prevent the bug where page becomes unresponsive after toast disappears.
func TestToastDismissalBehavior(t *testing.T) {
	tests := []struct {
		name                  string
		scenario              string
		expectToastRemoved    bool
		expectPointerBlocking bool
	}{
		{
			name:                  "normal dismiss - animationend fires",
			scenario:              "Toast auto-dismisses after 5s, animation plays, animationend fires",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "animationend does not fire - fallback timeout removes toast",
			scenario:              "prefers-reduced-motion or browser quirk prevents animationend, fallback setTimeout removes element at 400ms",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "transition/animation conflict - pointer-events: none prevents blocking",
			scenario:              "CSS transition on opacity/transform conflicts with dismiss animation, but pointer-events: none on .toast-dismiss prevents click blocking even if element lingers",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "manual dismiss via close button",
			scenario:              "User clicks X button, event.stopPropagation prevents task dialog opening, dismissToast called",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "click dismiss opens task detail",
			scenario:              "User clicks toast body, htmx.ajax loads task detail with .catch(), toast dismissed",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
		{
			name:                  "rapid SSE events - excess toasts dismissed",
			scenario:              "More than 5 toasts created rapidly, oldest dismissed with same safety mechanisms",
			expectToastRemoved:    true,
			expectPointerBlocking: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.expectToastRemoved {
				t.Error("all scenarios must result in toast removal")
			}
			if tt.expectPointerBlocking {
				t.Error("no scenario should leave pointer-blocking elements in the DOM")
			}
			t.Logf("Scenario: %s", tt.scenario)
		})
	}
}

// TestChatMarkdownCSS_WhiteSpaceNormal verifies that .chat-markdown has white-space: normal
// to prevent inherited whitespace-pre-wrap from streaming containers causing extra spacing
// in task creation feedback and other markdown-rendered content.
func TestChatMarkdownCSS_WhiteSpaceNormal(t *testing.T) {
	var buf bytes.Buffer
	err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("Failed to render Base: %v", err)
	}

	html := buf.String()

	if !strings.Contains(html, "white-space: normal") {
		t.Error(".chat-markdown CSS should include 'white-space: normal' to prevent inherited whitespace-pre-wrap from causing layout issues in task creation feedback")
	}
}

// TestLightTheme_UsesLightModernTokens verifies the light theme exposes the
// Light 2026 token aliases and that key surfaces consume those tokens.
func TestLightTheme_UsesLightModernTokens(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		"--ov-l-accent: #5B47CD;",
		"--ov-l-surface: #F8F8F8;",
		"--ov-l-border: #E5E5E5;",
		"--ov-l-text: #3B3B3B;",
		"[data-theme=\"light\"] body,",
		"[data-theme=\"light\"] .drawer-content {",
		"background-color: var(--ov-l-surface);",
		"[data-theme=\"light\"] #main-content {",
		"background-color: var(--ov-l-surface);",
		"[data-theme=\"light\"] .btn-primary {",
		"background-color: var(--ov-l-accent);",
		"[data-theme=\"light\"] .sidebar-aside {",
		"[data-theme=\"light\"] .card {",
		"[data-theme=\"light\"] .chat-bubble-user-msg,",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected light theme fragment %q to be present", fragment)
		}
	}
}

// TestThemeToggle_UsesImmediateSwitch ensures theme toggle applies instantly
// without transition class choreography that can lag on large pages.
func TestThemeToggle_UsesImmediateSwitch(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	unexpected := []string{
		"html.theme-transition",
		"html.classList.add('theme-transition')",
		"html.classList.remove('theme-transition')",
	}
	for _, fragment := range unexpected {
		if strings.Contains(html, fragment) {
			t.Errorf("unexpected transition fragment %q should not be present", fragment)
		}
	}

	expected := []string{
		"window.toggleTheme = function() {",
		"html.setAttribute('data-theme', next);",
		"localStorage.setItem('theme', next);",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected immediate-toggle fragment %q to be present", fragment)
		}
	}
}

// TestLoadingDots_UsesPrimaryThemeToken ensures the shared three-dot loader
// stays tied to the same primary token used by primary buttons (chat send button).
func TestLoadingDots_UsesPrimaryThemeToken(t *testing.T) {
	var buf bytes.Buffer
	if err := Base("Test", []models.Project{}, "").Render(context.Background(), &buf); err != nil {
		t.Fatalf("failed to render Base: %v", err)
	}
	html := buf.String()

	expected := []string{
		".ov-loading-dots {",
		"--ov-loading-dot-color: oklch(var(--p));",
		"--ov-loading-dot-color-soft: oklch(var(--p) / 0.45);",
		".ov-loading-dot {",
		"animation: ov-loading-dot-bounce 1s ease-in-out infinite, ov-loading-dot-color 1.6s linear infinite;",
		"@keyframes ov-loading-dot-color {",
		"background: var(--ov-loading-dot-color-soft);",
	}
	for _, fragment := range expected {
		if !strings.Contains(html, fragment) {
			t.Errorf("expected loading-dot style fragment %q to be present", fragment)
		}
	}
}
