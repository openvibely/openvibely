package persistence

import (
	"errors"
	"github.com/openvibely/openvibely/internal/models"
)

var (
	ErrLLMConfigNameRequired  = errors.New("Model name is required")
	ErrLLMConfigNameDuplicate = errors.New("A model with that name already exists")
	ErrLLMConfigModelRequired = errors.New("Model identifier is required")
)

type UnrefreshableOpenAIOAuth struct {
	models.LLMConfig
	PrincipalHash     string
	PrincipalVerified bool
}

type OAuthProviderPresence struct {
	AnyOAuth       bool
	AnthropicOAuth bool
	OpenAIOAuth    bool
}

// ListCardsPage returns one bounded, ordered Models-page projection. Search
// matches the same visible card metadata as the browser card-search helper.
type ModelCardListFilter struct {
	Search     string
	Provider   string
	Default    *bool
	AuthStatus string
	Kind       string
	Sort       string
}
