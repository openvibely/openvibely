package pages

import (
	"encoding/json"
	"testing"
)

func TestGoogleHostedModelOptions(t *testing.T) {
	var options map[string][]struct {
		Value     string `json:"value"`
		OAuth     bool   `json:"oauth"`
		OAuthOnly bool   `json:"oauthOnly"`
	}
	if err := json.Unmarshal([]byte(builtInModelOptionsJSON()), &options); err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, option := range options["gemini"] {
		if option.OAuthOnly {
			count++
			if !option.OAuth {
				t.Fatalf("%s unavailable for OAuth", option.Value)
			}
		}
	}
	if count != 7 {
		t.Fatalf("got %d subscription-only model variants, want 7", count)
	}
}
