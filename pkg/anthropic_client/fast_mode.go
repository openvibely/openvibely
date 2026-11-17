package anthropicclient

import "github.com/openvibely/openvibely/internal/models"

// FastModeBetaHeader enables the Messages API speed feature.
const FastModeBetaHeader = "fast-mode-2026-02-01"

// The shared setting uses priority/default for persistence. Anthropic's wire
// format uses speed=fast; standard processing omits the speed override.
// Never send OpenAI's service_tier field to Anthropic.
func fastModeSpeed(model, serviceTier string) string {
	if serviceTier == "priority" && models.ModelSupportsFastMode(models.ProviderAnthropic, model) {
		return "fast"
	}
	return ""
}
