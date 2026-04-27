package pages

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestModelsContent_NewModelVersionsInSelector(t *testing.T) {
	// Render the models page and verify the new model versions appear in the
	// HTML <option> elements and the JS modelOptionsByProvider catalog.
	agents := []models.LLMConfig{}
	var buf bytes.Buffer
	err := ModelsContent(agents, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	// HTML <option> elements
	for _, model := range []string{
		"claude-opus-4-7",
		"claude-sonnet-4-6",
	} {
		if !strings.Contains(out, `value="`+model+`"`) {
			t.Errorf("expected HTML option for %s", model)
		}
	}

	// JS modelOptionsByProvider entries
	for _, model := range []string{
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-5.4-mini",
		"gpt-5.3-codex-spark",
		"claude-opus-4-7",
		"claude-sonnet-4-6",
	} {
		if !strings.Contains(out, `'`+model+`'`) {
			t.Errorf("expected JS model option for %s", model)
		}
	}

	if strings.Contains(out, "defaultMaxTokens") {
		t.Error("expected browser catalog not to expose internal output-token defaults")
	}
	if strings.Contains(out, "Max Output Tokens / Request") || strings.Contains(out, "model_max_tokens") {
		t.Error("expected model dialog not to expose internal output-token cap")
	}
	if !strings.Contains(out, "Claude Effort") {
		t.Error("expected Claude effort label in model dialog")
	}
	if !strings.Contains(out, "Matches Claude Code effort: low, medium, high, or max") {
		t.Error("expected Claude effort behavior to be explained")
	}
	if !strings.Contains(out, "{ value: 'claude-opus-4-7', label: 'Claude Opus 4.7', efforts: ['low', 'medium', 'high', 'max']") {
		t.Error("expected Claude Opus 4.7 effort options")
	}
}

func TestModelsContent_ModelFormUsesNativePostSubmit(t *testing.T) {
	agents := []models.LLMConfig{}
	var buf bytes.Buffer
	err := ModelsContent(agents, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, `onsubmit="submitModelForm(event)"`) {
		t.Fatal("expected model form not to depend on custom submit JavaScript")
	}
	if !strings.Contains(out, `id="model_form" method="post" action="/models"`) {
		t.Fatal("expected model form to submit with native POST")
	}
	if !strings.Contains(out, "form.action = '/models/' + id;") {
		t.Fatal("expected edit flow to post to the existing model URL")
	}
	if !strings.Contains(out, "form.action = '/models';") {
		t.Fatal("expected create flow to post to /models")
	}
	if !strings.Contains(out, "form.dataset.mode = 'edit';") || !strings.Contains(out, "form.dataset.mode = 'create';") {
		t.Fatal("expected create/edit flow to track form mode")
	}
}

func TestModelsContent_ModelModalJavaScriptShape(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	for _, fn := range []string{
		"function handleModelChange()",
		"function toggleProviderFields(selectedModel, selectedReasoningEffort)",
		"function editModelFromData(button)",
		"function openNewModelModal()",
	} {
		if !strings.Contains(out, fn) {
			t.Fatalf("expected rendered script to contain %s", fn)
		}
	}

	if err := balancedJavaScriptBraces(out); err != nil {
		t.Fatal(err)
	}

	for _, broken := range []string{
		"// In \"Create\" mode, update the per-request output token cap to the model-specific default.",
		"if (typeof syncToastContainerHost === 'function') syncToastContainerHost()\t\t\t\t\tfunction",
	} {
		if strings.Contains(out, broken) {
			t.Fatalf("rendered script contains known broken modal JavaScript fragment: %q", broken)
		}
	}
}

func balancedJavaScriptBraces(value string) error {
	depth := 0
	inSingle := false
	inDouble := false
	inTemplate := false
	inLineComment := false
	inBlockComment := false
	escaped := false
	for i := 0; i < len(value); i++ {
		ch := value[i]
		var next byte
		if i+1 < len(value) {
			next = value[i+1]
		}
		if inLineComment {
			if ch == '\n' {
				inLineComment = false
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && next == '/' {
				inBlockComment = false
				i++
			}
			continue
		}
		if inSingle || inDouble || inTemplate {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if inSingle && ch == '\'' {
				inSingle = false
			}
			if inDouble && ch == '"' {
				inDouble = false
			}
			if inTemplate && ch == '`' {
				inTemplate = false
			}
			continue
		}
		if ch == '/' && next == '/' {
			inLineComment = true
			i++
			continue
		}
		if ch == '/' && next == '*' {
			inBlockComment = true
			i++
			continue
		}
		switch ch {
		case '\'':
			inSingle = true
		case '"':
			inDouble = true
		case '`':
			inTemplate = true
		case '{':
			depth++
		case '}':
			depth--
			if depth < 0 {
				return fmt.Errorf("rendered JavaScript has an unmatched closing brace near byte %d", i)
			}
		}
	}
	if depth != 0 {
		return fmt.Errorf("rendered JavaScript has %d unclosed brace(s)", depth)
	}
	return nil
}

func TestModelsContent_OAuthLinksLaunchInSystemBrowser(t *testing.T) {
	agents := []models.LLMConfig{
		{
			ID:         "openai-oauth",
			Name:       "OpenAI OAuth",
			Provider:   models.ProviderOpenAI,
			AuthMethod: models.AuthMethodOAuth,
			Model:      "gpt-5.4",
		},
	}

	var buf bytes.Buffer
	err := ModelsContent(agents, nil).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "return launchOAuthInSystemBrowser(this.dataset.oauthPath)") {
		t.Fatal("expected OAuth links to launch through system-browser helper")
	}
	if !strings.Contains(out, "data-oauth-path=\"/models/openai-oauth/oauth/initiate\"") {
		t.Fatal("expected OAuth links to expose model-specific oauth path via data attribute")
	}
	if !strings.Contains(out, "external=1") {
		t.Fatal("expected system-browser helper to request backend external launch mode")
	}
	if !strings.Contains(out, "fetch(externalURL") {
		t.Fatal("expected OAuth launcher to call backend in background via fetch")
	}
	if strings.Contains(out, "window.location.href = externalURL") {
		t.Fatal("expected OAuth launcher to avoid page navigation")
	}
}
