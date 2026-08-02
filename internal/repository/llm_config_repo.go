package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/openvibely/openvibely/internal/models"
)

type LLMConfigRepo struct {
	db *sql.DB
}

func NewLLMConfigRepo(db *sql.DB) *LLMConfigRepo {
	return &LLMConfigRepo{db: db}
}

const llmConfigColumns = `id, name, provider, model, reasoning_effort, api_key, max_tokens, temperature, is_default, created_at, updated_at, auth_method, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_account_id, max_workers, worker_timeout, oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url, base_url, transport, preset_slug, models_url, auth_header_name, auth_header_value_prefix, extra_headers_json, extra_body_json, default_max_tokens, token_exchange_format, token_refresh_format, custom_auth_config_json, custom_auth_state_json, oauth_config_revision, mixture_config_json, auto_start_tasks`

// llmConfigCardColumns is the bounded Models-page projection. Credential bodies,
// edit-only endpoint settings, request JSON, custom-auth JSON, and full mixture
// JSON are deliberately excluded from the initial response path.
const llmConfigCardColumns = `id, name, provider, model, reasoning_effort,
	CASE WHEN api_key != '' THEN 1 ELSE 0 END, temperature, is_default, auth_method,
	CASE WHEN oauth_access_token != '' THEN 1 ELSE 0 END, oauth_expires_at,
	max_workers, worker_timeout, substr(ollama_base_url, 1, 512), substr(base_url, 1, 512),
	CASE WHEN json_valid(mixture_config_json) THEN substr(COALESCE(json_extract(mixture_config_json, '$.aggregator.agent_config_id'), ''), 1, 128) ELSE '' END,
	CASE WHEN json_valid(mixture_config_json) THEN substr(COALESCE(json_extract(mixture_config_json, '$.aggregator.label'), ''), 1, 256) ELSE '' END,
	CASE WHEN json_valid(mixture_config_json) AND json_type(mixture_config_json, '$.reference_models') = 'array' THEN json_array_length(mixture_config_json, '$.reference_models') ELSE 0 END`

func scanLLMConfig(row interface{ Scan(dest ...any) error }, a *models.LLMConfig) error {
	return row.Scan(&a.ID, &a.Name, &a.Provider, &a.Model, &a.ReasoningEffort, &a.APIKey,
		&a.MaxTokens, &a.Temperature, &a.IsDefault, &a.CreatedAt, &a.UpdatedAt,
		&a.AuthMethod, &a.OAuthAccessToken, &a.OAuthRefreshToken, &a.OAuthExpiresAt,
		&a.OAuthAccountID,
		&a.MaxWorkers, &a.WorkerTimeout,
		&a.OAuthClientID, &a.OAuthClientSecret, &a.OAuthAuthorizeURL, &a.OAuthTokenURL, &a.OAuthScopes,
		&a.OllamaBaseURL, &a.BaseURL, &a.Transport, &a.PresetSlug, &a.ModelsURL,
		&a.AuthHeaderName, &a.AuthHeaderValuePrefix, &a.ExtraHeadersJSON, &a.ExtraBodyJSON,
		&a.DefaultMaxTokens, &a.TokenExchangeFormat, &a.TokenRefreshFormat, &a.CustomAuthConfigJSON, &a.CustomAuthStateJSON, &a.OAuthConfigRevision, &a.MixtureConfigJSON, &a.AutoStartTasks)
}

func (r *LLMConfigRepo) List(ctx context.Context) ([]models.LLMConfig, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+llmConfigColumns+`
			 FROM agent_configs ORDER BY is_default DESC, name ASC`)
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
	return configs, rows.Err()
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
			&hasOAuthKey, &a.OAuthExpiresAt, &a.MaxWorkers, &a.WorkerTimeout,
			&a.OllamaBaseURL, &a.BaseURL, &a.MixtureAggregatorID,
			&a.MixtureAggregatorLabel, &a.MixtureReferenceCount); err != nil {
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

func (r *LLMConfigRepo) GetByID(ctx context.Context, id string) (*models.LLMConfig, error) {
	var a models.LLMConfig
	err := scanLLMConfig(r.db.QueryRowContext(ctx,
		`SELECT `+llmConfigColumns+`
		 FROM agent_configs WHERE id = ?`, id), &a)
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
		 FROM agent_configs WHERE is_default = 1 LIMIT 1`), &a)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting default model config: %w", err)
	}
	return &a, nil
}

func (r *LLMConfigRepo) ensureDefaultModelTx(ctx context.Context, tx *sql.Tx) error {
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

func (r *LLMConfigRepo) deleteWithTx(ctx context.Context, tx *sql.Tx, id string) error {
	// Nullify FK references in tasks and executions before deleting
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET agent_id = NULL WHERE agent_id = ?`, id); err != nil {
		return fmt.Errorf("nullifying model config in tasks: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE executions SET agent_config_id = NULL WHERE agent_config_id = ?`, id); err != nil {
		return fmt.Errorf("nullifying model config in executions: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM agent_configs WHERE id = ?`, id); err != nil {
		return fmt.Errorf("deleting model config: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) Create(ctx context.Context, a *models.LLMConfig) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create model config tx: %w", err)
	}
	defer tx.Rollback()

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
		a.AuthMethod = models.AuthMethodCLI
	}
	err = tx.QueryRowContext(ctx,
		`INSERT INTO agent_configs (id, name, provider, model, reasoning_effort, api_key, max_tokens, temperature, is_default, auth_method, oauth_access_token, oauth_refresh_token, oauth_expires_at, oauth_account_id, max_workers, worker_timeout, oauth_client_id, oauth_client_secret, oauth_authorize_url, oauth_token_url, oauth_scopes, ollama_base_url, base_url, transport, preset_slug, models_url, auth_header_name, auth_header_value_prefix, extra_headers_json, extra_body_json, default_max_tokens, token_exchange_format, token_refresh_format, custom_auth_config_json, custom_auth_state_json, mixture_config_json, auto_start_tasks)
			 VALUES (lower(hex(randomblob(16))), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 RETURNING id, created_at, updated_at`,
		a.Name, a.Provider, a.Model, a.ReasoningEffort, a.APIKey, a.MaxTokens, a.Temperature, a.IsDefault,
		a.AuthMethod, a.OAuthAccessToken, a.OAuthRefreshToken, a.OAuthExpiresAt, a.OAuthAccountID, a.MaxWorkers, a.WorkerTimeout,
		a.OAuthClientID, a.OAuthClientSecret, a.OAuthAuthorizeURL, a.OAuthTokenURL, a.OAuthScopes,
		a.OllamaBaseURL, a.BaseURL, a.Transport, a.PresetSlug, a.ModelsURL,
		a.AuthHeaderName, a.AuthHeaderValuePrefix, a.ExtraHeadersJSON, a.ExtraBodyJSON,
		a.DefaultMaxTokens, a.TokenExchangeFormat, a.TokenRefreshFormat, a.CustomAuthConfigJSON, a.CustomAuthStateJSON, a.MixtureConfigJSON, a.AutoStartTasks).
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
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin update model config tx: %w", err)
	}
	defer tx.Rollback()

	if a.IsDefault {
		if _, err := tx.ExecContext(ctx, `UPDATE agent_configs SET is_default = 0`); err != nil {
			return fmt.Errorf("unsetting defaults: %w", err)
		}
	}
	if a.AuthMethod == "" {
		a.AuthMethod = models.AuthMethodCLI
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE agent_configs SET name = ?, provider = ?, model = ?, reasoning_effort = ?, api_key = ?,
		 max_tokens = ?, temperature = ?, is_default = ?,
		 auth_method = ?, oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?,
		 max_workers = ?, worker_timeout = ?,
		 oauth_client_id = ?, oauth_client_secret = ?, oauth_authorize_url = ?, oauth_token_url = ?, oauth_scopes = ?,
		 ollama_base_url = ?, base_url = ?, transport = ?, preset_slug = ?, models_url = ?,
		 auth_header_name = ?, auth_header_value_prefix = ?, extra_headers_json = ?, extra_body_json = ?,
		 default_max_tokens = ?, token_exchange_format = ?, token_refresh_format = ?, custom_auth_config_json = ?, custom_auth_state_json = ?, mixture_config_json = ?, auto_start_tasks = ?,
		 oauth_config_revision = oauth_config_revision + 1,
		 updated_at = datetime('now')
		 WHERE id = ?`,
		a.Name, a.Provider, a.Model, a.ReasoningEffort, a.APIKey, a.MaxTokens, a.Temperature, a.IsDefault,
		a.AuthMethod, a.OAuthAccessToken, a.OAuthRefreshToken, a.OAuthExpiresAt, a.OAuthAccountID,
		a.MaxWorkers, a.WorkerTimeout,
		a.OAuthClientID, a.OAuthClientSecret, a.OAuthAuthorizeURL, a.OAuthTokenURL, a.OAuthScopes,
		a.OllamaBaseURL, a.BaseURL, a.Transport, a.PresetSlug, a.ModelsURL,
		a.AuthHeaderName, a.AuthHeaderValuePrefix, a.ExtraHeadersJSON, a.ExtraBodyJSON,
		a.DefaultMaxTokens, a.TokenExchangeFormat, a.TokenRefreshFormat, a.CustomAuthConfigJSON, a.CustomAuthStateJSON, a.MixtureConfigJSON, a.AutoStartTasks,
		a.ID)
	if err != nil {
		return fmt.Errorf("updating model config: %w", err)
	}
	if err := r.ensureDefaultModelTx(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit update model config tx: %w", err)
	}
	a.OAuthConfigRevision++
	return nil
}

// UpdateOAuthTokens updates only the OAuth token fields for a config.
func (r *LLMConfigRepo) UpdateOAuthTokens(ctx context.Context, id string, accessToken, refreshToken string, expiresAt int64, accountID ...string) error {
	var (
		err error
	)
	if len(accountID) > 0 {
		_, err = r.db.ExecContext(ctx,
			`UPDATE agent_configs SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?, updated_at = datetime('now')
			 WHERE id = ?`,
			accessToken, refreshToken, expiresAt, accountID[0], id)
	} else {
		_, err = r.db.ExecContext(ctx,
			`UPDATE agent_configs SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, updated_at = datetime('now')
			 WHERE id = ?`,
			accessToken, refreshToken, expiresAt, id)
	}
	if err != nil {
		return fmt.Errorf("updating OAuth tokens: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) UpdateCustomAuthState(ctx context.Context, id, stateJSON string) error {
	if _, err := r.db.ExecContext(ctx,
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
	result, err := r.db.ExecContext(ctx,
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
	result, err := r.db.ExecContext(ctx,
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
	err := r.db.QueryRowContext(ctx,
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
	result, err := r.db.ExecContext(ctx,
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
	nowMillis := now.UTC().UnixMilli()
	result, err := r.db.ExecContext(ctx,
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
	if _, err := r.db.ExecContext(ctx,
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
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transfer default tx: %w", err)
	}
	defer tx.Rollback()

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
		 FROM agent_configs WHERE id IN (`+string(placeholders)+`)`, args...)
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
	return result, rows.Err()
}

func (r *LLMConfigRepo) Delete(ctx context.Context, id string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete model config tx: %w", err)
	}
	defer tx.Rollback()

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
