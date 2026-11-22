package models

import "testing"

func TestGoogleHostedModelsRequireOAuth(t *testing.T) {
	ids := []string{"claude-sonnet-5-5-low", "claude-sonnet-5-5-medium", "claude-sonnet-5-5-high", "claude-opus-5-5-low", "claude-opus-5-5-medium", "claude-opus-5-5-high", "gpt-oss-120b-medium"}
	for _, id := range ids {
		t.Run(id, func(t *testing.T) {
			if !BuiltInModelSupportedForAuth(ProviderGemini, id, AuthMethodOAuth) {
				t.Fatal("OAuth model unavailable")
			}
			for _, auth := range []AuthMethod{AuthMethodAPIKey, ""} {
				if BuiltInModelSupportedForAuth(ProviderGemini, id, auth) {
					t.Fatal("subscription model available without OAuth")
				}
			}
			spec, _ := LookupModel(ProviderGemini, id)
			if spec.SupportsWebSearch {
				t.Fatal("unverified Google grounding enabled")
			}
		})
	}
	if !BuiltInModelSupportedForAuth(ProviderGemini, "gemini-4-argon", AuthMethodAPIKey) {
		t.Fatal("regular Gemini API-key support changed")
	}
}
