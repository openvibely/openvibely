package handler

import (
	"golang.org/x/net/html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestTemplateDialogsHaveConsistentCloseControls(t *testing.T) {
	templatesRoot := filepath.Join("..", "..", "web", "templates")
	dialogRe := regexp.MustCompile(`(?s)<dialog\b[^>]*id="([^"]+)"[^>]*>.*?</dialog>`)

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
			sharedSearchableSelector := strings.Contains(dialogMarkup, "data-searchable-selector-dialog")
			if sharedSearchableSelector {
				if strings.Contains(dialogMarkup, "data-searchable-selector-close") || strings.Contains(dialogMarkup, "data-searchable-selector-header") {
					t.Errorf("%s dialog %q shared searchable selector should render search and results without a header or close button", path, dialogID)
				}
				continue
			}
			closeCount := strings.Count(dialogMarkup, "ov-modal-close") + strings.Count(dialogMarkup, "@ModalCloseButton")
			if closeCount != 1 {
				t.Errorf("%s dialog %q should have exactly one modal close button, found %d", path, dialogID, closeCount)
			}
			if !strings.Contains(dialogMarkup, `aria-label="Close `) && !strings.Contains(dialogMarkup, `@ModalCloseButton`) {
				t.Errorf("%s dialog %q close button should include a specific aria-label", path, dialogID)
			}
			if strings.Contains(dialogMarkup, `title="Close`) {
				t.Errorf("%s dialog %q close button should not show a redundant tooltip", path, dialogID)
			}

			modalBoxWithClose := modalBoxContainsCloseControl(dialogMarkup)
			if !modalBoxWithClose {
				t.Errorf("%s dialog %q close button should be inside a modal-box", path, dialogID)
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
		"data-tooltip-disabled",
	} {
		if !strings.Contains(helper, expected) {
			t.Fatalf("expected shared modal close helper markup %q", expected)
		}
	}
}

// Tokenize nesting: a non-greedy div regex stops at an inner toolbar div.
func modalBoxContainsCloseControl(markup string) bool {
	z := html.NewTokenizer(strings.NewReader(markup))
	var stack []bool
	inside := func() bool {
		for _, box := range stack {
			if box {
				return true
			}
		}
		return false
	}
	for {
		switch z.Next() {
		case html.ErrorToken:
			return false
		case html.StartTagToken:
			token := z.Token()
			if token.Data == "div" {
				box := false
				for _, attr := range token.Attr {
					if attr.Key == "class" && strings.Contains(" "+attr.Val+" ", " modal-box ") {
						box = true
					}
				}
				stack = append(stack, box)
			}
			if inside() {
				for _, attr := range token.Attr {
					if attr.Key == "class" && strings.Contains(" "+attr.Val+" ", " ov-modal-close ") {
						return true
					}
				}
			}
		case html.EndTagToken:
			if z.Token().Data == "div" && len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case html.TextToken:
			if inside() && strings.Contains(string(z.Text()), "@ModalCloseButton") {
				return true
			}
		}
	}
}

func TestModalBoxCloseControlNesting(t *testing.T) {
	if !modalBoxContainsCloseControl(`<div class="modal-box"><header><div>Download</div><button class="ov-modal-close">Close</button></header></div>`) {
		t.Fatal("nested toolbar hid close control")
	}
	if modalBoxContainsCloseControl(`<div class="modal-box"><div>Content</div></div><button class="ov-modal-close">Close</button>`) {
		t.Fatal("accepted close outside modal")
	}
}
