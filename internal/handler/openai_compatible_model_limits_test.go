package handler

import (
	"strings"
	"testing"
)

func TestDecodeOpenAICompatibleModelsIncludesLimits(t *testing.T) {
	body := strings.NewReader(`{"data":[{"id":"vllm-model","max_model_len":8192,"max_tokens":4096}]}`)
	models, err := decodeOpenAICompatibleModels(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "vllm-model" {
		t.Fatalf("models = %+v", models)
	}
	if models[0].ContextLength != 8192 || models[0].MaxOutputTokens != 4096 {
		t.Fatalf("limits = context %d output %d", models[0].ContextLength, models[0].MaxOutputTokens)
	}
}
