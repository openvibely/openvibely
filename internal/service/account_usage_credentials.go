package service

import (
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

// accountUsageCredentials walks account configurations in their existing order,
// skipping failed credential owners and accounts already served successfully.
// Callers supply only the account group(s) they are authorized to operate on.
type accountUsageCredentials struct {
	configs        []models.LLMConfig
	provider       string
	failedOwners   map[string]bool
	servedAccounts map[string]bool
}

func newAccountUsageCredentials(configs []models.LLMConfig, provider string) *accountUsageCredentials {
	return &accountUsageCredentials{configs: configs, provider: provider, failedOwners: map[string]bool{}, servedAccounts: map[string]bool{}}
}

func (c *accountUsageCredentials) next() (models.LLMConfig, bool) {
	for len(c.configs) > 0 {
		cfg := c.configs[0]
		c.configs = c.configs[1:]
		if cfg.AuthMethod != models.AuthMethodOAuth || strings.TrimSpace(cfg.OAuthAccessToken) == "" {
			continue
		}
		if cfg.Provider != models.ProviderOpenAI && cfg.Provider != models.ProviderAnthropic {
			continue
		}
		if c.provider != "" && string(cfg.Provider) != c.provider {
			continue
		}
		if c.failedOwners[accountUsageCredentialOwnerKeyForConfig(cfg)] || c.servedAccounts[accountUsageKeyForConfig(cfg)] {
			continue
		}
		return cfg, true
	}
	return models.LLMConfig{}, false
}

func (c *accountUsageCredentials) failed(cfg models.LLMConfig) {
	c.failedOwners[accountUsageCredentialOwnerKeyForConfig(cfg)] = true
}
func (c *accountUsageCredentials) succeeded(cfg models.LLMConfig) {
	c.servedAccounts[accountUsageKeyForConfig(cfg)] = true
}
