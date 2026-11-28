package components

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

func TestConversationModelPickerIconsCoverAllProviders(t *testing.T) {
	start := strings.Index(conversationModelPickerIconsJS, "var i={")
	if start < 0 {
		t.Fatal("could not locate icon map in conversation_model_picker_icons.js")
	}
	jsonStart := start + len("var i=")
	end := strings.Index(conversationModelPickerIconsJS[jsonStart:], "};")
	if end < 0 {
		t.Fatal("could not locate end of icon map in conversation_model_picker_icons.js")
	}
	var icons map[string]string
	if err := json.Unmarshal([]byte(conversationModelPickerIconsJS[jsonStart:jsonStart+end+1]), &icons); err != nil {
		t.Fatalf("parse icons json: %v", err)
	}
	for _, pair := range [][2]string{
		{"Qwen / DashScope Intl", "Qwen / DashScope"},
		{"StepFun Step Plan", "StepFun"},
		{"Tencent TokenHub Intl", "Tencent TokenHub"},
	} {
		if strings.Contains(conversationModelPickerIconsJS, `i["`+pair[0]+`"]=i["`+pair[1]+`"]`) {
			icons[pair[0]] = icons[pair[1]]
		}
	}
	for _, key := range []string{"__pins", "__manage", "__fallback"} {
		if !strings.Contains(icons[key], "<svg") {
			t.Errorf("missing UI SVG for %q", key)
		}
	}
	coreProviders := []models.LLMConfig{
		{Provider: models.ProviderAnthropic},
		{Provider: models.ProviderOpenAI},
		{Provider: models.ProviderGemini},
		{Provider: models.ProviderOllama},
		{Provider: models.ProviderMixture},
	}
	presets := []string{
		"openrouter", "nvidia_nim", "vllm", "lm_studio", "sglang", "litellm",
		"deepinfra", "fireworks", "groq", "mistral", "cerebras", "together",
		"huggingface_router", "deepseek", "moonshot", "dashscope", "dashscope_intl",
		"alibaba_coding_plan", "zai_glm", "novita", "venice", "qianfan", "kilo_code",
		"arcee", "stepfun", "stepfun_step_plan", "gmi_cloud", "chutes",
		"tokenhub", "tokenhub_intl", "xiaomi_mimo", "inferrs", "ds4",
	}
	for _, slug := range presets {
		coreProviders = append(coreProviders, models.LLMConfig{
			Provider:   models.ProviderOpenAICompatible,
			PresetSlug: slug,
		})
	}
	for _, cfg := range coreProviders {
		label := pickerProvider(cfg)
		svg := icons[label]
		if !strings.Contains(svg, "<svg") || !strings.Contains(svg, "viewBox=") {
			t.Errorf("provider %q (preset %q) is missing a valid SVG icon", label, cfg.PresetSlug)
		}
		if strings.Contains(strings.ToLower(svg), "</script") {
			t.Errorf("provider %q SVG must not contain closing script tag", label)
		}
	}
}
