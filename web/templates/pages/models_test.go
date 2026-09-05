package pages

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestCardPaginationCompletionIsSilent(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContentPageWithPagination(nil, nil, nil, false, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models pagination status: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "End of list") || strings.Contains(out, "data-card-pagination-end") {
		t.Fatalf("pagination completion should be silent, got visible end marker")
	}
	for _, required := range []string{"data-card-pagination-loading", "data-card-pagination-error", "data-card-pagination-retry", "data-card-pagination-sentinel"} {
		if !strings.Contains(out, required) {
			t.Errorf("expected pagination status to retain %s", required)
		}
	}
}

func TestModelsContent_NewModelVersionsInSelector(t *testing.T) {
	// Render the models page and verify the new model versions appear in the
	// HTML <option> elements and the JS modelOptionsByProvider catalog.
	agents := []models.LLMConfig{}
	var buf bytes.Buffer
	err := ModelsContent(agents, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	// HTML <option> elements
	for _, model := range []string{
		"claude-sonnet-5",
		"claude-opus-5",
		"claude-fable-5-1",
		"claude-mythos-5-1",
		"claude-fable-5",
		"claude-mythos-5",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-sonnet-4-6",
	} {
		if !strings.Contains(out, `value="`+model+`"`) {
			t.Errorf("expected HTML option for %s", model)
		}
	}

	// JS modelOptionsByProvider entries
	for _, model := range []string{
		"gpt-6-astra",
		"gpt-5.6-sol",
		"gpt-5.6-terra",
		"gpt-5.6-luna",
		"gpt-5.5",
		"gpt-5.5-pro",
		"gpt-5.4-mini",
		"gpt-5.3-codex-spark",
		"claude-sonnet-5",
		"claude-opus-5",
		"claude-fable-5-1",
		"claude-mythos-5-1",
		"claude-fable-5",
		"claude-mythos-5",
		"claude-opus-4-8",
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
	if !strings.Contains(out, "0 = use global pool; positive values set a per-model cap with no product-level maximum") {
		t.Error("expected model worker limit guidance to describe inherited and positive per-model limits")
	}
	modelWorkerInputStart := strings.Index(out, `id="model_max_workers"`)
	if modelWorkerInputStart < 0 {
		t.Fatal("expected model worker input")
	}
	modelWorkerInputEnd := strings.Index(out[modelWorkerInputStart:], ">")
	if modelWorkerInputEnd < 0 {
		t.Fatal("expected model worker input to be well-formed")
	}
	if strings.Contains(out[modelWorkerInputStart:modelWorkerInputStart+modelWorkerInputEnd], `max="10"`) {
		t.Error("expected model worker input not to retain a hard maximum of 10")
	}
	if !strings.Contains(out, "Save endpoint changes and reconnect before discovering models.") {
		t.Error("expected edit-mode discovery to explain that endpoint changes must be saved first")
	}
	if !strings.Contains(out, `name="custom_access_token_header"`) ||
		!strings.Contains(out, `name="custom_access_token_prefix"`) ||
		!strings.Contains(out, `<option value="raw">Raw token</option>`) {
		t.Error("expected custom OAuth token header, prefix, and raw-token controls")
	}
	for _, control := range []string{
		`name="auth_header_name"`,
		`name="auth_header_value_prefix"`,
		`name="extra_headers_json"`,
		`name="extra_body_json"`,
		`name="models_url"`,
	} {
		if !strings.Contains(out, control) {
			t.Errorf("expected custom API-key request control %s", control)
		}
	}
	if !strings.Contains(out, "if (!showCustom) methodInput.value = 'api_key';") {
		t.Error("expected provider changes to reset the hidden custom OAuth selector")
	}
	if !strings.Contains(out, "apiKeyField.classList.toggle('hidden', showCustom && method === 'oauth');") {
		t.Error("expected switching from OAuth to API key to restore the API-key field")
	}
	if !strings.Contains(out, "el.disabled = !showCustom || method !== 'oauth';") {
		t.Error("expected hidden OAuth controls to be disabled outside custom OAuth mode")
	}
	if !strings.Contains(out, "Claude Effort") {
		t.Error("expected Claude effort label in model dialog")
	}
	if !strings.Contains(out, "Matches Claude Code effort: low, medium, high, xhigh, or max. Availability varies by model.") {
		t.Error("expected Claude effort behavior to be explained")
	}
	if !strings.Contains(out, "{ value: 'claude-sonnet-5', label: 'Claude Sonnet 5', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Sonnet 5 effort options")
	}
	if !strings.Contains(out, "{ value: 'claude-opus-5', label: 'Claude Opus 5', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Opus 5 effort options")
	}
	if !strings.Contains(out, "{ value: 'claude-fable-5-1', label: 'Claude Fable 5.1', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Fable 5.1 effort options")
	}
	if !strings.Contains(out, "{ value: 'gpt-5.6-sol', label: 'gpt-5.6-sol', efforts: ['none', 'low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected GPT-5.6 Sol effort options")
	}
	if !strings.Contains(out, "{ value: 'gpt-6-astra', label: 'gpt-6-astra', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected GPT-6 Astra effort options without unsupported none")
	}
	for _, model := range []string{"kimi-k3", "kimi-k2.7-code", "kimi-k2.7-code-highspeed", "kimi-k2.6", "kimi-k2.5"} {
		if !strings.Contains(out, "{ value: '"+model+"'") {
			t.Errorf("expected current Moonshot model %q in selector", model)
		}
	}
	if !strings.Contains(out, "{ value: 'kimi-k3', label: 'Kimi K3', efforts: ['low', 'high', 'max']") {
		t.Error("expected Kimi K3 reasoning effort options")
	}
	if !strings.Contains(out, "Kimi Reasoning Effort") {
		t.Error("expected Kimi reasoning effort label")
	}
	if strings.Contains(out, "{ value: 'kimi-k2-0711-preview'") {
		t.Error("did not expect discontinued Kimi K2 preview in selector")
	}
	for _, model := range []string{"glm-5.2", "glm-5.1", "glm-5-turbo", "glm-5", "glm-4.7", "glm-4.7-flashx", "glm-4.7-flash", "glm-4.6"} {
		if !strings.Contains(out, "{ value: '"+model+"'") {
			t.Errorf("expected current Z.AI model %q in selector", model)
		}
	}
	if !strings.Contains(out, "{ value: 'glm-5.2', label: 'GLM 5.2', efforts: ['none', 'minimal', 'low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected GLM 5.2 reasoning effort options")
	}
	if !strings.Contains(out, "GLM Reasoning Effort") {
		t.Error("expected GLM reasoning effort label")
	}
	if !strings.Contains(out, "{ value: 'claude-fable-5', label: 'Claude Fable 5', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Fable 5 effort options")
	}
	if !strings.Contains(out, "{ value: 'claude-mythos-5-1', label: 'Claude Mythos 5.1', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Mythos 5.1 effort options")
	}
	if !strings.Contains(out, "{ value: 'claude-mythos-5', label: 'Claude Mythos 5', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Mythos 5 effort options")
	}
	if !strings.Contains(out, "{ value: 'claude-opus-4-7', label: 'Claude Opus 4.7', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Opus 4.7 effort options")
	}
	if !strings.Contains(out, "{ value: 'claude-opus-4-8', label: 'Claude Opus 4.8', efforts: ['low', 'medium', 'high', 'xhigh', 'max']") {
		t.Error("expected Claude Opus 4.8 effort options")
	}
}

func TestModelsContent_AnthropicDefaultModelSelection(t *testing.T) {
	// Render the models page and verify:
	// 1. The first Anthropic HTML <option> and JS catalog entry is a stable Sonnet model.
	// 2. Claude Fable 5.1, Fable 5, and Claude Mythos 5 remain selectable.
	// 3. None of those models is the first option (i.e., auto-selected).
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	// The first <option> in the model select must be the newest stable Sonnet model.
	// We detect this by confirming claude-sonnet-5 appears before the higher-risk
	// Fable/Mythos entries and older Sonnet entries.
	sonnet5Idx := strings.Index(out, `value="claude-sonnet-5"`)
	sonnet46Idx := strings.Index(out, `value="claude-sonnet-4-6"`)
	fable51Idx := strings.Index(out, `value="claude-fable-5-1"`)
	mythos51Idx := strings.Index(out, `value="claude-mythos-5-1"`)
	fableIdx := strings.Index(out, `value="claude-fable-5"`)
	mythosIdx := strings.Index(out, `value="claude-mythos-5"`)
	if sonnet5Idx < 0 {
		t.Fatal("expected claude-sonnet-5 to be present as an HTML option")
	}
	if sonnet46Idx < 0 {
		t.Fatal("expected claude-sonnet-4-6 to be present as an HTML option")
	}
	if fable51Idx < 0 {
		t.Fatal("expected claude-fable-5-1 to be present as an HTML option")
	}
	if mythos51Idx < 0 {
		t.Fatal("expected claude-mythos-5-1 to be present as an HTML option")
	}
	if fableIdx < 0 {
		t.Fatal("expected claude-fable-5 to be present as an HTML option")
	}
	if mythosIdx < 0 {
		t.Fatal("expected claude-mythos-5 to be present as an HTML option")
	}
	if sonnet5Idx > sonnet46Idx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-sonnet-4-6 in the HTML selector")
	}
	if sonnet5Idx > fable51Idx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-fable-5-1 in the HTML selector (fable-5.1 must not be the default)")
	}
	if sonnet5Idx > mythos51Idx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-mythos-5-1 in the HTML selector (mythos-5.1 must not be the default)")
	}
	if sonnet5Idx > fableIdx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-fable-5 in the HTML selector (fable-5 must not be the default)")
	}
	if sonnet5Idx > mythosIdx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-mythos-5 in the HTML selector (mythos-5 must not be the default)")
	}

	// The JS anthropic catalog must also list claude-sonnet-5 before fable/mythos and older Sonnet entries.
	jsAnthropicIdx := strings.Index(out, "anthropic: [")
	if jsAnthropicIdx < 0 {
		t.Fatal("expected JS anthropic catalog block to be present")
	}
	// Bound the slice to just the anthropic block (ends before the next provider key).
	jsCatalog := out[jsAnthropicIdx:]
	if nextProvider := strings.Index(jsCatalog, "openai: ["); nextProvider > 0 {
		jsCatalog = jsCatalog[:nextProvider]
	}
	jsSonnet5Idx := strings.Index(jsCatalog, "'claude-sonnet-5'")
	jsSonnet46Idx := strings.Index(jsCatalog, "'claude-sonnet-4-6'")
	jsFable51Idx := strings.Index(jsCatalog, "'claude-fable-5-1'")
	jsMythos51Idx := strings.Index(jsCatalog, "'claude-mythos-5-1'")
	jsFableIdx := strings.Index(jsCatalog, "'claude-fable-5'")
	jsMythosIdx := strings.Index(jsCatalog, "'claude-mythos-5'")
	if jsSonnet5Idx < 0 {
		t.Fatal("expected claude-sonnet-5 in JS anthropic catalog")
	}
	if jsSonnet46Idx < 0 {
		t.Fatal("expected claude-sonnet-4-6 in JS anthropic catalog")
	}
	if jsFable51Idx < 0 {
		t.Fatal("expected claude-fable-5-1 in JS anthropic catalog")
	}
	if jsMythos51Idx < 0 {
		t.Fatal("expected claude-mythos-5-1 in JS anthropic catalog")
	}
	if jsFableIdx < 0 {
		t.Fatal("expected claude-fable-5 in JS anthropic catalog")
	}
	if jsMythosIdx < 0 {
		t.Fatal("expected claude-mythos-5 in JS anthropic catalog")
	}
	if jsSonnet5Idx > jsSonnet46Idx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-sonnet-4-6 in JS anthropic catalog")
	}
	if jsSonnet5Idx > jsFable51Idx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-fable-5-1 in JS anthropic catalog (fable-5.1 must not be the first/default entry)")
	}
	if jsSonnet5Idx > jsMythos51Idx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-mythos-5-1 in JS anthropic catalog (mythos-5.1 must not be the first/default entry)")
	}
	if jsSonnet5Idx > jsFableIdx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-fable-5 in JS anthropic catalog (fable-5 must not be the first/default entry)")
	}
	if jsSonnet5Idx > jsMythosIdx {
		t.Errorf("expected claude-sonnet-5 to appear before claude-mythos-5 in JS anthropic catalog (mythos-5 must not be the first/default entry)")
	}
}

func TestModelsContent_ModelFormUsesHTMXSubmit(t *testing.T) {
	agents := []models.LLMConfig{}
	var buf bytes.Buffer
	err := ModelsContent(agents, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, `onsubmit="submitModelForm(event)"`) {
		t.Fatal("expected model form not to depend on custom submit JavaScript")
	}
	// The form has a native fallback action and method plus HTMX attributes.
	if !strings.Contains(out, `id="model_form" method="post" action="/models"`) {
		t.Fatal("expected model form to retain native POST fallback")
	}
	// HTMX attributes enable in-place swap so the URL (and project_id param) is preserved.
	if !strings.Contains(out, `hx-post="/models"`) {
		t.Fatal("expected model form to have static hx-post attribute for HTMX submission")
	}
	if !strings.Contains(out, `hx-target="#models-container"`) {
		t.Fatal("expected model form to have hx-target pointing at models-container")
	}
	if !strings.Contains(out, `hx-swap="outerHTML"`) {
		t.Fatal("expected model form to have hx-swap outerHTML")
	}
	if !strings.Contains(out, `id="model_config_id" name="model_config_id" value=""`) {
		t.Fatal("expected model form to include hidden model config ID")
	}
	if !strings.Contains(out, `id="model_form_error"`) ||
		!strings.Contains(out, `aria-live="assertive"`) {
		t.Fatal("expected model form to include an accessible save-error banner")
	}
	if !strings.Contains(out, `showModelFormError(modelSaveErrorMessage(event.detail.xhr));`) {
		t.Fatal("expected model save failures to display in the model form")
	}
	if !strings.Contains(out, `payload.message || payload.error || fallback`) {
		t.Fatal("expected model save error responses to be parsed for a useful message")
	}
	if !strings.Contains(out, `addEventListener('invalid'`) ||
		!strings.Contains(out, `Complete the required field:`) {
		t.Fatal("expected invalid model fields to display a visible validation error")
	}
	// JS dynamically updates HTMX method and action to include project_id for create/edit paths.
	if !strings.Contains(out, "form.removeAttribute('hx-put');") || !strings.Contains(out, "form.setAttribute('hx-post', _createUrl);") {
		t.Fatal("expected create flow to use hx-post and clear edit hx-put")
	}
	if !strings.Contains(out, "form.removeAttribute('hx-post');") || !strings.Contains(out, "form.setAttribute('hx-put', _editUrl);") {
		t.Fatal("expected edit flow to use hx-put and clear create hx-post")
	}
	if !strings.Contains(out, "form.action = _createUrl;") {
		t.Fatal("expected create flow to update form action")
	}
	if !strings.Contains(out, "form.action = _editUrl;") {
		t.Fatal("expected edit flow to update form action")
	}
	// project_id is taken from the live project selector first so mutations preserve
	// a newly selected project even if the URL query is missing or stale.
	if !strings.Contains(out, "function selectedProjectIDForModelMutation()") {
		t.Fatal("expected JS helper to resolve current project for model mutations")
	}
	if !strings.Contains(out, "document.getElementById('project-selector')") {
		t.Fatal("expected model mutation project helper to prefer active project selector")
	}
	if !strings.Contains(out, "new URLSearchParams(window.location.search)") {
		t.Fatal("expected JS to fall back to URL project_id params")
	}
	if !strings.Contains(out, "function modelMutationURL(path)") || !strings.Contains(out, "project_id=' + encodeURIComponent(projectID)") {
		t.Fatal("expected JS to append encoded project_id to model mutation URLs")
	}
	if !strings.Contains(out, "var _createUrl = modelMutationURL('/models');") {
		t.Fatal("expected create flow to preserve selected project in request URL")
	}
	if !strings.Contains(out, "var _editUrl = modelMutationURL('/models/' + id);") {
		t.Fatal("expected edit flow to preserve selected project in request URL")
	}
	if !strings.Contains(out, "form.dataset.mode = 'edit';") || !strings.Contains(out, "form.dataset.mode = 'create';") {
		t.Fatal("expected create/edit flow to track form mode")
	}
	if !strings.Contains(out, "document.getElementById('model_config_id').value = id;") {
		t.Fatal("expected edit flow to submit existing model config ID")
	}
	if !strings.Contains(out, "document.getElementById('model_config_id').value = '';") {
		t.Fatal("expected create flow to clear model config ID")
	}
}

func TestModelsContent_ModelMutationsPreserveActiveProject(t *testing.T) {
	agents := []models.LLMConfig{
		{ID: "model-a", Name: "Model A", Provider: models.ProviderAnthropic, Model: "claude-sonnet-5", IsDefault: false},
		{ID: "model-b", Name: "Model B", Provider: models.ProviderOpenAI, Model: "gpt-5", IsDefault: true},
	}
	var buf bytes.Buffer
	if err := ModelsContent(agents, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"var selector = document.getElementById('project-selector');",
		"if (selector && selector.value) return selector.value;",
		"return params.get('project_id') || '';",
		"var _createUrl = modelMutationURL('/models');",
		"var _editUrl = modelMutationURL('/models/' + id);",
		"data-model-set-default-url=",
		`onclick="event.stopPropagation(); setDefaultModel(this)"`,
		"htmx.ajax('POST', modelMutationURL(path)",
		"htmx.ajax('DELETE', modelMutationURL('/models/' + _deleteModelId)",
		"htmx.ajax('DELETE', modelMutationURL('/models/' + _deleteModelId + '?new_default_id=' + encodeURIComponent(newDefaultId))",
		"href = modelMutationURL(href);",
		"fetch(modelMutationURL('/models/oauth/manual-complete')",
		"window.location.href = modelMutationURL('/models');",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected rendered Models mutation flow to contain %q", want)
		}
	}

	for _, stale := range []string{
		"var _pid = _params.get('project_id')",
		"var _editPid = _editParams.get('project_id')",
		"htmx.ajax('DELETE', '/models/' + _deleteModelId",
		`hx-post="/models/model-a/set-default"`,
		"window.location.reload();",
	} {
		if strings.Contains(out, stale) {
			t.Fatalf("rendered Models mutation flow still contains stale project-less behavior %q", stale)
		}
	}
}

func TestModelsContent_ModelModalJavaScriptShape(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	for _, fn := range []string{
		"function handleModelChange()",
		"function toggleProviderFields(selectedModel, selectedReasoningEffort)",
		"function editModelFromData(button)",
		"function openNewModelModal()",
		"function discoverOpenAICompatibleModels()",
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
		"// Map DB provider values to UI values\t\t\t\tvar uiProvider = dbProvider;",
	} {
		if strings.Contains(out, broken) {
			t.Fatalf("rendered script contains known broken modal JavaScript fragment: %q", broken)
		}
	}
	if !strings.Contains(out, "/* Map DB provider values to UI values. */") || !strings.Contains(out, "var uiProvider = dbProvider;") {
		t.Fatal("expected edit modal JavaScript to initialize uiProvider before provider-specific mapping")
	}
}

func TestModelsContent_CardsCarryOnlyBoundedListData(t *testing.T) {
	agents := []models.LLMConfig{
		{
			ID: "default-model", Name: "Default OpenAI", Provider: models.ProviderOpenAI,
			Model: "gpt-5.5", AuthMethod: models.AuthMethodAPIKey, APIKey: "sk-default",
			Temperature: 0.42, IsDefault: true, AutoStartTasks: true,
		},
		{
			ID: "other-model", Name: "Other Claude", Provider: models.ProviderAnthropic,
			Model: "claude-sonnet-5", AuthMethod: models.AuthMethodOAuth,
			Temperature: 0.9, ReasoningEffort: "high",
		},
	}
	var buf bytes.Buffer
	if err := ModelsContent(agents, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	defaultCard := renderedModelCard(t, out, "default-model")
	if !strings.Contains(defaultCard, `onclick="editModelFromData(this)"`) || !strings.Contains(defaultCard, "Default</span>") {
		t.Fatal("expected default card to remain directly editable and display its default badge")
	}
	if strings.Contains(defaultCard, `data-model-set-default-url=`) {
		t.Fatal("default card should not render a set-default action")
	}
	for _, forbidden := range []string{"sk-default", "data-model-api-key", "data-model-auth-method", "data-model-auto-start-tasks", "data-model-reasoning-effort"} {
		if strings.Contains(defaultCard, forbidden) {
			t.Fatalf("bounded card leaked edit-only value %q", forbidden)
		}
	}

	otherCard := renderedModelCard(t, out, "other-model")
	if !strings.Contains(otherCard, `onclick="editModelFromData(this)"`) || !strings.Contains(otherCard, "Reasoning effort: high") {
		t.Fatal("expected non-default card display and edit action to remain intact")
	}
	for _, want := range []string{"/edit-details", "details.id !== id", "populateModelEditForm", "modelReasoningEffort: details.reasoning_effort"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected lazy edit script to contain %q", want)
		}
	}
}

func TestModelsContent_ModelCardsShowEffectiveSettings(t *testing.T) {
	agents := []models.LLMConfig{
		{
			ID:          "astra-model",
			Name:        "Astra",
			Provider:    models.ProviderOpenAI,
			Model:       "gpt-6-astra",
			Temperature: 0.8,
		},
		{
			ID:              "kimi-model",
			Name:            "Kimi",
			Provider:        models.ProviderOpenAICompatible,
			Model:           "kimi-k3",
			ReasoningEffort: "max",
			Temperature:     0.7,
		},
		{
			ID:              "glm-model",
			Name:            "GLM",
			Provider:        models.ProviderOpenAICompatible,
			Model:           "glm-5.2",
			ReasoningEffort: "high",
			Temperature:     0.4,
		},
	}
	var buf bytes.Buffer
	if err := ModelsContent(agents, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}

	astraCard := renderedModelCard(t, buf.String(), "astra-model")
	if strings.Contains(astraCard, "Temperature:") {
		t.Fatalf("Astra card should not show an unsupported temperature:\n%s", astraCard)
	}

	kimiCard := renderedModelCard(t, buf.String(), "kimi-model")
	if strings.Contains(kimiCard, "Temperature:") {
		t.Fatalf("Kimi card should not show an unused temperature:\n%s", kimiCard)
	}
	if !strings.Contains(kimiCard, "Reasoning effort: max") {
		t.Fatalf("Kimi card should show its reasoning effort:\n%s", kimiCard)
	}

	glmCard := renderedModelCard(t, buf.String(), "glm-model")
	if !strings.Contains(glmCard, "Temperature: 0.4") {
		t.Fatalf("GLM card should show its effective temperature:\n%s", glmCard)
	}
	if !strings.Contains(glmCard, "Reasoning effort: high") {
		t.Fatalf("GLM card should show its reasoning effort:\n%s", glmCard)
	}
}

func TestModelsContent_MixtureReferenceOrderingControls(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		`id="model_field"`,
		`if (modelField) modelField.classList.toggle('hidden', provider === 'mixture');`,
		`id="model_temperature_field"`,
		`function modelSupportsTemperature(provider, model)`,
		`provider === 'openai'`,
		`normalizedModel !== 'gpt-6-astra'`,
		`indexOf('kimi-') !== 0`,
		`function updateTemperatureField(provider, model)`,
		`field.classList.toggle('hidden', !supported);`,
		`input.disabled = !supported;`,
		`updateTemperatureField(provider, model);`,
		`function mixtureConfigValue(value, fallback)`,
		`mixtureConfigValue(cfg.reference_temperature, 0.6)`,
		`mixtureConfigValue(cfg.aggregator_temperature, 0.4)`,
		`id="model_mixture_reference_available"`,
		`id="model_mixture_references"`,
		`id="model_mixture_reference_ids_order"`,
		`onclick="addMixtureReference()"`,
		`onclick="moveMixtureReference(-1)"`,
		`onclick="moveMixtureReference(1)"`,
		`onclick="removeMixtureReference()"`,
		"Add Reference",
		"Move Up",
		"Move Down",
		"select one in the ordered list",
		"function renderMixtureReferenceOptions(selectedIDs)",
		"function addMixtureReference()",
		"function removeMixtureReference()",
		"function moveMixtureReference(direction)",
		"var index = select.selectedIndex;",
		"select.insertBefore(option, select.options[index - 1]);",
		"select.insertBefore(select.options[index + 1], option);",
		"syncMixtureReferenceOrderInput();",
		"selectedMixtureReferenceIDs().map(function(id)",
		"modelSelect.innerHTML = '<option value=\"mixture\">Mixture of Models</option>'",
		"return false;",
		"htmx:responseError",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected mixture reference ordering UI/script to contain %q", want)
		}
	}

	for _, broken := range []string{
		"Reference order follows the selected model list order",
		"function moveMixtureReferences(direction)",
		"opt.selected && !prev.selected",
		"current.selected && !next.selected",
		"id === aggregatorID",
		"The aggregator cannot also be a reference model.",
	} {
		if strings.Contains(out, broken) {
			t.Fatalf("rendered mixture reference ordering still contains broken fixed-order behavior: %q", broken)
		}
	}
}

func TestModelsContent_MixtureEditHydratesSavedReferenceOrderLazily(t *testing.T) {
	agents := []models.LLMConfig{
		{ID: "ref-a", Name: "Reference A", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-a"},
		{ID: "ref-b", Name: "Reference B", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodAPIKey, Model: "claude-b"},
		{ID: "mix", Name: "Ordered Mix", Provider: models.ProviderMixture, Model: "mixture", MixtureAggregatorID: "ref-a", MixtureReferenceCount: 2},
	}
	var buf bytes.Buffer
	if err := ModelsContent(agents, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()
	cardMarkup := renderedModelCard(t, out, "mix")
	if strings.Contains(cardMarkup, `data-model-mixture-config-json=`) || strings.Contains(cardMarkup, `reference_models`) {
		t.Fatal("initial mixture card exposed full edit configuration")
	}
	for _, want := range []string{
		"modelMixtureConfigJson: details.mixture_config_json || ''",
		"applyMixtureConfig(dbProvider === 'mixture' ? mixtureConfigJSON : '');",
		"renderMixtureReferenceOptions(selectedIDs);",
		"generation !== window._modelEditRequestGeneration",
		"window._modelEditRequestedID !== id",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected lazy ordered mixture hydration script to contain %q", want)
		}
	}
	if strings.Index(out, "toggleProviderFields(model, reasoningEffort);") > strings.Index(out, "applyMixtureConfig(dbProvider === 'mixture' ? mixtureConfigJSON : '')") {
		t.Fatal("expected edit mixture config hydration to run after provider field toggling")
	}
}

func TestModelsContent_MixturePickerFiltersNonCallableModels(t *testing.T) {
	agents := []models.LLMConfig{
		{ID: "api-openai", Name: "OpenAI API", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-5"},
		{ID: "oauth-anthropic", Name: "Claude OAuth", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token", OAuthExpiresAt: 9999999999999, Model: "claude-sonnet"},
		{ID: "cli-openai", Name: "Codex CLI", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodCLI, Model: "gpt-5-codex"},
		{ID: "cli-anthropic", Name: "Claude CLI", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodCLI, Model: "claude-sonnet"},
		{ID: "mixture", Name: "Existing Mixture", Provider: models.ProviderMixture, Model: "default"},
		{ID: "internal", Name: "Internal", Provider: models.LLMProvider("internal"), AuthMethod: models.AuthMethodAPIKey, Model: "internal"},
	}
	var buf bytes.Buffer
	if err := ModelsContent(agents, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()
	start := strings.Index(out, `id="mixture_fields"`)
	if start < 0 {
		t.Fatal("expected rendered mixture fields")
	}
	pickerMarkup := out[start:]
	if end := strings.Index(pickerMarkup, `>`); end >= 0 {
		pickerMarkup = pickerMarkup[:end]
	}

	for _, allowed := range []string{"api-openai", "oauth-anthropic", "OpenAI API", "Claude OAuth"} {
		if !strings.Contains(pickerMarkup, allowed) {
			t.Fatalf("expected callable mixture option %q in rendered picker data: %s", allowed, pickerMarkup)
		}
	}
	for _, blocked := range []string{"cli-openai", "cli-anthropic", "Codex CLI", "Claude CLI", "Existing Mixture", "internal"} {
		if strings.Contains(pickerMarkup, blocked) {
			t.Fatalf("expected non-callable mixture option %q to be omitted from picker data: %s", blocked, pickerMarkup)
		}
	}
}

func TestModelsContent_OpenAICompatibleDiscoveryUI(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	providerSelectStart := strings.Index(out, `<select id="model_provider"`)
	if providerSelectStart < 0 {
		t.Fatal("expected provider dropdown")
	}
	providerSelectEnd := strings.Index(out[providerSelectStart:], `</select>`)
	if providerSelectEnd < 0 {
		t.Fatal("expected provider dropdown closing tag")
	}
	providerSelectMarkup := out[providerSelectStart : providerSelectStart+providerSelectEnd]
	for _, want := range []string{`<optgroup label="Virtual Providers">`, `<option value="mixture">Mixture of Models</option>`, `<optgroup label="Model Providers">`} {
		if !strings.Contains(providerSelectMarkup, want) {
			t.Fatalf("expected provider dropdown to contain %q", want)
		}
	}
	if strings.Index(providerSelectMarkup, `<option value="mixture">Mixture of Models</option>`) > strings.Index(providerSelectMarkup, `<optgroup label="Model Providers">`) {
		t.Fatal("expected Mixture of Models to appear before other model providers")
	}

	presetOptions := map[string]string{
		"openrouter":          "OpenRouter",
		"nvidia_nim":          "NVIDIA NIM",
		"vllm":                "Local vLLM",
		"lm_studio":           "LM Studio",
		"sglang":              "SGLang",
		"litellm":             "LiteLLM",
		"deepinfra":           "DeepInfra",
		"fireworks":           "Fireworks",
		"groq":                "Groq",
		"mistral":             "Mistral",
		"cerebras":            "Cerebras",
		"together":            "Together",
		"huggingface_router":  "Hugging Face Router",
		"deepseek":            "DeepSeek",
		"moonshot":            "Moonshot",
		"dashscope":           "Qwen / DashScope",
		"dashscope_intl":      "Qwen / DashScope Intl",
		"alibaba_coding_plan": "Alibaba Coding Plan",
		"zai_glm":             "Z.AI / GLM",
		"novita":              "NovitaAI",
		"venice":              "Venice",
		"qianfan":             "Qianfan",
		"kilo_code":           "Kilo Code",
		"arcee":               "Arcee AI",
		"stepfun":             "StepFun",
		"stepfun_step_plan":   "StepFun Step Plan",
		"gmi_cloud":           "GMI Cloud",
		"chutes":              "Chutes",
		"tokenhub":            "Tencent TokenHub",
		"tokenhub_intl":       "Tencent TokenHub Intl",
		"xiaomi_mimo":         "Xiaomi MiMo",
		"inferrs":             "Inferrs Local",
		"ds4":                 "ds4 Local",
		"custom":              "Custom OpenAI-Compatible",
	}
	for slug, label := range presetOptions {
		want := `<option value="openai_compatible_` + slug + `">` + label + `</option>`
		if !strings.Contains(out, want) {
			t.Fatalf("expected provider dropdown to contain %q", want)
		}
	}

	presetDefaults := map[string]string{
		"openrouter":          "https://openrouter.ai/api/v1/",
		"nvidia_nim":          "https://integrate.api.nvidia.com/v1/",
		"vllm":                "http://127.0.0.1:8000/v1/",
		"lm_studio":           "http://127.0.0.1:1234/v1/",
		"sglang":              "http://127.0.0.1:30000/v1/",
		"litellm":             "http://localhost:4000/v1/",
		"deepinfra":           "https://api.deepinfra.com/v1/openai/",
		"fireworks":           "https://api.fireworks.ai/inference/v1/",
		"groq":                "https://api.groq.com/openai/v1/",
		"mistral":             "https://api.mistral.ai/v1/",
		"cerebras":            "https://api.cerebras.ai/v1/",
		"together":            "https://api.together.xyz/v1/",
		"huggingface_router":  "https://router.huggingface.co/v1/",
		"deepseek":            "https://api.deepseek.com/v1/",
		"moonshot":            "https://api.moonshot.ai/v1/",
		"dashscope":           "https://dashscope.aliyuncs.com/compatible-mode/v1/",
		"dashscope_intl":      "https://dashscope-intl.aliyuncs.com/compatible-mode/v1/",
		"alibaba_coding_plan": "https://coding-intl.dashscope.aliyuncs.com/v1/",
		"zai_glm":             "https://api.z.ai/api/paas/v4/",
		"novita":              "https://api.novita.ai/openai/v1/",
		"venice":              "https://api.venice.ai/api/v1/",
		"qianfan":             "https://qianfan.baidubce.com/v2/",
		"kilo_code":           "https://api.kilo.ai/api/gateway/",
		"arcee":               "https://api.arcee.ai/api/v1/",
		"stepfun":             "https://api.stepfun.ai/v1/",
		"stepfun_step_plan":   "https://api.stepfun.ai/step_plan/v1/",
		"gmi_cloud":           "https://api.gmi-serving.com/v1/",
		"chutes":              "https://llm.chutes.ai/v1/",
		"tokenhub":            "https://tokenhub.tencentmaas.com/v1/",
		"tokenhub_intl":       "https://tokenhub-intl.tencentmaas.com/v1/",
		"xiaomi_mimo":         "https://api.xiaomimimo.com/v1/",
		"inferrs":             "http://127.0.0.1:8080/v1/",
		"ds4":                 "http://127.0.0.1:18000/v1/",
	}
	for slug, baseURL := range presetDefaults {
		if !strings.Contains(out, slug+": '"+baseURL+"'") {
			t.Fatalf("expected preset default %s -> %s", slug, baseURL)
		}
	}

	for _, want := range []string{
		`<input type="hidden" id="model_provider_value" name="provider" value="anthropic"`,
		`<select id="model_provider"`,
		`oninput="syncModelAPIKeySubmitValue(); scheduleAutoDiscoverOpenAICompatibleModels()"`,
		`onsubmit="clearModelFormError(); return normalizeModelFormBeforeSubmit()"`,
		`<input type="hidden" id="model_openai_compatible_preset" name="preset_slug" value="custom"`,
		"OpenAI-compatible presets auto-load available models when selected; Custom stays manual.",
		"openai_compatible_openrouter: [",
		"openai_compatible_groq: [",
		"openai_compatible_deepseek: [",
		"openai_compatible_lm_studio: [",
		"openai_compatible_custom: [",
		"{ value: 'nvidia/nemotron-3-ultra-550b-a55b', label: 'NVIDIA Nemotron', efforts: [] }",
		"{ value: 'deepseek-chat', label: 'DeepSeek Chat', efforts: [] }",
		"{ value: 'local-model', label: 'LM Studio local model', efforts: [] }",
		"Enter model ID manually",
		"function modelOptionsForProvider(provider)",
		"isDiscoverableOpenAICompatiblePreset()",
		"runAutoDiscoverOpenAICompatibleModels();",
		"var forcePresetDefaults = selectedModel === undefined && selectedReasoningEffort === undefined;",
		"applyOpenAICompatiblePreset(forcePresetDefaults);",
		"if (!force && provider === 'openai_compatible_custom' && currentPreset !== 'custom' && !openAICompatiblePresetDefaults[currentPreset]) preset = currentPreset;",
		"var hasPresetDefault = Object.prototype.hasOwnProperty.call(openAICompatiblePresetDefaults, preset);",
		"var next = hasPresetDefault ? openAICompatiblePresetDefaults[preset] : '';",
		"if (hasPresetDefault && preset !== 'custom' && (force || (!isEditingModelForm() && !baseURL.value)))",
		"providerValue.value = 'openai_compatible';",
		"Enter the model ID manually for local or custom endpoints.",
		"/models/openai-compatible/available?",
		"new URLSearchParams({base_url: baseURL})",
		"X-OpenAI-Compatible-API-Key",
		"X-OpenAI-Compatible-Auth-Header-Name",
		"X-OpenAI-Compatible-Auth-Header-Prefix",
		"X-OpenAI-Compatible-Extra-Headers",
		"X-OpenAI-Compatible-Models-Array-Path",
		"X-OpenAI-Compatible-Model-ID-Field",
		"(!configID || customAuthMethod === 'api_key')",
		"clearExtraHeaders.checked",
		"cfg.model_id_field || 'id'",
		"data.resolved_id",
		"setOpenAICompatibleModelValue(models[i].id, models[i].id, false)",
		"setOpenAICompatibleModelValue(data.resolved_id, data.resolved_id, true)",
		"if (!isDiscoverableOpenAICompatiblePreset())",
		"document.getElementById('model_provider').value !== provider",
		"Discover Models",
		`onclick="discoverOpenAICompatibleModels()"`,
		`name="custom_static_headers_json"`,
		`name="custom_authorization_parameters_json"`,
		`name="custom_oauth_pkce"`,
		`name="custom_allow_private_endpoints"`,
		`name="custom_local_callback_host"`,
		`name="custom_local_callback_path"`,
		"The callback port is always selected automatically.",
		`cfg.local_callback_host || 'localhost'`,
		`cfg.local_callback_path || '/callback'`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected OpenAI-compatible discovery UI to contain %q", want)
		}
	}
	modelsPathIndex := strings.Index(out, `name="custom_models_array_path"`)
	oauthFieldsIndex := strings.Index(out, `id="custom_provider_oauth_fields"`)
	if modelsPathIndex < 0 || oauthFieldsIndex < 0 || modelsPathIndex > oauthFieldsIndex {
		t.Fatal("expected model discovery schema controls to be available outside the OAuth-only fields")
	}
	modelIDFieldIndex := strings.Index(out, `id="model_custom_model_id_field"`)
	if modelIDFieldIndex < 0 {
		t.Fatal("expected custom model ID field")
	}
	modelIDFieldMarkup := out[modelIDFieldIndex:]
	if end := strings.Index(modelIDFieldMarkup, `>`); end >= 0 {
		modelIDFieldMarkup = modelIDFieldMarkup[:end]
	}
	if !strings.Contains(modelIDFieldMarkup, `value="id"`) {
		t.Fatalf("expected custom model ID field to default to id: %s", modelIDFieldMarkup)
	}
	for _, forbidden := range []string{
		`<select id="model_openai_compatible_preset"`,
		`onchange="applyOpenAICompatiblePreset()"`,
		"api_key: apiKey",
		"api_key=",
		"openai_compatible_api_key",
		"Object.values(openAICompatiblePresetDefaults).indexOf(baseURL.value)",
		"Custom compatible model",
		"openai_compatible_xai",
		"GitHub Copilot",
		"Bedrock",
		"Gemini native",
		"' (discovered)'",
	} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("expected discovery UI not to contain %q", forbidden)
		}
	}
}

func renderedModelCard(t *testing.T, out, id string) string {
	t.Helper()
	marker := `data-model-id="` + id + `"`
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("expected rendered model card for %s", id)
	}
	cardClass := `<div class="card bg-base-100 shadow-sm border border-base-300 cursor-pointer`
	start := strings.LastIndex(out[:idx], cardClass)
	if start < 0 {
		t.Fatalf("expected model card %s to start with card container", id)
	}
	end := strings.Index(out[idx+len(marker):], cardClass)
	if end >= 0 {
		return out[start : idx+len(marker)+end]
	}
	modalStart := strings.Index(out[idx:], `<dialog id="new_model_modal"`)
	if modalStart < 0 {
		t.Fatalf("expected model card %s to be followed by modal markup", id)
	}
	return out[start : idx+modalStart]
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

// TestModelsContent_NoCLIOptionInAuthSelects verifies that the rendered model
// setup dialog no longer exposes the "CLI (OAuth via terminal)" option for
// Anthropic or OpenAI connection-method selects.
func TestModelsContent_NoCLIOptionInAuthSelects(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, `value="cli">CLI`) {
		t.Error("expected CLI option to be removed from auth/connection method selects, but found it in rendered HTML")
	}
	if strings.Contains(out, "CLI (OAuth via terminal)") {
		t.Error("expected CLI (OAuth via terminal) label to be absent from rendered auth selects")
	}

	// Auth method selects should each have only the oauth option remaining
	if !strings.Contains(out, `value="oauth">API (OAuth via web)`) {
		t.Error("expected OAuth option to remain in auth/connection method select")
	}
}

func TestModelsContent_OAuthLinksUseRuntimeSpecificLaunch(t *testing.T) {
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
	err := ModelsContent(agents, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "return launchOAuthInSystemBrowser(this.dataset.oauthPath, this)") {
		t.Fatal("expected OAuth links to use the runtime-specific launch helper")
	}
	if !strings.Contains(out, "data-oauth-path=\"/models/openai-oauth/oauth/initiate\"") {
		t.Fatal("expected OAuth links to expose model-specific oauth path via data attribute")
	}
	if !strings.Contains(out, "data-oauth-external=\"false\"") {
		t.Fatal("expected server-rendered OAuth links to use normal browser navigation")
	}
	if strings.Contains(out, "getAttribute('data-runtime')") {
		t.Fatal("expected OAuth launch mode not to depend on client-side runtime detection")
	}
	if !strings.Contains(out, "external=1") {
		t.Fatal("expected desktop OAuth launcher to request backend external launch mode")
	}
	if !strings.Contains(out, "fetch(externalURL") {
		t.Fatal("expected desktop OAuth launcher to call backend in background via fetch")
	}
	if strings.Contains(out, "window.location.href = externalURL") {
		t.Fatal("expected desktop OAuth launcher to avoid WebView navigation")
	}

	buf.Reset()
	if err := ModelsContent(agents, nil, true).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render desktop models content: %v", err)
	}
	if !strings.Contains(buf.String(), "data-oauth-external=\"true\"") {
		t.Fatal("expected desktop OAuth links to request external browser launch")
	}
}

func TestModelsContent_CustomOAuthEditLoadsRevealableSecretsAndHeaders(t *testing.T) {
	agents := []models.LLMConfig{{
		ID:                "custom-oauth",
		Name:              "Custom OAuth",
		Provider:          models.ProviderOpenAICompatible,
		AuthMethod:        models.AuthMethodOAuth,
		Model:             "custom-model",
		PresetSlug:        "custom",
		OAuthClientSecret: "saved-client-secret",
		ExtraHeadersJSON:  `{"X-Inference-Secret":"saved-inference-secret"}`,
		ExtraBodyJSON:     `{"saved_option":true}`,
		CustomAuthConfigJSON: `{"enabled":true,"signing_secret":"saved-signing-secret",` +
			`"static_headers":{"X-Required":"saved-static-header"},` +
			`"token_headers":{"X-Token":"saved-token-header"},` +
			`"refresh_headers":{"X-Refresh":"saved-refresh-header"},` +
			`"refresh_parameters":{"refresh_secret":"saved-refresh-parameter"}}`,
	}, {
		ID:                   "builtin-oauth",
		Name:                 "Built-in OAuth",
		Provider:             models.ProviderOpenAI,
		AuthMethod:           models.AuthMethodOAuth,
		Model:                "gpt-5.4",
		OAuthClientSecret:    "builtin-client-secret-must-not-render",
		CustomAuthConfigJSON: `{"signing_secret":"builtin-signing-secret-must-not-render"}`,
	}}

	var buf bytes.Buffer
	if err := ModelsContent(agents, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := html.UnescapeString(buf.String())

	if !strings.Contains(out, `id="models-container" data-search-container hx-history="false"`) {
		t.Fatal("expected the secret-bearing Models fragment to opt out of HTMX history snapshots")
	}
	for _, value := range []string{
		"saved-client-secret",
		"saved-signing-secret",
		"saved-inference-secret",
		"saved-static-header",
		"saved-token-header",
		"saved-refresh-header",
		"saved-refresh-parameter",
		`{"saved_option":true}`,
	} {
		if strings.Contains(out, value) {
			t.Errorf("initial Models response leaked saved edit value %q", value)
		}
	}
	for _, inputID := range []string{
		"model_custom_oauth_client_secret",
		"model_custom_signing_secret",
		"model_compatible_extra_headers",
		"model_custom_static_headers_json",
		"model_custom_token_headers_json",
		"model_custom_refresh_headers_json",
		"model_custom_refresh_parameters_json",
	} {
		if !strings.Contains(out, `togglePasswordVisibility('`+inputID+`', this)`) {
			t.Errorf("expected reveal control for %s", inputID)
		}
		if !strings.Contains(out, `resetSecretInputVisibility('`+inputID+`')`) {
			t.Errorf("expected %s to reset to hidden whenever the modal opens", inputID)
		}
	}
	for _, secret := range []string{
		"builtin-client-secret-must-not-render",
		"builtin-signing-secret-must-not-render",
	} {
		if strings.Contains(out, secret) {
			t.Errorf("built-in provider secret %q leaked into model page", secret)
		}
	}
	for _, inputID := range []string{
		"model_compatible_extra_headers",
		"model_custom_static_headers_json",
		"model_custom_token_headers_json",
		"model_custom_refresh_headers_json",
		"model_custom_refresh_parameters_json",
	} {
		if strings.Contains(out, `<textarea id="`+inputID+`"`) {
			t.Errorf("sensitive JSON field %s rendered as a plaintext textarea", inputID)
		}
	}
	if strings.Contains(out, "Leave blank to keep saved secret") ||
		strings.Contains(out, `name="clear_oauth_client_secret"`) ||
		strings.Contains(out, `name="custom_clear_signing_secret"`) {
		t.Fatal("expected custom OAuth edit controls not to use blank-preserve or separate-clear behavior")
	}
	if !strings.Contains(out, "modelOauthClientSecret: details.oauth_client_secret || ''") ||
		!strings.Contains(out, "modelExtraHeadersJson: details.extra_headers_json || ''") ||
		!strings.Contains(out, "modelCustomAuthConfig: details.custom_auth_config_json || ''") ||
		!strings.Contains(out, "cfg.signing_secret || ''") ||
		!strings.Contains(out, "cfg.static_headers ? JSON.stringify") {
		t.Fatal("expected edit script to populate all saved custom OAuth secret and header values")
	}
}
