package pages

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/openvibely/openvibely/internal/models"
)

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
