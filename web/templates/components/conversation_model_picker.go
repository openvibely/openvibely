package components

import (
	_ "embed"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

//go:embed conversation_model_picker.js
var conversationModelPickerJS string

//go:embed conversation_model_picker.css
var conversationModelPickerCSS string

func pickerProvider(c models.LLMConfig) string {
	switch c.Provider {
	case models.ProviderAnthropic:
		return "Anthropic"
	case models.ProviderOpenAI:
		return "OpenAI"
	case models.ProviderOllama:
		return "Ollama"
	case models.ProviderMixture:
		return "Mixture"
	case models.ProviderOpenAICompatible:
		labels := map[string]string{
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
		if label := labels[c.PresetSlug]; label != "" {
			return label
		}
		if c.PresetSlug != "" {
			return c.PresetSlug
		}
		return "OpenAI-compatible"
	}
	return string(c.Provider)
}

func pickerEfforts(c models.LLMConfig) string {
	return strings.Join(models.ConversationEfforts(c), ",")
}
func pickerDefaultEffort(c models.LLMConfig) string {
	if models.ValidConversationEffort(c, c.ReasoningEffort) {
		return c.ReasoningEffort
	}
	if spec, ok := models.LookupModel(c.Provider, c.Model); ok {
		return spec.DefaultReasoningEffort
	}
	return ""
}
