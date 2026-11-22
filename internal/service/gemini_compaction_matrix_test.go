package service

import (
	"github.com/openvibely/openvibely/internal/models"
	geminiclient "github.com/openvibely/openvibely/pkg/gemini_client"
)

func configureGeminiCompactionServer(svc *LLMService, url string) {
	svc.providerAdapters[models.ProviderGemini].(*geminiProviderAdapter).adapter.SetClientConfigurer(func(c *geminiclient.Client) { c.SetBaseURL(url) })
}
