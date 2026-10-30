package openaiclient

import "testing"

func TestServiceTierPayloads(t *testing.T) {
	for _, model := range []string{"gpt-6-astra", "gpt-6.1-sol", "gpt-6.1-terra"} {
		for _, tier := range []string{"", "default", "priority", "invalid"} {
			for _, transport := range []string{"http", "standard_websocket", "lite_websocket"} {
				t.Run(model+"/"+tier+"/"+transport, func(t *testing.T) {
					payload := map[string]any{"model": model, "input": []any{}}
					applyServiceTier(payload, model, tier)
					switch transport {
					case "standard_websocket":
						payload = buildStandardResponsesWebsocketPayload(payload)
					case "lite_websocket":
						payload = buildResponsesLiteWebsocketPayload(payload, "", "fixture")
					}
					want := tier == "default" || tier == "priority"
					value, present := payload["service_tier"]
					if present != want || (want && value != tier) {
						t.Fatalf("tier=%v present=%v", value, present)
					}
				})
			}
		}
	}
	payload := map[string]any{}
	applyServiceTier(payload, "custom-model", "priority")
	if _, ok := payload["service_tier"]; ok {
		t.Fatal("unsupported model received tier")
	}
}
