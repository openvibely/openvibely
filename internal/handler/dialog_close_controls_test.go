package handler

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTemplateDialogsHaveConsistentCloseControls(t *testing.T) {
	templatesRoot := filepath.Join("..", "..", "web", "templates")
	dialogRe := regexp.MustCompile(`(?s)<dialog\b[^>]*id="([^"]+)"[^>]*>.*?</dialog>`)
	modalBoxRe := regexp.MustCompile(`(?s)<div\b[^>]*class="[^"]*\bmodal-box\b[^"]*"[^>]*>.*?</div>`)

	var dialogCount int
	err := filepath.WalkDir(templatesRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".templ") {
			return nil
		}

		contentBytes, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(contentBytes)
		for _, match := range dialogRe.FindAllStringSubmatch(content, -1) {
			dialogCount++
			dialogID := match[1]
			dialogMarkup := match[0]
			sharedProjectSelector := strings.Contains(dialogMarkup, "data-project-selector-dialog")
			closeCount := strings.Count(dialogMarkup, "ov-modal-close") + strings.Count(dialogMarkup, "@ModalCloseButton")
			if sharedProjectSelector {
				closeCount += strings.Count(dialogMarkup, "data-project-selector-close")
			}
			if closeCount != 1 {
				t.Errorf("%s dialog %q should have exactly one modal close button, found %d", path, dialogID, closeCount)
			}
			if !strings.Contains(dialogMarkup, `aria-label="Close `) && !strings.Contains(dialogMarkup, `@ModalCloseButton`) {
				t.Errorf("%s dialog %q close button should include a specific aria-label", path, dialogID)
			}
			if !strings.Contains(dialogMarkup, `title="Close`) && !strings.Contains(dialogMarkup, `@ModalCloseButton`) {
				t.Errorf("%s dialog %q close button should include a Close title", path, dialogID)
			}

			modalBoxWithClose := false
			for _, modalBox := range modalBoxRe.FindAllString(dialogMarkup, -1) {
				if strings.Contains(modalBox, "ov-modal-close") || strings.Contains(modalBox, "@ModalCloseButton") {
					modalBoxWithClose = true
					break
				}
			}
			sharedSelectorWithClose := sharedProjectSelector &&
				strings.Contains(dialogMarkup, `class={ templateui.SearchableSelectorPanelClass }`) &&
				strings.Contains(dialogMarkup, "data-project-selector-close")
			if !modalBoxWithClose && !sharedSelectorWithClose {
				t.Errorf("%s dialog %q close button should be inside a modal-box or direct shared selector panel", path, dialogID)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk templates: %v", err)
	}
	if dialogCount == 0 {
		t.Fatal("expected to find template dialogs")
	}

	baseBytes, err := os.ReadFile(filepath.Join(templatesRoot, "layout", "base.templ"))
	if err != nil {
		t.Fatalf("read base template: %v", err)
	}
	base := string(baseBytes)
	for _, expected := range []string{
		".ov-modal-close {",
		".ov-modal-close:focus-visible",
		`outline: 2px solid currentColor;`,
		`[data-theme="light"] .ov-modal-close`,
		`[data-theme="light"] .ov-modal-close:hover`,
	} {
		if !strings.Contains(base, expected) {
			t.Fatalf("expected shared modal close style %q", expected)
		}
	}

	helperBytes, err := os.ReadFile(filepath.Join(templatesRoot, "pages", "modal_close.templ"))
	if err != nil {
		t.Fatalf("read modal close helper: %v", err)
	}
	helper := string(helperBytes)
	for _, expected := range []string{
		"ov-modal-close",
		"aria-label={ label }",
		"title={ label }",
	} {
		if !strings.Contains(helper, expected) {
			t.Fatalf("expected shared modal close helper markup %q", expected)
		}
	}
}
