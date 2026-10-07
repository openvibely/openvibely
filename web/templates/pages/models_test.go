package pages

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

func TestOAuthStatusTextShowsPermanentReauthenticationState(t *testing.T) {
	cfg := models.LLMConfig{
		Provider:         models.ProviderAnthropic,
		AuthMethod:       models.AuthMethodOAuth,
		OAuthAccessToken: "present",
		OAuthExpiresAt:   time.Now().Add(time.Hour).UnixMilli(),
		OAuthNeedsReauth: true,
	}
	if got := oauthStatusText(cfg); got != "Reconnect Required" {
		t.Fatalf("oauthStatusText = %q, want Reconnect Required", got)
	}
}

func TestModelSearchTextIncludesOAuthAccountName(t *testing.T) {
	agent := models.LLMConfig{
		Name:                "Fast model",
		Provider:            models.ProviderOpenAI,
		Model:               "gpt-test",
		AuthMethod:          models.AuthMethodOAuth,
		OAuthConnectionName: "alice@example.com",
	}
	if got := modelSearchText(agent); !strings.Contains(got, "alice@example.com") {
		t.Fatalf("modelSearchText = %q, want OAuth account name", got)
	}
}

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

func TestModelsContent_CatalogModelsInSelector(t *testing.T) {
	agents := []models.LLMConfig{}
	var buf bytes.Buffer
	err := ModelsContent(agents, nil, false).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	// Selector contents and order follow the catalog for both providers.
	for _, provider := range []models.LLMProvider{models.ProviderAnthropic, models.ProviderOpenAI} {
		lastJSON, lastHTML := -1, -1
		for _, spec := range models.ProviderModels(provider) {
			marker := fmt.Sprintf(`&#34;value&#34;:&#34;%s&#34;`, spec.ID)
			position := strings.Index(out, marker)
			if position < 0 || position <= lastJSON {
				t.Errorf("JavaScript catalog missing or out of order: %s", spec.ID)
			}
			lastJSON = position
			// OpenAI options are populated by JavaScript.
			if provider == models.ProviderAnthropic {
				position = strings.Index(out, fmt.Sprintf(`value="%s"`, spec.ID))
				if position < 0 || position <= lastHTML {
					t.Errorf("HTML selector missing or out of order: %s", spec.ID)
				}
				lastHTML = position
			}
		}
	}

	if strings.Contains(out, "defaultMaxTokens") {
		t.Error("expected browser catalog not to expose internal output-token defaults")
	}
	if strings.Contains(out, "Max Output Tokens / Request") || strings.Contains(out, "model_max_tokens") {
		t.Error("expected model dialog not to expose internal output-token cap")
	}
	if !strings.Contains(out, "Choose a custom limit to restrict concurrent workers for this model.") {
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
	if !strings.Contains(out, "Connect OAuth to load models for these settings.") {
		t.Error("expected OAuth discovery to require reconnecting after endpoint changes")
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
	allEfforts := []string{"low", "medium", "high", "xhigh", "max"}
	noneEfforts := []string{"none", "low", "medium", "high", "xhigh", "max"}
	for model, want := range map[string][]string{
		"claude-sonnet-5-5": allEfforts, "claude-sonnet-5": allEfforts,
		"claude-opus-5-5": allEfforts, "claude-opus-5": allEfforts, "claude-fable-5-1": allEfforts,
		"gpt-5.6-sol": noneEfforts, "gpt-6-astra": allEfforts,
		"gpt-6.1-sol": allEfforts, "gpt-6-sol": noneEfforts, "gpt-6-luna": noneEfforts,
	} {
		provider := models.ProviderOpenAI
		if strings.HasPrefix(model, "claude-") {
			provider = models.ProviderAnthropic
		}
		spec, ok := models.LookupModel(provider, model)
		if !ok || !slices.Equal(spec.ReasoningEfforts, want) {
			t.Errorf("catalog efforts for %s = %v, want %v", model, spec.ReasoningEfforts, want)
		}
	}
	if models.ModelSupportsTemperature(models.ProviderOpenAI, "gpt-6.1-sol") {
		t.Error("expected GPT-6.1 Sol temperature control to be disabled")
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
	for _, model := range []string{"claude-fable-5", "claude-mythos-5-1", "claude-mythos-5", "claude-opus-4-7", "claude-opus-4-8"} {
		spec, ok := models.LookupModel(models.ProviderAnthropic, model)
		if !ok || !slices.Equal(spec.ReasoningEfforts, allEfforts) {
			t.Errorf("catalog efforts for %s = %v, want %v", model, spec.ReasoningEfforts, allEfforts)
		}
	}
}

func TestBuiltInModelOptionsPreserveCatalogReasoningDefaults(t *testing.T) {
	var options map[string][]struct {
		Value         string   `json:"value"`
		DefaultEffort string   `json:"defaultEffort"`
		Efforts       []string `json:"efforts"`
	}
	if err := json.Unmarshal([]byte(builtInModelOptionsJSON()), &options); err != nil {
		t.Fatal(err)
	}
	for _, option := range options[string(models.ProviderOpenAI)] {
		spec, ok := models.LookupModel(models.ProviderOpenAI, option.Value)
		if !ok || option.DefaultEffort != spec.DefaultReasoningEffort {
			t.Errorf("%s browser default %q differs from catalog %q", option.Value, option.DefaultEffort, spec.DefaultReasoningEffort)
		}
	}
	wantEfforts := []string{"low", "medium", "high", "xhigh", "max"}
	for _, option := range options[string(models.ProviderAnthropic)] {
		if option.Value == "claude-sonnet-5-5" {
			if !slices.Equal(option.Efforts, wantEfforts) {
				t.Fatalf("Sonnet 5.5 browser efforts = %v, want %v", option.Efforts, wantEfforts)
			}
			return
		}
	}
	t.Fatal("Sonnet 5.5 missing from Anthropic browser model options")
}

func TestModelsContent_AnthropicDefaultModelSelection(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	selectStart := strings.Index(out, `id="model_id"`)
	if selectStart < 0 {
		t.Fatal("expected model selector")
	}
	selectHTML := out[selectStart:]
	if selectEnd := strings.Index(selectHTML, "</select>"); selectEnd >= 0 {
		selectHTML = selectHTML[:selectEnd]
	}
	firstOption := strings.Index(selectHTML, "<option ")
	if firstOption < 0 || !strings.HasPrefix(selectHTML[firstOption:], `<option value="claude-opus-5-5"`) {
		t.Fatal("expected claude-opus-5-5 to be the default Anthropic HTML option")
	}

	options := models.ProviderModels(models.ProviderAnthropic)
	if len(options) == 0 || options[0].ID != "claude-opus-5-5" {
		t.Fatal("expected claude-opus-5-5 to be the default Anthropic catalog entry")
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

func TestModelsContent_OpenAICompatibleDiscoveryCancelsStaleRequest(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"var openAICompatibleDiscoveryAbortController = null;",
		"var openAICompatibleDiscoveryGeneration = 0;",
		"function cancelOpenAICompatibleDiscovery()",
		"openAICompatibleDiscoveryAbortController.abort();",
		"cancelOpenAICompatibleDiscovery();",
		"var discoveryAbortController = typeof AbortController === 'function' ? new AbortController() : null;",
		"if (discoveryAbortController) fetchOptions.signal = discoveryAbortController.signal;",
		"fetch('/models/openai-compatible/available?' + params.toString(), fetchOptions)",
		"if (err && err.name === 'AbortError') return;",
		"openAICompatibleDiscoveryGeneration !== discoveryGeneration",
		"if (generation !== openAICompatibleDiscoveryGeneration) return;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected OpenAI-compatible discovery cancellation script to contain %q", want)
		}
	}
	if strings.Contains(out, "fetch('/models/openai-compatible/available?' + params.toString(), {headers: headers})") {
		t.Fatal("discovery fetch still omits AbortController signal")
	}
	cancelBody := renderedFunctionBody(t, out, "function cancelOpenAICompatibleDiscovery()")
	for _, want := range []string{
		"clearTimeout(openAICompatibleDiscoveryTimer);",
		"openAICompatibleDiscoveryTimer = null;",
	} {
		if !strings.Contains(cancelBody, want) {
			t.Fatalf("expected cancellation to clear pending OpenAI-compatible discovery debounce timer with %q", want)
		}
	}
	scheduleBody := renderedFunctionBody(t, out, "function scheduleAutoDiscoverOpenAICompatibleModels()")
	if !strings.Contains(scheduleBody, "cancelOpenAICompatibleDiscovery();") {
		t.Fatal("scheduled discovery should cancel the prior stale request before starting a debounce")
	}
	for _, lifecycle := range []struct {
		name string
		sig  string
	}{
		{name: "provider changes", sig: "function toggleProviderFields(selectedModel, selectedReasoningEffort)"},
		{name: "edit population", sig: "function populateModelEditForm(button)"},
		{name: "modal close", sig: "function closeModelModal()"},
		{name: "new modal reset", sig: "function openNewModelModal()"},
	} {
		body := renderedFunctionBody(t, out, lifecycle.sig)
		if !strings.Contains(body, "cancelOpenAICompatibleDiscovery();") {
			t.Fatalf("expected %s lifecycle to cancel stale OpenAI-compatible discovery", lifecycle.name)
		}
	}
	successCloseStart := strings.Index(out, "document.body.addEventListener('htmx:afterSwap'")
	if successCloseStart < 0 {
		t.Fatal("expected rendered script to include the HTMX success modal close handler")
	}
	successCloseEnd := strings.Index(out[successCloseStart:], "document.body.addEventListener('htmx:responseError'")
	if successCloseEnd < 0 {
		t.Fatal("expected rendered script to include the HTMX response error handler after success close handler")
	}
	successCloseHandler := out[successCloseStart : successCloseStart+successCloseEnd]
	cancelIndex := strings.Index(successCloseHandler, "cancelOpenAICompatibleDiscovery();")
	closeIndex := strings.Index(successCloseHandler, "modal.close();")
	if cancelIndex < 0 || closeIndex < 0 || cancelIndex > closeIndex {
		t.Fatal("expected successful HTMX model save modal close to cancel stale OpenAI-compatible discovery before closing")
	}
	modelModalStart := strings.Index(out, `<dialog id="new_model_modal"`)
	if modelModalStart < 0 {
		t.Fatal("expected rendered content to include the model modal")
	}
	modelModalEnd := strings.Index(out[modelModalStart:], `</dialog>`)
	if modelModalEnd < 0 {
		t.Fatal("expected rendered content to include the model modal closing tag")
	}
	modelModal := out[modelModalStart : modelModalStart+modelModalEnd]
	if strings.Contains(modelModal, `<form method="dialog" class="modal-backdrop"><button>close</button></form>`) {
		t.Fatal("model modal backdrop still uses native dialog close without cancelling stale discovery")
	}
	if !strings.Contains(modelModal, `<form class="modal-backdrop"><button type="button" onclick="closeModelModal()">close</button></form>`) {
		t.Fatal("expected model modal backdrop to close through the cancellation-aware helper")
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
	if !strings.Contains(defaultCard, `return; editModelFromData(this)"`) || !strings.Contains(defaultCard, "Default</span>") {
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
	if !strings.Contains(otherCard, `return; editModelFromData(this)"`) || !strings.Contains(otherCard, "Reasoning effort: high") {
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
		`option.temperature !== false`,
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

func TestModelsContentOmitsRetiredOpenAIOptions(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	visible := models.ProviderModels(models.ProviderOpenAI)
	for _, model := range []string{"gpt-5.2-codex", "gpt-5.1-codex-max", "gpt-5.1-codex", "gpt-5.1-codex-mini", "gpt-5-codex", "gpt-5-codex-mini", "gpt-5.3-codex-spark"} {
		if slices.ContainsFunc(visible, func(spec models.ModelSpec) bool { return spec.ID == model }) {
			t.Errorf("retired model %s remains selectable", model)
		}
	}
	if !slices.ContainsFunc(visible, func(spec models.ModelSpec) bool { return spec.ID == "gpt-6-astra" }) {
		t.Fatal("current OpenAI model missing from catalog")
	}
}

func TestModelsContentKeepsRetiredConfigurationWithError(t *testing.T) {
	agent := models.LLMConfig{
		ID:       "retired-model",
		Name:     "Old Claude",
		Provider: models.ProviderAnthropic,
		Model:    "claude-opus-4-5",
	}
	var buf bytes.Buffer
	if err := ModelsContent([]models.LLMConfig{agent}, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	card := renderedModelCard(t, buf.String(), agent.ID)
	if !strings.Contains(card, agent.Model) {
		t.Fatal("retired saved model was not rendered")
	}
	if !strings.Contains(card, "This model is no longer supported") {
		t.Fatal("retired saved model has no unsupported error")
	}
}

func TestBrowserFunctional_ModelEditorRetiredOptionsInChrome(t *testing.T) {
	var rendered bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &rendered); err != nil {
		t.Fatal(err)
	}
	// Exercise the rendered dropdown function without unrelated page lifecycle code.
	source := rendered.String()
	start := strings.Index(source, "function setModelOptions(provider, selectedModel)")
	end := strings.Index(source, "function handleModelChange()")
	if start < 0 || end <= start {
		t.Fatal("model dropdown function missing")
	}
	fixture := `<main id="reconnect-result"></main><select id="model_id"></select><script>
var catalog = ` + builtInModelOptionsJSON() + `;
function canonicalProvider(provider) { return provider; }
function isOpenAICompatibleProvider(provider) { return provider === 'openai_compatible'; }
function modelOptionsForProvider(provider) { return catalog[provider] || []; }
` + source[start:end] + `
try {
  [
    ['anthropic', 'claude-opus-4-5', true],
    ['openai', 'gpt-5.2-codex', true],
    ['anthropic', catalog.anthropic[0].value, false],
    ['openai', catalog.openai[0].value, false],
    ['openai_compatible', 'custom-model', false],
    ['ollama', 'local-model', false]
  ].forEach(function(test) {
    setModelOptions(test[0], test[1]);
    var select = document.getElementById('model_id');
    var option = select.selectedOptions[0];
    if (!option || option.value !== test[1]) throw new Error('saved model lost: ' + test[1]);
    if (option.disabled !== test[2]) throw new Error('wrong availability: ' + test[1]);
    if (test[2] && option.textContent.indexOf('unavailable') < 0) throw new Error('missing warning: ' + test[1]);
  });
  document.getElementById('reconnect-result').setAttribute('data-test-result', 'pass');
} catch (error) {
  var result = document.getElementById('reconnect-result');
  result.setAttribute('data-test-result', 'fail');
  result.setAttribute('data-test-error', String(error));
}
</script>`
	runReconnectChromeFixture(t, fixture)
}

func TestModelsContent_MixturePickerFiltersNonCallableModels(t *testing.T) {
	agents := []models.LLMConfig{
		{ID: "api-openai", Name: "OpenAI API", Provider: models.ProviderOpenAI, AuthMethod: models.AuthMethodAPIKey, Model: "gpt-6-astra"},
		{ID: "oauth-anthropic", Name: "Claude OAuth", Provider: models.ProviderAnthropic, AuthMethod: models.AuthMethodOAuth, OAuthAccessToken: "token", OAuthExpiresAt: 9999999999999, Model: "claude-opus-5"},
		{ID: "unsupported-openai", Name: "Codex unsupported auth", Provider: models.ProviderOpenAI, AuthMethod: "unsupported", Model: "gpt-6-astra"},
		{ID: "unsupported-anthropic", Name: "Claude unsupported auth", Provider: models.ProviderAnthropic, AuthMethod: "unsupported", Model: "claude-opus-5"},
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
	for _, blocked := range []string{"unsupported-openai", "unsupported-anthropic", "Codex unsupported auth", "Claude unsupported auth", "Existing Mixture", "internal"} {
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
		`id="model_base_url" name="base_url" class="input input-bordered" placeholder="https://openrouter.ai/api/v1/" oninput="clearDiscoveredModelLimits(); scheduleAutoDiscoverOpenAICompatibleModels()"`,
		`oninput="syncModelAPIKeySubmitValue(); scheduleAutoDiscoverOpenAICompatibleModels()"`,
		`onsubmit="clearModelFormError(); if (!normalizeModelFormBeforeSubmit()) { event.preventDefault(); event.stopImmediatePropagation(); return false; }"`,
		`<input type="hidden" id="model_openai_compatible_preset" name="preset_slug" value="custom"`,
		"Loading models…",
		"openai_compatible_openrouter: [",
		"openai_compatible_groq: [",
		"openai_compatible_deepseek: [",
		"openai_compatible_lm_studio: [",
		"openai_compatible_custom: [",
		"{ value: 'nvidia/nemotron-3-ultra-550b-a55b', label: 'NVIDIA Nemotron', efforts: [] }",
		"{ value: 'deepseek-chat', label: 'DeepSeek Chat', efforts: [] }",
		"{ value: 'local-model', label: 'LM Studio local model', efforts: [] }",
		"No models available",
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
		"function currentOpenAICompatibleDiscoveryIdentity()",
		"var discoveryIdentity = currentOpenAICompatibleDiscoveryIdentity();",
		"currentOpenAICompatibleDiscoveryIdentity() !== discoveryIdentity",
		"api_key: document.getElementById('model_api_key').value.trim()",
		"custom_auth_method: document.getElementById('model_custom_auth_method').value",
		"auth_header_name: document.getElementById('model_compatible_auth_header_name').value.trim()",
		"auth_header_prefix: document.getElementById('model_compatible_auth_header_prefix').value",
		"extra_headers: document.getElementById('model_compatible_extra_headers').value.trim()",
		"clear_extra_headers: !!(clearExtraHeaders && clearExtraHeaders.checked)",
		"models_array_path: document.getElementById('model_custom_models_array_path').value.trim()",
		"model_id_field: document.getElementById('model_custom_model_id_field').value.trim()",
		"allow_private: document.getElementById('model_custom_allow_private_endpoints').checked",
		"(!configID || customAuthMethod === 'api_key')",
		"clearExtraHeaders.checked",
		"cfg.model_id_field || 'id'",
		"setOpenAICompatibleModelValue(models[i].id, models[i].id, false)",
		"setOpenAICompatibleModelValue(models[0].id, models[0].id, true)",
		"if (!isDiscoverableOpenAICompatiblePreset())",
		`aria-label="Refresh models"`,
		`onclick="refreshModelDiscovery()"`,
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
	for _, control := range []struct {
		id    string
		event string
	}{
		{id: "model_custom_auth_method", event: `onchange="toggleCustomProviderAuthFields(); scheduleAutoDiscoverOpenAICompatibleModels()"`},
		{id: "model_custom_models_array_path", event: `oninput="scheduleAutoDiscoverOpenAICompatibleModels()"`},
		{id: "model_custom_model_id_field", event: `oninput="scheduleAutoDiscoverOpenAICompatibleModels()"`},
		{id: "model_compatible_auth_header_name", event: `oninput="scheduleAutoDiscoverOpenAICompatibleModels()"`},
		{id: "model_compatible_auth_header_prefix", event: `oninput="scheduleAutoDiscoverOpenAICompatibleModels()"`},
		{id: "model_compatible_extra_headers", event: `oninput="scheduleAutoDiscoverOpenAICompatibleModels()"`},
		{id: "model_custom_allow_private_endpoints", event: `onchange="scheduleAutoDiscoverOpenAICompatibleModels()"`},
	} {
		markup := renderedTagWithID(t, out, control.id)
		if !strings.Contains(markup, control.event) {
			t.Fatalf("expected %s to schedule OpenAI-compatible discovery cancellation, got %s", control.id, markup)
		}
	}
	modelIDFieldMarkup := renderedTagWithID(t, out, "model_custom_model_id_field")
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

func renderedFunctionBody(t *testing.T, out, signature string) string {
	t.Helper()
	idx := strings.Index(out, signature)
	if idx < 0 {
		t.Fatalf("expected rendered script to contain %s", signature)
	}
	open := strings.Index(out[idx:], "{")
	if open < 0 {
		t.Fatalf("expected rendered function %s to have an opening brace", signature)
	}
	bodyStart := idx + open + 1
	depth := 1
	inSingle := false
	inDouble := false
	inTemplate := false
	escaped := false
	for pos := bodyStart; pos < len(out); pos++ {
		ch := out[pos]
		if escaped {
			escaped = false
			continue
		}
		if inSingle || inDouble || inTemplate {
			if ch == '\\' {
				escaped = true
				continue
			}
			if inSingle && ch == '\'' {
				inSingle = false
			} else if inDouble && ch == '"' {
				inDouble = false
			} else if inTemplate && ch == '`' {
				inTemplate = false
			}
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
			if depth == 0 {
				return out[bodyStart:pos]
			}
		}
	}
	t.Fatalf("expected rendered function %s to have a closing brace", signature)
	return ""
}

func renderedTagWithID(t *testing.T, out, id string) string {
	t.Helper()
	marker := `id="` + id + `"`
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("expected rendered element with id %s", id)
	}
	start := strings.LastIndex(out[:idx], "<")
	if start < 0 {
		t.Fatalf("expected rendered element %s to have a start tag", id)
	}
	end := strings.Index(out[idx:], ">")
	if end < 0 {
		t.Fatalf("expected rendered element %s to have an end of start tag", id)
	}
	return out[start : idx+end+1]
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

func TestModelsContent_ConnectionSelectsOnlyOfferOAuth(t *testing.T) {
	var buf bytes.Buffer
	if err := ModelsContent(nil, nil, false).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()

	for _, id := range []string{"model_openai_connection_method", "model_auth_method"} {
		start := strings.Index(out, `id="`+id+`"`)
		if start < 0 {
			t.Fatalf("missing select %s", id)
		}
		end := strings.Index(out[start:], "</select>")
		if end < 0 {
			t.Fatalf("unclosed select %s", id)
		}
		options := out[start : start+end]
		if strings.Count(options, "<option") != 1 || !strings.Contains(options, `value="oauth"`) {
			t.Errorf("%s must offer only OAuth: %s", id, options)
		}
	}
}

func TestModelsContent_OAuthAccountDropdownShowsProviderProfileNames(t *testing.T) {
	connections := []models.OAuthConnection{
		{
			ID:           "anthropic-connection",
			Name:         "Alice",
			Provider:     models.ProviderAnthropic,
			NeedsReauth:  true,
			LinkedModels: 4,
		},
		{
			ID:           "openai-connection",
			Name:         "Engineering workspace",
			Provider:     models.ProviderOpenAI,
			AccessToken:  "present",
			LinkedModels: 3,
		},
		{
			ID:       "unnamed-anthropic-connection",
			Provider: models.ProviderAnthropic,
		},
	}

	var buf bytes.Buffer
	if err := modelsContent(nil, nil, connections, nil, false, false, CardListState{}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render models content: %v", err)
	}
	out := buf.String()
	selectStart := strings.Index(out, `id="model_oauth_connection_id"`)
	if selectStart < 0 {
		t.Fatal("expected OAuth Account dropdown")
	}
	selectEnd := strings.Index(out[selectStart:], `</select>`)
	if selectEnd < 0 {
		t.Fatal("expected OAuth Account dropdown closing tag")
	}
	dropdown := out[selectStart : selectStart+selectEnd]
	for _, want := range []string{
		`<option value="anthropic-connection" data-provider="anthropic" data-status="Reconnect required">Alice</option>`,
		`<option value="openai-connection" data-provider="openai" data-status="Connected">Engineering workspace</option>`,
		`<option value="unnamed-anthropic-connection" data-provider="anthropic" data-status="Not connected">Anthropic account</option>`,
	} {
		if !strings.Contains(dropdown, want) {
			t.Errorf("expected account option %q", want)
		}
	}
	for _, unwanted := range []string{"4 models", "3 models", "Alice ·", "Engineering workspace ·"} {
		if strings.Contains(dropdown, unwanted) {
			t.Errorf("OAuth Account dropdown exposed unwanted option text %q", unwanted)
		}
	}
}

func TestModelsContent_EditorOAuthActionUsesRuntimeSpecificLaunch(t *testing.T) {
	agents := []models.LLMConfig{
		{
			ID:                  "openai-oauth",
			Name:                "OpenAI OAuth",
			Provider:            models.ProviderOpenAI,
			AuthMethod:          models.AuthMethodOAuth,
			Model:               "gpt-5.4",
			OAuthConnectionID:   "openai-connection",
			OAuthConnectionName: "OpenAI account",
		},
		{
			ID:                  "healthy-oauth",
			Name:                "Healthy OAuth",
			Provider:            models.ProviderOpenAI,
			AuthMethod:          models.AuthMethodOAuth,
			Model:               "gpt-5.4",
			OAuthConnectionID:   "healthy-connection",
			OAuthConnectionName: "OpenAI account",
			OAuthAccessToken:    "present",
			OAuthExpiresAt:      time.Now().Add(time.Hour).UnixMilli(),
		},
	}
	connections := []models.OAuthConnection{{
		ID:          "openai-connection",
		Name:        "OpenAI account",
		Provider:    models.ProviderOpenAI,
		AccessToken: "present",
	}}

	var buf bytes.Buffer
	err := modelsContent(agents, agents, connections, nil, false, false, CardListState{}).Render(context.Background(), &buf)
	if err != nil {
		t.Fatalf("render models content: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "return launchOAuthInSystemBrowser(this.dataset.oauthPath, this)") {
		t.Fatal("expected editor OAuth action to use the runtime-specific launch helper")
	}
	if !strings.Contains(out, `data-oauth-path="/models/openai-oauth/oauth/initiate"`) || !strings.Contains(out, `href="/models/openai-oauth/oauth/initiate"`) {
		t.Fatal("expected disconnected OAuth model cards to expose the OAuth connection action")
	}
	if !strings.Contains(out, `>Connect OAuth</a>`) {
		t.Fatal("expected disconnected OAuth model cards to label the action Connect OAuth")
	}
	if strings.Contains(out, `data-oauth-path="/models/healthy-oauth/oauth/initiate"`) || strings.Contains(out, `href="/models/healthy-oauth/oauth/initiate"`) {
		t.Fatal("expected healthy OAuth model cards not to expose a reconnect action")
	}
	if strings.Contains(out, "Disconnect account") || strings.Contains(out, "Reconnect account") {
		t.Fatal("expected model cards not to expose connection-wide account controls")
	}
	if !strings.Contains(out, `'/models/' + encodeURIComponent(id) + '/oauth/initiate'`) {
		t.Fatal("expected the editor to derive its OAuth path from the loaded model")
	}
	if !strings.Contains(out, "select.value !== persistedConnectionID") || !strings.Contains(out, "Save this model before connecting the selected account") {
		t.Fatal("expected changed account selections to require save before OAuth initiation")
	}
	if !strings.Contains(out, "onclick=\"startCustomModelOAuth(this)\"") {
		t.Fatal("expected custom OpenAI-compatible OAuth to retain an editor-owned connect action")
	}
	if !strings.Contains(out, "data-oauth-external=\"false\"") {
		t.Fatal("expected server-rendered editor OAuth action to use normal browser navigation")
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
	if err := modelsContent(agents, agents, connections, nil, true, false, CardListState{}).Render(context.Background(), &buf); err != nil {
		t.Fatalf("render desktop models content: %v", err)
	}
	if !strings.Contains(buf.String(), "data-oauth-external=\"true\"") {
		t.Fatal("expected desktop editor OAuth action to request external browser launch")
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
