package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type LLMConfigRepo struct {
	db *sql.DB
}

var (
	ErrLLMConfigNameRequired  = errors.New("Model name is required")
	ErrLLMConfigNameDuplicate = errors.New("A model with that name already exists")
	ErrLLMConfigModelRequired = errors.New("Model identifier is required")
)

func NewLLMConfigRepo(db *sql.DB) *LLMConfigRepo {
	return &LLMConfigRepo{db: db}
}

const llmConfigColumns = `a.id, a.name, a.provider, a.model, a.reasoning_effort, a.api_key, a.max_tokens, a.temperature, a.is_default, a.created_at, a.updated_at, a.auth_method,
	a.oauth_access_token, a.oauth_refresh_token, a.oauth_expires_at, a.oauth_account_id, a.oauth_needs_reauth,
	a.max_workers, a.worker_timeout, a.oauth_client_id, a.oauth_client_secret, a.oauth_authorize_url, a.oauth_token_url, a.oauth_scopes, a.ollama_base_url, a.base_url, a.transport, a.preset_slug, a.models_url, a.auth_header_name, a.auth_header_value_prefix, a.extra_headers_json, a.extra_body_json, a.default_max_tokens, a.context_window, a.compaction_threshold, a.token_exchange_format, a.token_refresh_format, a.custom_auth_config_json, a.custom_auth_state_json, a.oauth_config_revision, a.mixture_config_json, a.auto_start_tasks,
	COALESCE(a.oauth_connection_id, ''), COALESCE(c.name, ''), COALESCE(c.provider, ''),
	COALESCE(c.oauth_access_token, ''), COALESCE(c.oauth_refresh_token, ''), COALESCE(c.oauth_expires_at, 0),
	COALESCE(c.oauth_account_id, ''), COALESCE(c.oauth_needs_reauth, 1), COALESCE(c.oauth_revision, -1)`

// llmConfigCardColumns is the bounded Models-page projection. Credential bodies,
// edit-only endpoint settings, request JSON, custom-auth JSON, and full mixture
// JSON are deliberately excluded from the initial response path.
const llmConfigCardColumns = `id, name, provider, model, reasoning_effort,
			CASE WHEN api_key != '' THEN 1 ELSE 0 END, temperature, is_default, auth_method,
			CASE WHEN COALESCE(oauth_connection_id, '') != ''
				THEN EXISTS(SELECT 1 FROM oauth_connections c WHERE c.id = oauth_connection_id AND c.oauth_access_token != '')
				ELSE oauth_access_token != '' END,
			CASE WHEN COALESCE(oauth_connection_id, '') != ''
				THEN COALESCE((SELECT c.oauth_expires_at FROM oauth_connections c WHERE c.id = oauth_connection_id), 0)
				ELSE oauth_expires_at END,
			CASE WHEN COALESCE(oauth_connection_id, '') != ''
				THEN COALESCE((SELECT c.oauth_needs_reauth FROM oauth_connections c WHERE c.id = oauth_connection_id), 1)
				ELSE oauth_needs_reauth END,
			max_workers, worker_timeout, substr(ollama_base_url, 1, 512), substr(base_url, 1, 512),
			CASE WHEN json_valid(mixture_config_json) THEN substr(COALESCE(json_extract(mixture_config_json, '$.aggregator.agent_config_id'), ''), 1, 128) ELSE '' END,
			CASE WHEN json_valid(mixture_config_json) THEN substr(COALESCE(json_extract(mixture_config_json, '$.aggregator.label'), ''), 1, 256) ELSE '' END,
			CASE WHEN json_valid(mixture_config_json) AND json_type(mixture_config_json, '$.reference_models') = 'array' THEN json_array_length(mixture_config_json, '$.reference_models') ELSE 0 END,
			COALESCE(oauth_connection_id, ''),
			COALESCE((SELECT c.name FROM oauth_connections c WHERE c.id = oauth_connection_id), '')`

// llmConfigPickerColumns is the render-only model picker projection for Chat
// and Agent dialogs. It deliberately excludes provider identity, credentials,
// endpoint settings, request JSON, custom-auth state, worker fields, timestamps,
// and mixture definitions.
const llmConfigPickerColumns = `id, name, model`

// llmConfigChatSelectionColumns is the compact API Chat auto-selection and
// prompt-context projection. It preserves model identity, provider display, and
// default-marker semantics while excluding credentials and large provider JSON.
const llmConfigChatSelectionColumns = `id, name, provider, model, is_default`

// llmConfigVisionSelectionColumns is the compact image-routing projection. It
// preserves only the fields used by SelectLLMWithVision and non-secret
// credential-presence sentinels needed to exclude legacy Anthropic CLI rows.
const llmConfigVisionSelectionColumns = `id, name, provider, model, auth_method, is_default,
			CASE WHEN COALESCE(api_key, '') != '' THEN 1 ELSE 0 END,
			CASE WHEN COALESCE(oauth_connection_id, '') != ''
				THEN EXISTS(SELECT 1 FROM oauth_connections c WHERE c.id = oauth_connection_id AND c.oauth_access_token != '')
				ELSE COALESCE(oauth_access_token, '') != '' END`

// llmConfigTaskCreationSelectionColumns is the compact runtime create_task
// selection/category projection. It adds auto_start_tasks to the Chat selection
// fields while deliberately excluding credentials, endpoint settings, request
// JSON, custom-auth state, and full mixture definitions.
const llmConfigTaskCreationSelectionColumns = `id, name, provider, model, is_default, auto_start_tasks`

// llmConfigBadgeColumns is the minimal projection for task-card model badges
// and the chat-thread composer label. The model slug is included because the
// thread composer renders "Name (model)" labels; id, name, and is_default are
// needed to resolve the badge and default-model fallback on task cards.
// All credential, OAuth, request-JSON, and large-blob columns are excluded.
const llmConfigBadgeColumns = `id, name, model, is_default`

// llmConfigRuntimeSummaryColumns is the compact read-only Chat model/status
// projection. It deliberately excludes credentials, OAuth token bodies, endpoint
// URLs, provider request JSON, custom-auth JSON, and full mixture definitions.
// WorkerTimeout is included because view_settings displays it for configured
// per-model worker pools.
const llmConfigRuntimeSummaryColumns = `id, name, provider, model, is_default, auth_method, max_workers, worker_timeout`

// llmConfigWorkerCapacityColumns is the bounded worker-capacity projection. It
// deliberately excludes credentials, OAuth token bodies, endpoint URLs, provider
// request JSON, custom-auth JSON/state, and full mixture definitions.
const llmConfigWorkerCapacityColumns = `id, name, model, max_workers`

func scanLLMConfig(row interface{ Scan(dest ...any) error }, a *models.LLMConfig) error {
	var connectionProvider models.LLMProvider
	var connectionAccessToken, connectionRefreshToken, connectionAccountID string
	var connectionExpiresAt, connectionRevision int64
	var connectionNeedsReauth bool
	if err := row.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.ReasoningEffort, &a.APIKey,
		&a.MaxTokens, &a.Temperature, &a.IsDefault, &a.CreatedAt, &a.UpdatedAt,
		&a.AuthMethod, &a.OAuthAccessToken, &a.OAuthRefreshToken, &a.OAuthExpiresAt,
		&a.OAuthAccountID, &a.OAuthNeedsReauth,
		&a.MaxWorkers, &a.WorkerTimeout,
		&a.OAuthClientID, &a.OAuthClientSecret, &a.OAuthAuthorizeURL, &a.OAuthTokenURL, &a.OAuthScopes,
		&a.OllamaBaseURL, &a.BaseURL, &a.Transport, &a.PresetSlug, &a.ModelsURL,
		&a.AuthHeaderName, &a.AuthHeaderValuePrefix, &a.ExtraHeadersJSON, &a.ExtraBodyJSON,
		&a.DefaultMaxTokens, &a.ContextWindow, &a.CompactionThreshold, &a.TokenExchangeFormat, &a.TokenRefreshFormat, &a.CustomAuthConfigJSON, &a.CustomAuthStateJSON, &a.OAuthConfigRevision, &a.MixtureConfigJSON, &a.AutoStartTasks,
		&a.OAuthConnectionID, &a.OAuthConnectionName, &connectionProvider,
		&connectionAccessToken, &connectionRefreshToken, &connectionExpiresAt,
		&connectionAccountID, &connectionNeedsReauth, &connectionRevision); err != nil {
		return err
	}
	if a.OAuthConnectionID == "" {
		return nil
	}
	if a.AuthMethod != models.AuthMethodOAuth || connectionProvider != a.Provider {
		a.OAuthAccessToken = ""
		a.OAuthRefreshToken = ""
		a.OAuthExpiresAt = 0
		a.OAuthAccountID = ""
		a.OAuthNeedsReauth = true
		a.OAuthConfigRevision = -1
		a.OAuthConnectionName = ""
		return nil
	}
	a.OAuthAccessToken = connectionAccessToken
	a.OAuthRefreshToken = connectionRefreshToken
	a.OAuthExpiresAt = connectionExpiresAt
	a.OAuthAccountID = connectionAccountID
	a.OAuthNeedsReauth = connectionNeedsReauth
	a.OAuthConfigRevision = connectionRevision
	return nil
}

func normalizeLLMConfigName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", ErrLLMConfigNameRequired
	}
	return name, nil
}

func validateLLMConfigModel(a *models.LLMConfig) error {
	switch a.Provider {
	case models.ProviderAnthropic, models.ProviderOpenAI, models.ProviderOpenAICompatible, models.ProviderOllama:
		if strings.TrimSpace(a.Model) == "" {
			return ErrLLMConfigModelRequired
		}
	}
	return nil
}

func validateLLMConfigNameAvailableTx(ctx context.Context, tx SQLExecutor, name, excludeID string) (string, error) {
	normalized, err := normalizeLLMConfigName(name)
	if err != nil {
		return "", err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, name FROM agent_configs`)
	if err != nil {
		return "", fmt.Errorf("checking model config name: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, existingName string
		if err := rows.Scan(&id, &existingName); err != nil {
			return "", fmt.Errorf("scanning model config name: %w", err)
		}
		if id != excludeID && strings.EqualFold(strings.TrimSpace(existingName), normalized) {
			return "", ErrLLMConfigNameDuplicate
		}
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("checking model config names: %w", err)
	}
	return normalized, nil
}

func (r *LLMConfigRepo) ValidateNameAvailable(ctx context.Context, name, excludeID string) (string, error) {
	return validateLLMConfigNameAvailableTx(ctx, r.db, name, excludeID)
}

func (r *LLMConfigRepo) List(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigColumns+`
			 FROM agent_configs a
			 LEFT JOIN oauth_connections c ON c.id = a.oauth_connection_id AND c.provider = a.provider AND a.auth_method = ?
		 ORDER BY a.is_default DESC, a.name ASC`, models.AuthMethodOAuth)
	if err != nil {
		return nil, fmt.Errorf("listing models: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := scanLLMConfig(rows, &a); err != nil {
			return nil, fmt.Errorf("scanning model config: %w", err)
		}
		configs = append(configs, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return configs, nil
}

func (r *LLMConfigRepo) ListRefreshableOAuth(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT a.id, a.name, a.provider, a.model, a.auth_method,
		        c.oauth_access_token, c.oauth_refresh_token, c.oauth_expires_at,
		        c.oauth_account_id, c.oauth_needs_reauth, c.oauth_revision, c.id
		 FROM oauth_connections c
		 JOIN agent_configs a ON a.id = (
		   SELECT linked.id FROM agent_configs linked
		   WHERE linked.oauth_connection_id = c.id AND linked.auth_method = ? AND linked.provider = c.provider
		   ORDER BY linked.id LIMIT 1
		 )
		 WHERE c.provider IN (?, ?)
		   AND c.oauth_access_token != '' AND c.oauth_refresh_token != '' AND c.oauth_needs_reauth = 0
		 ORDER BY c.oauth_expires_at ASC, c.id ASC`,
		models.AuthMethodOAuth, models.ProviderOpenAI, models.ProviderAnthropic)
	if err != nil {
		return nil, fmt.Errorf("listing refreshable OAuth connections: %w", err)
	}
	defer rows.Close()
	var configs []models.LLMConfig
	for rows.Next() {
		var cfg models.LLMConfig
		if err := rows.Scan(&cfg.ID, &cfg.Name, &cfg.Provider, &cfg.Model, &cfg.AuthMethod,
			&cfg.OAuthAccessToken, &cfg.OAuthRefreshToken, &cfg.OAuthExpiresAt,
			&cfg.OAuthAccountID, &cfg.OAuthNeedsReauth, &cfg.OAuthConfigRevision, &cfg.OAuthConnectionID); err != nil {
			return nil, fmt.Errorf("scanning refreshable OAuth connection: %w", err)
		}
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

// ListUnrefreshableOpenAIOAuth returns one representative model for legacy
// Codex connections whose stored access JWT may still identify their owner.
func (r *LLMConfigRepo) ListUnrefreshableOpenAIOAuth(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT a.id, a.name, a.provider, a.model, a.auth_method,
		        c.oauth_access_token, c.oauth_refresh_token, c.oauth_expires_at,
		        c.oauth_account_id, c.oauth_needs_reauth, c.oauth_revision, c.id
		 FROM oauth_connections c
		 JOIN agent_configs a ON a.id = (
		   SELECT linked.id FROM agent_configs linked
		   WHERE linked.oauth_connection_id = c.id AND linked.auth_method = ? AND linked.provider = c.provider
		   ORDER BY linked.id LIMIT 1
		 )
		 WHERE c.provider = ? AND c.oauth_access_token != ''
		   AND (c.oauth_needs_reauth = 1 OR c.oauth_refresh_token = '' OR
		        (c.oauth_expires_at > 0 AND c.oauth_expires_at <= CAST(strftime('%s', 'now') AS INTEGER) * 1000))
		 ORDER BY c.id`, models.AuthMethodOAuth, models.ProviderOpenAI)
	if err != nil {
		return nil, fmt.Errorf("listing unrefreshable OpenAI OAuth connections: %w", err)
	}
	defer rows.Close()
	var configs []models.LLMConfig
	for rows.Next() {
		var cfg models.LLMConfig
		if err := rows.Scan(&cfg.ID, &cfg.Name, &cfg.Provider, &cfg.Model, &cfg.AuthMethod,
			&cfg.OAuthAccessToken, &cfg.OAuthRefreshToken, &cfg.OAuthExpiresAt,
			&cfg.OAuthAccountID, &cfg.OAuthNeedsReauth, &cfg.OAuthConfigRevision, &cfg.OAuthConnectionID); err != nil {
			return nil, fmt.Errorf("scanning unrefreshable OpenAI OAuth connection: %w", err)
		}
		configs = append(configs, cfg)
	}
	return configs, rows.Err()
}

func (r *LLMConfigRepo) HasAny(ctx context.Context) (bool, error) {
	var exists bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM agent_configs)`).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("checking model configs exist: %w", err)
	}
	return exists, nil
}

// ListCards returns the bounded configuration needed to render the Models page.
// Boolean credential-presence expressions are represented as non-secret
// sentinels so existing card status helpers retain their semantics.
func (r *LLMConfigRepo) ListCards(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigCardColumns+`
						 FROM agent_configs ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing model cards: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var (
			a           models.LLMConfig
			hasAPIKey   bool
			hasOAuthKey bool
		)
		if err := rows.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.ReasoningEffort,
			&hasAPIKey, &a.Temperature, &a.IsDefault, &a.AuthMethod,
			&hasOAuthKey, &a.OAuthExpiresAt, &a.OAuthNeedsReauth, &a.MaxWorkers, &a.WorkerTimeout,
			&a.OllamaBaseURL, &a.BaseURL, &a.MixtureAggregatorID,
			&a.MixtureAggregatorLabel, &a.MixtureReferenceCount, &a.OAuthConnectionID, &a.OAuthConnectionName); err != nil {
			return nil, fmt.Errorf("scanning model card: %w", err)
		}
		if hasAPIKey {
			a.APIKey = "present"
		}
		if hasOAuthKey {
			a.OAuthAccessToken = "present"
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
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

func (r *LLMConfigRepo) ListCardsPage(ctx context.Context, limit, offset int, search string) ([]models.LLMConfig, error) {
	return r.ListCardsPageFiltered(ctx, limit, offset, ModelCardListFilter{Search: search})
}

func (r *LLMConfigRepo) ListCardsPageFiltered(ctx context.Context, limit, offset int, filter ModelCardListFilter) ([]models.LLMConfig, error) {
	limit, offset = normalizeCardPageArgs(limit, offset)
	query := `SELECT ` + llmConfigCardColumns + ` FROM agent_configs AS configs WHERE 1=1`
	args := make([]any, 0, 8)
	if search := strings.TrimSpace(filter.Search); search != "" {
		query += ` AND INSTR(LOWER(
			COALESCE(name, '') || ' ' || COALESCE(provider, '') || ' ' ||
			COALESCE(model, '') || ' ' ||
			CASE WHEN is_default = 1 THEN 'default' ELSE 'active' END || ' ' ||
				CASE WHEN auth_method = 'oauth' AND (
					(provider IN ('anthropic', 'openai') AND EXISTS(
						SELECT 1 FROM oauth_connections c WHERE c.id = oauth_connection_id
						AND c.provider = configs.provider AND c.oauth_needs_reauth = 0 AND c.oauth_access_token != ''
						AND c.oauth_expires_at > CAST(strftime('%s', 'now') AS INTEGER) * 1000
					)) OR
					(provider = 'openai_compatible' AND COALESCE(oauth_needs_reauth, 0) = 0 AND COALESCE(oauth_access_token, '') != '' AND (
						COALESCE(oauth_expires_at, 0) = 0 OR COALESCE(oauth_expires_at, 0) > CAST(strftime('%s', 'now') AS INTEGER) * 1000
					))
				) THEN 'connected'
				ELSE CASE WHEN auth_method = 'oauth' AND provider IN ('anthropic', 'openai', 'openai_compatible') THEN 'not connected' ELSE '' END END
		), ?) > 0`
		args = append(args, strings.ToLower(search))
	}
	if filter.Provider != "" {
		query += ` AND provider = ?`
		args = append(args, filter.Provider)
	}
	if filter.Default != nil {
		query += ` AND is_default = ?`
		args = append(args, *filter.Default)
	}
	switch filter.AuthStatus {
	case "connected":
		query += ` AND auth_method = 'oauth' AND (
			(provider IN ('anthropic', 'openai') AND EXISTS(
				SELECT 1 FROM oauth_connections c WHERE c.id = oauth_connection_id
				AND c.provider = configs.provider AND c.oauth_needs_reauth = 0 AND c.oauth_access_token != ''
				AND c.oauth_expires_at > CAST(strftime('%s', 'now') AS INTEGER) * 1000
			)) OR
			(provider = 'openai_compatible' AND COALESCE(oauth_needs_reauth, 0) = 0 AND COALESCE(oauth_access_token, '') != ''
				AND (COALESCE(oauth_expires_at, 0) = 0 OR COALESCE(oauth_expires_at, 0) > CAST(strftime('%s', 'now') AS INTEGER) * 1000))
		)`
	case "not_connected":
		query += ` AND auth_method = 'oauth' AND provider IN ('anthropic', 'openai', 'openai_compatible') AND NOT (
			(provider IN ('anthropic', 'openai') AND EXISTS(
				SELECT 1 FROM oauth_connections c WHERE c.id = oauth_connection_id
				AND c.provider = configs.provider AND c.oauth_needs_reauth = 0 AND c.oauth_access_token != ''
				AND c.oauth_expires_at > CAST(strftime('%s', 'now') AS INTEGER) * 1000
			)) OR
			(provider = 'openai_compatible' AND COALESCE(oauth_needs_reauth, 0) = 0 AND COALESCE(oauth_access_token, '') != ''
				AND (COALESCE(oauth_expires_at, 0) = 0 OR COALESCE(oauth_expires_at, 0) > CAST(strftime('%s', 'now') AS INTEGER) * 1000))
		)`
	case "not_required":
		query += ` AND (auth_method != 'oauth' OR provider NOT IN ('anthropic', 'openai', 'openai_compatible'))`
	}
	if filter.Kind == "mixture" {
		query += ` AND provider = 'mixture'`
	} else if filter.Kind == "direct" {
		query += ` AND provider != 'mixture'`
	}
	switch filter.Sort {
	case "name_asc":
		query += ` ORDER BY name COLLATE NOCASE ASC, name ASC, id ASC`
	case "name_desc":
		query += ` ORDER BY name COLLATE NOCASE DESC, name DESC, id DESC`
	case "provider":
		query += ` ORDER BY provider COLLATE NOCASE ASC, provider ASC, name COLLATE NOCASE ASC, name ASC, id ASC`
	default:
		query += ` ORDER BY name COLLATE NOCASE ASC, name ASC, id ASC`
	}
	query += ` LIMIT ? OFFSET ?`
	args = append(args, limit, offset)
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing model card page: %w", err)
	}
	defer rows.Close()

	configs := make([]models.LLMConfig, 0, limit)
	for rows.Next() {
		var (
			a           models.LLMConfig
			hasAPIKey   bool
			hasOAuthKey bool
		)
		if err := rows.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.ReasoningEffort,
			&hasAPIKey, &a.Temperature, &a.IsDefault, &a.AuthMethod,
			&hasOAuthKey, &a.OAuthExpiresAt, &a.OAuthNeedsReauth, &a.MaxWorkers, &a.WorkerTimeout,
			&a.OllamaBaseURL, &a.BaseURL, &a.MixtureAggregatorID,
			&a.MixtureAggregatorLabel, &a.MixtureReferenceCount, &a.OAuthConnectionID, &a.OAuthConnectionName); err != nil {
			return nil, fmt.Errorf("scanning model card page: %w", err)
		}
		if hasAPIKey {
			a.APIKey = "present"
		}
		if hasOAuthKey {
			a.OAuthAccessToken = "present"
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ListModelCardOptions returns the small set of fields needed by Models-page
// dialogs and shared OAuth account actions. It is separate from the paged card
// rows so modal option semantics do not force provider credentials or card
// payloads into every page response.
func (r *LLMConfigRepo) ListModelCardOptions(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, provider, model, is_default, auth_method, COALESCE(oauth_connection_id, '')
		 FROM agent_configs AS configs ORDER BY is_default DESC, name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing model card options: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := rows.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.IsDefault, &a.AuthMethod, &a.OAuthConnectionID); err != nil {
			return nil, fmt.Errorf("scanning model card option: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ListPickerOptions returns only the fields needed to render model picker labels.
// The returned LLMConfig values are intentionally incomplete and must not be used
// for provider execution, model editing, default resolution, or persistence.
func (r *LLMConfigRepo) ListPickerOptions(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigPickerColumns+`
					 FROM agent_configs ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing model picker options: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := rows.Scan(&a.ID, &a.Name, &a.Model); err != nil {
			return nil, fmt.Errorf("scanning model picker option: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ListChatSelectionOptions returns the compact rows needed for API Chat model
// auto-selection and available-model prompt context. The returned LLMConfig
// values are intentionally incomplete and must not be used for provider
// execution, model editing, credential access, or persistence.
func (r *LLMConfigRepo) ListChatSelectionOptions(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigChatSelectionColumns+`
					 FROM agent_configs ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing chat model selection options: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := rows.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.IsDefault); err != nil {
			return nil, fmt.Errorf("scanning chat model selection option: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ListVisionSelectionOptions returns the compact rows needed for image-aware
// model selection. The returned LLMConfig values are intentionally incomplete
// and must not be used for provider execution, model editing, credential access,
// OAuth refresh, or persistence. APIKey and OAuthAccessToken contain only the
// non-secret sentinel "present" when the corresponding credential exists.
func (r *LLMConfigRepo) ListVisionSelectionOptions(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigVisionSelectionColumns+`
					 FROM agent_configs ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing vision model selection options: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var (
			a             models.LLMConfig
			hasAPIKey     bool
			hasOAuthToken bool
		)
		if err := rows.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.AuthMethod, &a.IsDefault, &hasAPIKey, &hasOAuthToken); err != nil {
			return nil, fmt.Errorf("scanning vision model selection option: %w", err)
		}
		if hasAPIKey {
			a.APIKey = "present"
		}
		if hasOAuthToken {
			a.OAuthAccessToken = "present"
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ListTaskCreationSelectionOptions returns the compact rows needed by runtime
// create_task handlers for model auto-selection, default/first fallback, explicit
// model ID preservation, and auto_start_tasks category resolution. The returned
// LLMConfig values are intentionally incomplete and must not be used for provider
// execution, model editing, credential access, OAuth refresh, or persistence.
func (r *LLMConfigRepo) ListTaskCreationSelectionOptions(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigTaskCreationSelectionColumns+`
					 FROM agent_configs ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing task creation model selection options: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := rows.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.IsDefault, &a.AutoStartTasks); err != nil {
			return nil, fmt.Errorf("scanning task creation model selection option: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ListBadgeOptions returns only the four fields needed to render model badges on
// task cards and task detail views (id, name, model, is_default). The returned
// LLMConfig values are intentionally incomplete and must not be used for provider
// execution, model editing, credential access, or persistence.
func (r *LLMConfigRepo) ListBadgeOptions(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigBadgeColumns+`
						 FROM agent_configs ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing model badge options: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := rows.Scan(&a.ID, &a.Name, &a.Model, &a.IsDefault); err != nil {
			return nil, fmt.Errorf("scanning model badge option: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

func scanLLMConfigRuntimeSummary(row interface{ Scan(dest ...any) error }, a *models.LLMConfig) error {
	return row.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.IsDefault, &a.AuthMethod, &a.MaxWorkers, &a.WorkerTimeout)
}

// ListRuntimeSummaries returns only the bounded model fields displayed by
// read-only Chat model/status tools. The returned LLMConfig values are
// intentionally incomplete and must not be used for provider execution, model
// editing, credential access, OAuth refresh, or persistence.
func (r *LLMConfigRepo) ListRuntimeSummaries(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigRuntimeSummaryColumns+`
						 FROM agent_configs ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing runtime model summaries: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := scanLLMConfigRuntimeSummary(rows, &a); err != nil {
			return nil, fmt.Errorf("scanning runtime model summary: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// ListWorkerCapacities returns only the fields needed to render model worker
// pool capacity rows. The returned LLMConfig values are intentionally incomplete
// and must not be used for provider execution, model editing, credential access,
// OAuth refresh, default resolution, or persistence.
func (r *LLMConfigRepo) ListWorkerCapacities(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigWorkerCapacityColumns+`
							 FROM agent_configs
							 WHERE max_workers > 0
							 ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("listing model worker capacities: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		if err := rows.Scan(&a.ID, &a.Name, &a.Model, &a.MaxWorkers); err != nil {
			return nil, fmt.Errorf("scanning model worker capacity: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

// GetRuntimeSummary returns one compact read-only Chat model summary by exact ID
// or case-insensitive normalized name. It uses bounded display columns only and
// avoids hydrating all model configs for targeted get_model requests.
func (r *LLMConfigRepo) GetRuntimeSummary(ctx context.Context, id, name string) (*models.LLMConfig, error) {
	id = strings.TrimSpace(id)
	name = strings.TrimSpace(name)
	if id == "" && name == "" {
		return nil, nil
	}

	var (
		row interface{ Scan(dest ...any) error }
	)
	switch {
	case id != "" && name != "":
		row = r.db.QueryRowContext(ctx,
			`SELECT `+llmConfigRuntimeSummaryColumns+`
				 FROM agent_configs
				 WHERE id = ? OR name = ? COLLATE NOCASE
				 ORDER BY is_default DESC, name ASC
				 LIMIT 1`, id, name)
	case id != "":
		row = r.db.QueryRowContext(ctx,
			`SELECT `+llmConfigRuntimeSummaryColumns+`
				 FROM agent_configs
				 WHERE id = ?
				 LIMIT 1`, id)
	default:
		row = r.db.QueryRowContext(ctx,
			`SELECT `+llmConfigRuntimeSummaryColumns+`
				 FROM agent_configs
				 WHERE name = ? COLLATE NOCASE
				 ORDER BY is_default DESC, name ASC
				 LIMIT 1`, name)
	}

	var a models.LLMConfig
	err := scanLLMConfigRuntimeSummary(row, &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting runtime model summary: %w", err)
	}
	return &a, nil
}

// ListMixtureDefinitions returns only the fields needed to test whether a model
// is referenced by any mixture before delete. It intentionally avoids full
// provider configs and custom-provider request/auth JSON from unrelated rows.
func (r *LLMConfigRepo) ListMixtureDefinitions(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, name, mixture_config_json
				 FROM agent_configs
				 WHERE provider = ? AND mixture_config_json != ''
				 ORDER BY name ASC`, models.ProviderMixture)
	if err != nil {
		return nil, fmt.Errorf("listing mixture model definitions: %w", err)
	}
	defer rows.Close()

	var configs []models.LLMConfig
	for rows.Next() {
		var a models.LLMConfig
		a.Provider = models.ProviderMixture
		if err := rows.Scan(&a.ID, &a.Name, &a.MixtureConfigJSON); err != nil {
			return nil, fmt.Errorf("scanning mixture model definition: %w", err)
		}
		configs = append(configs, a)
	}
	return configs, rows.Err()
}

func (r *LLMConfigRepo) GetByID(ctx context.Context, id string) (*models.LLMConfig, error) {
	var a models.LLMConfig
	err := scanLLMConfig(r.db.QueryRowContext(ctx,
		`SELECT `+llmConfigColumns+`
		 FROM agent_configs a
		 LEFT JOIN oauth_connections c ON c.id = a.oauth_connection_id AND c.provider = a.provider AND a.auth_method = ?
		 WHERE a.id = ?`, models.AuthMethodOAuth, id), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting model config: %w", err)
	}
	return &a, nil
}

func (r *LLMConfigRepo) GetDefault(ctx context.Context) (*models.LLMConfig, error) {
	var a models.LLMConfig
	err := scanLLMConfig(r.db.QueryRowContext(ctx,
		`SELECT `+llmConfigColumns+`
		 FROM agent_configs a
		 LEFT JOIN oauth_connections c ON c.id = a.oauth_connection_id AND c.provider = a.provider AND a.auth_method = ?
		 WHERE a.is_default = 1 LIMIT 1`, models.AuthMethodOAuth), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting default model config: %w", err)
	}
	return &a, nil
}

func (r *LLMConfigRepo) ensureDefaultModelTx(ctx context.Context, tx SQLExecutor) error {
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_configs`).Scan(&total); err != nil {
		return fmt.Errorf("counting model configs: %w", err)
	}
	if total == 0 {
		return nil
	}

	var defaultCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_configs WHERE is_default = 1`).Scan(&defaultCount); err != nil {
		return fmt.Errorf("counting default model configs: %w", err)
	}
	if defaultCount > 0 {
		return nil
	}

	var fallbackID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM agent_configs ORDER BY created_at ASC, name ASC LIMIT 1`).Scan(&fallbackID); err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("selecting fallback default model: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET is_default = 1, updated_at = datetime('now') WHERE id = ?`, fallbackID); err != nil {
		return fmt.Errorf("setting fallback default model: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) deleteWithTx(ctx context.Context, tx SQLExecutor, id string) error {
	// Nullify FK references in tasks and executions before deleting. Agent model
	// overrides are legacy string references, so reset them in the same transaction
	// rather than leaving a protected or reusable Agent pointed at a deleted config.
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET agent_id = NULL WHERE agent_id = ?`, id); err != nil {
		return fmt.Errorf("nullifying model config in tasks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE executions SET agent_config_id = NULL WHERE agent_config_id = ?`, id); err != nil {
		return fmt.Errorf("nullifying model config in executions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agents SET model = 'inherit', updated_at = datetime('now') WHERE model = ?`, id); err != nil {
		return fmt.Errorf("resetting agent model overrides: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_configs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting model config: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) Create(ctx context.Context, a *models.LLMConfig) error {
	if err := validateLLMConfigModel(a); err != nil {
		return err
	}

	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("begin create model config tx: %w", err)
	}
	defer cleanup()

	name, err := validateLLMConfigNameAvailableTx(ctx, tx, a.Name, "")
	if err != nil {
		return err
	}
	a.Name = name

	if !a.IsDefault {
		var existingCount int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_configs`).Scan(&existingCount); err != nil {
			return fmt.Errorf("counting existing model configs: %w", err)
		}
		if existingCount == 0 {
			a.IsDefault = true
		}
	}

	// If this is set as default, unset others first.
	if a.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET is_default = 0`); err != nil {
			return fmt.Errorf("unsetting defaults: %w", err)
		}
	}
	if a.AuthMethod == "" {
		a.AuthMethod = models.AuthMethodAPIKey
	}
	if a.AuthMethod == models.AuthMethodOAuth && (a.Provider == models.ProviderOpenAI || a.Provider == models.ProviderAnthropic) {
		if a.OAuthConnectionID == "" {
			if err := createPrivateOAuthConnectionTx(ctx, tx, a); err != nil {
				return fmt.Errorf("creating private OAuth connection: %w", err)
			}
		} else if err := validateOAuthConnectionLinkTx(ctx, tx, a.OAuthConnectionID, a.Provider); err != nil {
			return err
		}
	} else {
		a.OAuthConnectionID = ""
	}
	legacyAccessToken, legacyRefreshToken := a.OAuthAccessToken, a.OAuthRefreshToken
	legacyExpiresAt, legacyAccountID := a.OAuthExpiresAt, a.OAuthAccountID
	if a.OAuthConnectionID != "" {
		legacyAccessToken, legacyRefreshToken = "", ""
		legacyExpiresAt, legacyAccountID = 0, ""
	}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO agent_configs (id, name, provider, model, reasoning_effort, api_key, max_tokens, temperature, is_default, auth_method, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_account_id, max_workers, worker_timeout, oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url, base_url, transport, preset_slug, models_url, auth_header_name, auth_header_value_prefix, extra_headers_json, extra_body_json, default_max_tokens, context_window, compaction_threshold, token_exchange_format, token_refresh_format, custom_auth_config_json, custom_auth_state_json, mixture_config_json, auto_start_tasks, oauth_connection_id)
			 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 RETURNING id, created_at, updated_at`,
		a.Name, a.Provider, a.Model, a.ReasoningEffort, a.APIKey, a.MaxTokens, a.Temperature, a.IsDefault,
		a.AuthMethod, legacyAccessToken, legacyRefreshToken, legacyExpiresAt, legacyAccountID, a.MaxWorkers, a.WorkerTimeout,
		a.OAuthClientID, a.OAuthClientSecret, a.OAuthAuthorizeURL, a.OAuthTokenURL, a.OAuthScopes,
		a.OllamaBaseURL, a.BaseURL, a.Transport, a.PresetSlug, a.ModelsURL,
		a.AuthHeaderName, a.AuthHeaderValuePrefix, a.ExtraHeadersJSON, a.ExtraBodyJSON,
		a.DefaultMaxTokens, a.ContextWindow, a.CompactionThreshold, a.TokenExchangeFormat, a.TokenRefreshFormat, a.CustomAuthConfigJSON, a.CustomAuthStateJSON, a.MixtureConfigJSON, a.AutoStartTasks, nullStringArg(a.OAuthConnectionID)).
		Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return fmt.Errorf("creating model config: %w", err)
	}
	if err := r.ensureDefaultModelTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit create model config tx: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) Update(ctx context.Context, a *models.LLMConfig) error {
	if err := validateLLMConfigModel(a); err != nil {
		return err
	}

	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("begin update model config tx: %w", err)
	}
	defer cleanup()

	name, err := validateLLMConfigNameAvailableTx(ctx, tx, a.Name, a.ID)
	if err != nil {
		return err
	}
	a.Name = name

	if a.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET is_default = 0`); err != nil {
			return fmt.Errorf("unsetting defaults: %w", err)
		}
	}
	if a.AuthMethod == "" {
		a.AuthMethod = models.AuthMethodAPIKey
	}
	if a.AuthMethod == models.AuthMethodOAuth && (a.Provider == models.ProviderOpenAI || a.Provider == models.ProviderAnthropic) {
		if a.OAuthConnectionID != "" {
			var currentProvider models.LLMProvider
			var currentConnectionID string
			if err := tx.QueryRowContext(ctx, `SELECT provider, COALESCE(oauth_connection_id, '') FROM agent_configs WHERE id = ?`, a.ID).Scan(&currentProvider, &currentConnectionID); err != nil {
				return fmt.Errorf("loading current OAuth connection: %w", err)
			}
			if currentProvider != a.Provider && currentConnectionID == a.OAuthConnectionID {
				a.OAuthConnectionID = ""
			}
		}
		if a.OAuthConnectionID == "" {
			if err := createPrivateOAuthConnectionTx(ctx, tx, a); err != nil {
				return fmt.Errorf("creating private OAuth connection: %w", err)
			}
		} else if err := validateOAuthConnectionLinkTx(ctx, tx, a.OAuthConnectionID, a.Provider); err != nil {
			return err
		}
	} else {
		a.OAuthConnectionID = ""
	}
	legacyAccessToken, legacyRefreshToken := a.OAuthAccessToken, a.OAuthRefreshToken
	legacyExpiresAt, legacyAccountID, legacyNeedsReauth := a.OAuthExpiresAt, a.OAuthAccountID, a.OAuthNeedsReauth
	if a.OAuthConnectionID != "" {
		legacyAccessToken, legacyRefreshToken = "", ""
		legacyExpiresAt, legacyAccountID, legacyNeedsReauth = 0, "", false
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE agent_configs SET name = ?, provider = ?, model = ?, reasoning_effort = ?, api_key = ?,
		 max_tokens = ?, temperature = ?, is_default = ?,
			 auth_method = ?, oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?, oauth_needs_reauth = ?,
		 max_workers = ?, worker_timeout = ?,
		 oauth_client_id = ?, oauth_client_secret = ?, oauth_authorize_url = ?, oauth_token_url = ?, oauth_scopes = ?,
		 ollama_base_url = ?, base_url = ?, transport = ?, preset_slug = ?, models_url = ?,
		 auth_header_name = ?, auth_header_value_prefix = ?, extra_headers_json = ?, extra_body_json = ?,
		 default_max_tokens = ?, context_window = ?, compaction_threshold = ?, token_exchange_format = ?, token_refresh_format = ?, custom_auth_config_json = ?, custom_auth_state_json = ?, mixture_config_json = ?, auto_start_tasks = ?,
		 oauth_connection_id = ?,
		 oauth_config_revision = oauth_config_revision + 1,
		 updated_at = datetime('now')
		 WHERE id = ?`,
		a.Name, a.Provider, a.Model, a.ReasoningEffort, a.APIKey, a.MaxTokens, a.Temperature, a.IsDefault,
		a.AuthMethod, legacyAccessToken, legacyRefreshToken, legacyExpiresAt, legacyAccountID, legacyNeedsReauth,
		a.MaxWorkers, a.WorkerTimeout,
		a.OAuthClientID, a.OAuthClientSecret, a.OAuthAuthorizeURL, a.OAuthTokenURL, a.OAuthScopes,
		a.OllamaBaseURL, a.BaseURL, a.Transport, a.PresetSlug, a.ModelsURL,
		a.AuthHeaderName, a.AuthHeaderValuePrefix, a.ExtraHeadersJSON, a.ExtraBodyJSON,
		a.DefaultMaxTokens, a.ContextWindow, a.CompactionThreshold, a.TokenExchangeFormat, a.TokenRefreshFormat, a.CustomAuthConfigJSON, a.CustomAuthStateJSON, a.MixtureConfigJSON, a.AutoStartTasks,
		nullStringArg(a.OAuthConnectionID), a.ID)
	if err != nil {
		return fmt.Errorf("updating model config: %w", err)
	}
	if err := r.ensureDefaultModelTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update model config tx: %w", err)
	}
	if a.OAuthConnectionID == "" {
		a.OAuthConfigRevision++
	}
	return nil
}

// UpdateOAuthTokens updates the owning OAuth credential record for a config.
func (r *LLMConfigRepo) UpdateOAuthTokens(ctx context.Context, id string, accessToken, refreshToken string, expiresAt int64, accountID ...string) error {
	var connectionID string
	err := r.db.QueryRowContext(ctx, `SELECT COALESCE(oauth_connection_id, '') FROM agent_configs WHERE id = ?`, id).Scan(&connectionID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("updating OAuth tokens: model config not found")
	}
	if err != nil {
		return fmt.Errorf("loading OAuth token owner: %w", err)
	}
	var result sql.Result
	if connectionID != "" {
		if len(accountID) > 0 {
			result, err = execBoundSQLite(ctx, r.db,
				`UPDATE oauth_connections SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?, oauth_needs_reauth = 0, updated_at = datetime('now') WHERE id = ?`,
				accessToken, refreshToken, expiresAt, accountID[0], connectionID)
		} else {
			result, err = execBoundSQLite(ctx, r.db,
				`UPDATE oauth_connections SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_needs_reauth = 0, updated_at = datetime('now') WHERE id = ?`,
				accessToken, refreshToken, expiresAt, connectionID)
		}
	} else if len(accountID) > 0 {
		result, err = execBoundSQLite(ctx, r.db,
			`UPDATE agent_configs SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?, oauth_needs_reauth = 0, updated_at = datetime('now') WHERE id = ?`,
			accessToken, refreshToken, expiresAt, accountID[0], id)
	} else {
		result, err = execBoundSQLite(ctx, r.db,
			`UPDATE agent_configs SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_needs_reauth = 0, updated_at = datetime('now') WHERE id = ?`,
			accessToken, refreshToken, expiresAt, id)
	}
	if err != nil {
		return fmt.Errorf("updating OAuth tokens: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking OAuth token update: %w", err)
	}
	if changed != 1 {
		return fmt.Errorf("updating OAuth tokens: credential owner not found")
	}
	return nil
}

func (r *LLMConfigRepo) UpdateStandardOAuthConnectionIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider, accessToken, refreshToken string, expiresAt int64, accountID string) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE oauth_connections
			 SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?,
			     oauth_provider_display_name = '', oauth_principal_hash = '',
			     oauth_needs_reauth = 0, oauth_revision = oauth_revision + 1, updated_at = datetime('now')
		 WHERE id = (SELECT oauth_connection_id FROM agent_configs WHERE id = ? AND provider = ? AND auth_method = ?)
		   AND provider = ? AND oauth_revision = ?`,
		accessToken, refreshToken, expiresAt, accountID, id, provider, models.AuthMethodOAuth, provider, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("conditionally updating OAuth connection: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking conditional OAuth connection update: %w", err)
	}
	return changed == 1, nil
}

func (r *LLMConfigRepo) UpdateStandardOAuthTokensIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider, accessToken, refreshToken string, expiresAt int64, accountID ...string) (bool, error) {
	identityAssignment := ""
	args := []any{accessToken, refreshToken, expiresAt}
	if len(accountID) > 0 {
		identityAssignment = ", oauth_account_id = ?"
		args = append(args, accountID[0])
	}
	args = append(args, id, provider, models.AuthMethodOAuth, provider, expectedRevision)
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE oauth_connections
		 SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?`+identityAssignment+`,
		     oauth_needs_reauth = 0, updated_at = datetime('now')
		 WHERE id = (SELECT oauth_connection_id FROM agent_configs WHERE id = ? AND provider = ? AND auth_method = ?)
		   AND provider = ? AND oauth_revision = ?`, args...)
	if err != nil {
		return false, fmt.Errorf("conditionally updating OAuth tokens: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking conditional OAuth token update: %w", err)
	}
	return changed == 1, nil
}

func (r *LLMConfigRepo) UpdateOAuthAccountIDIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider, accountID string) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE oauth_connections SET oauth_account_id = ?, updated_at = datetime('now')
		 WHERE id = (SELECT oauth_connection_id FROM agent_configs WHERE id = ? AND provider = ? AND auth_method = ?)
		   AND provider = ? AND oauth_revision = ?`,
		accountID, id, provider, models.AuthMethodOAuth, provider, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("conditionally updating OAuth account identity: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking OAuth account identity update: %w", err)
	}
	return changed == 1, nil
}

func (r *LLMConfigRepo) MarkOAuthNeedsReauthIfRevision(ctx context.Context, id string, expectedRevision int64, provider models.LLMProvider) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE oauth_connections SET oauth_needs_reauth = 1, updated_at = datetime('now')
		 WHERE id = (SELECT oauth_connection_id FROM agent_configs WHERE id = ? AND provider = ? AND auth_method = ?)
		   AND provider = ? AND oauth_revision = ?`,
		id, provider, models.AuthMethodOAuth, provider, expectedRevision)
	if err != nil {
		return false, fmt.Errorf("marking OAuth reauthentication required: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking OAuth reauthentication update: %w", err)
	}
	return changed == 1, nil
}

func (r *LLMConfigRepo) UpdateCustomAuthState(ctx context.Context, id, stateJSON string) error {
	if _, err := execBoundSQLite(ctx, r.db,
		`UPDATE agent_configs SET custom_auth_state_json = ?, updated_at = datetime('now') WHERE id = ?`,
		stateJSON, id); err != nil {
		return fmt.Errorf("updating custom authentication state: %w", err)
	}
	return nil
}

// UpdateCustomOAuthConnection atomically persists tokens and the provider state
// required to use them, so a connected token is never stored without its
// mandatory request metadata.
func (r *LLMConfigRepo) UpdateCustomOAuthConnection(ctx context.Context, id, accessToken, refreshToken string, expiresAt int64, stateJSON string) error {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE agent_configs
		 SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?,
		     custom_auth_state_json = ?, updated_at = datetime('now')
		 WHERE id = ?`,
		accessToken, refreshToken, expiresAt, stateJSON, id)
	if err != nil {
		return fmt.Errorf("updating custom OAuth connection: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil {
		return fmt.Errorf("checking custom OAuth connection update: %w", err)
	} else if changed != 1 {
		return fmt.Errorf("updating custom OAuth connection: model config not found")
	}
	return nil
}

func (r *LLMConfigRepo) UpdateCustomOAuthConnectionIfRevision(ctx context.Context, id string, expectedRevision int64, accessToken, refreshToken string, expiresAt int64, stateJSON string) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE agent_configs
		 SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?,
		     custom_auth_state_json = ?, updated_at = datetime('now')
		 WHERE id = ? AND oauth_config_revision = ? AND provider = ? AND auth_method = ?`,
		accessToken, refreshToken, expiresAt, stateJSON, id, expectedRevision,
		models.ProviderOpenAICompatible, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("conditionally updating custom OAuth connection: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking conditional custom OAuth connection update: %w", err)
	}
	return changed == 1, nil
}

// AdvanceCustomOAuthRevision assigns a new generation to a custom OAuth
// authorization attempt. Starting another attempt invalidates callbacks and
// refreshes that were created from an earlier generation.
func (r *LLMConfigRepo) AdvanceCustomOAuthRevision(ctx context.Context, id string) (int64, bool, error) {
	var revision int64
	err := queryRowBoundSQLite(ctx, r.db,
		`UPDATE agent_configs
		 SET oauth_config_revision = oauth_config_revision + 1
		 WHERE id = ? AND provider = ? AND auth_method = ?
		 RETURNING oauth_config_revision`,
		id, models.ProviderOpenAICompatible, models.AuthMethodOAuth,
	).Scan(&revision)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("advancing custom OAuth revision: %w", err)
	}
	return revision, true, nil
}

func (r *LLMConfigRepo) UpdateCustomOAuthTokensIfRevision(ctx context.Context, id string, expectedRevision int64, accessToken, refreshToken string, expiresAt int64) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE agent_configs
		 SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, updated_at = datetime('now')
		 WHERE id = ? AND oauth_config_revision = ? AND provider = ? AND auth_method = ?`,
		accessToken, refreshToken, expiresAt, id, expectedRevision,
		models.ProviderOpenAICompatible, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("conditionally updating custom OAuth tokens: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking conditional custom OAuth token update: %w", err)
	}
	return changed == 1, nil
}

func (r *LLMConfigRepo) TryAcquireOAuthRefreshLease(ctx context.Context, configID, ownerToken string, now time.Time, leaseDuration time.Duration) (bool, error) {
	configID = strings.TrimSpace(configID)
	ownerToken = strings.TrimSpace(ownerToken)
	if configID == "" || ownerToken == "" || leaseDuration <= 0 {
		return false, fmt.Errorf("complete OAuth refresh lease identity is required")
	}
	var connectionID string
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(
		(SELECT id FROM oauth_connections WHERE id = ?),
		(SELECT oauth_connection_id FROM agent_configs WHERE id = ?), '')`, configID, configID).Scan(&connectionID); err != nil {
		return false, fmt.Errorf("loading OAuth refresh lease owner: %w", err)
	}
	nowMillis := now.UTC().UnixMilli()
	if connectionID != "" {
		result, err := execBoundSQLite(ctx, r.db,
			`UPDATE oauth_connections
			 SET refresh_lease_owner = ?, refresh_lease_until = ?, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ? AND (refresh_lease_until <= ? OR refresh_lease_owner = ?)`,
			ownerToken, now.UTC().Add(leaseDuration).UnixMilli(), connectionID, nowMillis, ownerToken)
		if err != nil {
			return false, fmt.Errorf("acquiring OAuth connection refresh lease: %w", err)
		}
		changed, err := result.RowsAffected()
		return changed == 1, err
	}
	result, err := execBoundSQLite(ctx, r.db,
		`INSERT INTO oauth_refresh_leases (config_id, owner_token, lease_expires_at)
		 VALUES (?, ?, ?)
		 ON CONFLICT(config_id) DO UPDATE SET
		   owner_token = excluded.owner_token,
		   lease_expires_at = excluded.lease_expires_at,
		   updated_at = CURRENT_TIMESTAMP
		 WHERE oauth_refresh_leases.lease_expires_at <= ? OR oauth_refresh_leases.owner_token = excluded.owner_token`,
		configID, ownerToken, now.UTC().Add(leaseDuration).UnixMilli(), nowMillis)
	if err != nil {
		return false, fmt.Errorf("acquiring OAuth refresh lease: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("checking OAuth refresh lease: %w", err)
	}
	return changed == 1, nil
}

func (r *LLMConfigRepo) ReleaseOAuthRefreshLease(ctx context.Context, configID, ownerToken string) error {
	var connectionID string
	configID = strings.TrimSpace(configID)
	if err := r.db.QueryRowContext(ctx, `SELECT COALESCE(
		(SELECT id FROM oauth_connections WHERE id = ?),
		(SELECT oauth_connection_id FROM agent_configs WHERE id = ?), '')`, configID, configID).Scan(&connectionID); err != nil {
		return fmt.Errorf("loading OAuth refresh lease owner: %w", err)
	}
	if connectionID != "" {
		if _, err := execBoundSQLite(ctx, r.db,
			`UPDATE oauth_connections SET refresh_lease_owner = '', refresh_lease_until = 0
			 WHERE id = ? AND refresh_lease_owner = ?`, connectionID, strings.TrimSpace(ownerToken)); err != nil {
			return fmt.Errorf("releasing OAuth connection refresh lease: %w", err)
		}
		return nil
	}
	if _, err := execBoundSQLite(ctx, r.db,
		`DELETE FROM oauth_refresh_leases WHERE config_id = ? AND owner_token = ?`,
		strings.TrimSpace(configID), strings.TrimSpace(ownerToken)); err != nil {
		return fmt.Errorf("releasing OAuth refresh lease: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) Count(ctx context.Context) (int, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_configs`).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("counting model configs: %w", err)
	}
	return count, nil
}

func (r *LLMConfigRepo) TransferDefaultAndDelete(ctx context.Context, deleteID, newDefaultID string) error {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("begin transfer default tx: %w", err)
	}
	defer cleanup()

	// Set the new default (unsets all others first)
	if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET is_default = 0`); err != nil {
		return fmt.Errorf("unsetting defaults: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET is_default = 1, updated_at = datetime('now') WHERE id = ?`, newDefaultID); err != nil {
		return fmt.Errorf("setting new default: %w", err)
	}
	// Now delete the old default.
	if err := r.deleteWithTx(ctx, tx, deleteID); err != nil {
		return err
	}
	if err := r.ensureDefaultModelTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transfer default tx: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) GetByIDs(ctx context.Context, ids []string) (map[string]*models.LLMConfig, error) {
	if len(ids) == 0 {
		return map[string]*models.LLMConfig{}, nil
	}
	placeholders := make([]byte, 0, len(ids)*2-1)
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		if i > 0 {
			placeholders = append(placeholders, ',')
		}
		placeholders = append(placeholders, '?')
		args[i] = id
	}
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigColumns+`
		 FROM agent_configs a
		 LEFT JOIN oauth_connections c ON c.id = a.oauth_connection_id AND c.provider = a.provider AND a.auth_method = ?
		 WHERE a.id IN (`+string(placeholders)+`)`, append([]interface{}{models.AuthMethodOAuth}, args...)...)
	if err != nil {
		return nil, fmt.Errorf("batch getting model configs: %w", err)
	}
	defer rows.Close()

	result := make(map[string]*models.LLMConfig, len(ids))
	for rows.Next() {
		var a models.LLMConfig
		if err := scanLLMConfig(rows, &a); err != nil {
			return nil, fmt.Errorf("scanning model config: %w", err)
		}
		result[a.ID] = &a
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r *LLMConfigRepo) DeleteBulk(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return fmt.Errorf("at least one model is required")
	}
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("begin bulk delete model config tx: %w", err)
	}
	defer cleanup()
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i := range ids {
		args[i] = ids[i]
	}
	var count, defaults int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(SUM(is_default), 0) FROM agent_configs WHERE id IN (`+placeholders+`)`, args...).Scan(&count, &defaults); err != nil {
		return err
	}
	if count != len(ids) {
		return fmt.Errorf("model not found")
	}
	if defaults > 0 {
		return fmt.Errorf("default models cannot be deleted in bulk")
	}
	for _, id := range ids {
		if err := r.deleteWithTx(ctx, tx, id); err != nil {
			return err
		}
	}
	if err := r.ensureDefaultModelTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit bulk delete model config tx: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) Delete(ctx context.Context, id string) error {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("begin delete model config tx: %w", err)
	}
	defer cleanup()

	if err := r.deleteWithTx(ctx, tx, id); err != nil {
		return err
	}
	if err := r.ensureDefaultModelTx(ctx, tx); err != nil {
		return err
	}
	err = tx.Commit()
	if err != nil {
		return fmt.Errorf("commit delete model config tx: %w", err)
	}
	return nil
}
