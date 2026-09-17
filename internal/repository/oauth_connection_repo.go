package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

var (
	ErrOAuthConnectionNotFound    = errors.New("OAuth connection not found")
	ErrOAuthConnectionInvalidMove = errors.New("invalid OAuth connection model move")
	ErrOAuthConnectionLinked      = errors.New("OAuth connection has linked models")
)

func scanOAuthConnection(row interface{ Scan(...any) error }, connection *models.OAuthConnection) error {
	return row.Scan(
		&connection.ID, &connection.Provider, &connection.Name,
		&connection.AccessToken, &connection.RefreshToken, &connection.ExpiresAt,
		&connection.AccountID, &connection.ProviderDisplayName, &connection.PrincipalHash,
		&connection.PrincipalVerified,
		&connection.NeedsReauth, &connection.Revision,
		&connection.CreatedAt, &connection.UpdatedAt, &connection.LinkedModels,
	)
}

const oauthConnectionColumns = `c.id, c.provider, c.name,
		c.oauth_access_token, c.oauth_refresh_token, c.oauth_expires_at,
			c.oauth_account_id, c.oauth_provider_display_name, c.oauth_principal_hash, c.oauth_principal_verified,
		c.oauth_needs_reauth, c.oauth_revision,
	c.created_at, c.updated_at,
	(SELECT COUNT(*) FROM agent_configs linked WHERE linked.oauth_connection_id = c.id)`

const oauthConnectionSummaryColumns = `c.id, c.provider,
			COALESCE(
				NULLIF(TRIM(c.oauth_provider_display_name), ''),
				(SELECT NULLIF(TRIM(snapshot.account_display_name), '')
			 FROM account_usage_snapshots snapshot
			 WHERE snapshot.oauth_connection_id = c.id
			   AND snapshot.oauth_config_revision = c.oauth_revision
			   AND TRIM(snapshot.account_display_name) != ''
			 ORDER BY snapshot.fetched_at DESC, snapshot.created_at DESC, snapshot.rowid DESC
			 LIMIT 1),
			CASE c.provider
				WHEN 'anthropic' THEN 'Anthropic account'
				WHEN 'openai' THEN 'OpenAI account'
				ELSE c.name
			END
		),
			CASE WHEN c.oauth_access_token != '' THEN 'present' ELSE '' END, '', c.oauth_expires_at,
				'', '', '', 0, c.oauth_needs_reauth, 0,
		c.created_at, c.updated_at,
		(SELECT COUNT(*) FROM agent_configs linked WHERE linked.oauth_connection_id = c.id)`

func (r *LLMConfigRepo) CreateOAuthConnection(ctx context.Context, connection *models.OAuthConnection) error {
	if connection == nil {
		return fmt.Errorf("OAuth connection is required")
	}
	if connection.Provider != models.ProviderOpenAI && connection.Provider != models.ProviderAnthropic {
		return fmt.Errorf("OAuth connections require OpenAI or Anthropic provider")
	}
	connection.Name = strings.TrimSpace(connection.Name)
	if connection.Name == "" {
		connection.Name = string(connection.Provider) + " account"
	}
	return queryRowBoundSQLite(ctx, r.db, `
			INSERT INTO oauth_connections (
				provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at,
				oauth_account_id, oauth_provider_display_name, oauth_principal_hash, oauth_principal_verified,
				oauth_needs_reauth, oauth_revision
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		connection.Provider, connection.Name, connection.AccessToken, connection.RefreshToken,
		connection.ExpiresAt, connection.AccountID, connection.ProviderDisplayName,
		connection.PrincipalHash, connection.PrincipalVerified, connection.NeedsReauth, connection.Revision,
	).Scan(&connection.ID, &connection.CreatedAt, &connection.UpdatedAt)
}

func (r *LLMConfigRepo) GetOAuthConnectionByID(ctx context.Context, id string) (*models.OAuthConnection, error) {
	var connection models.OAuthConnection
	err := scanOAuthConnection(r.db.QueryRowContext(ctx, `SELECT `+oauthConnectionColumns+` FROM oauth_connections c WHERE c.id = ?`, id), &connection)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("getting OAuth connection: %w", err)
	}
	return &connection, nil
}

func (r *LLMConfigRepo) ListOAuthConnections(ctx context.Context, provider models.LLMProvider) ([]models.OAuthConnection, error) {
	query := `SELECT ` + oauthConnectionSummaryColumns + ` FROM oauth_connections c`
	var args []any
	if provider != "" {
		query += ` WHERE c.provider = ?`
		args = append(args, provider)
	}
	query += ` ORDER BY c.provider, c.name COLLATE NOCASE, c.id`
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing OAuth connections: %w", err)
	}
	defer rows.Close()
	var connections []models.OAuthConnection
	for rows.Next() {
		var connection models.OAuthConnection
		if err := scanOAuthConnection(rows, &connection); err != nil {
			return nil, fmt.Errorf("scanning OAuth connection: %w", err)
		}
		connections = append(connections, connection)
	}
	return connections, rows.Err()
}

func validateOAuthConnectionLinkTx(ctx context.Context, tx SQLExecutor, connectionID string, provider models.LLMProvider) error {
	if strings.TrimSpace(connectionID) == "" {
		return nil
	}
	var storedProvider models.LLMProvider
	if err := tx.QueryRowContext(ctx, `SELECT provider FROM oauth_connections WHERE id = ?`, connectionID).Scan(&storedProvider); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("OAuth connection not found")
		}
		return fmt.Errorf("validating OAuth connection: %w", err)
	}
	if storedProvider != provider {
		return fmt.Errorf("OAuth connection provider does not match model provider")
	}
	return nil
}

func createPrivateOAuthConnectionTx(ctx context.Context, tx SQLExecutor, cfg *models.LLMConfig) error {
	if cfg.Provider != models.ProviderOpenAI && cfg.Provider != models.ProviderAnthropic {
		return nil
	}
	return tx.QueryRowContext(ctx, `
		INSERT INTO oauth_connections (
			provider, name, oauth_access_token, oauth_refresh_token, oauth_expires_at,
			oauth_account_id, oauth_needs_reauth, oauth_revision
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`, cfg.Provider, cfg.Name, cfg.OAuthAccessToken, cfg.OAuthRefreshToken,
		cfg.OAuthExpiresAt, cfg.OAuthAccountID, cfg.OAuthNeedsReauth, cfg.OAuthConfigRevision,
	).Scan(&cfg.OAuthConnectionID)
}

func (r *LLMConfigRepo) LinkOAuthConnection(ctx context.Context, modelID, connectionID string) error {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return err
	}
	defer cleanup()
	var provider models.LLMProvider
	var auth models.AuthMethod
	if err := tx.QueryRowContext(ctx, `SELECT provider, auth_method FROM agent_configs WHERE id = ?`, modelID).Scan(&provider, &auth); err != nil {
		return fmt.Errorf("loading model for OAuth link: %w", err)
	}
	if auth != models.AuthMethodOAuth || (provider != models.ProviderOpenAI && provider != models.ProviderAnthropic) {
		return fmt.Errorf("only OpenAI or Anthropic OAuth models can link an OAuth connection")
	}
	if err := validateOAuthConnectionLinkTx(ctx, tx, connectionID, provider); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_configs
		SET oauth_connection_id = ?, oauth_access_token = '', oauth_refresh_token = '',
			oauth_expires_at = 0, oauth_account_id = '', oauth_needs_reauth = 0,
			updated_at = datetime('now')
		WHERE id = ?`, connectionID, modelID); err != nil {
		return fmt.Errorf("linking OAuth connection: %w", err)
	}
	return tx.Commit()
}

func (r *LLMConfigRepo) ReplaceLinkedOAuthConnectionIfRevision(ctx context.Context, modelID, connectionID string, expectedRevision int64, provider models.LLMProvider, accessToken, refreshToken string, expiresAt int64, accountID string) (bool, error) {
	return r.ReplaceLinkedOAuthConnectionWithProfileIfRevision(ctx, modelID, connectionID, expectedRevision, provider, accessToken, refreshToken, expiresAt, accountID, "", "")
}

// ReplaceLinkedOAuthConnectionWithProfileIfRevision atomically replaces one
// connection generation and adopts other connections only when the provider has
// supplied the same strong user/account principal.
func (r *LLMConfigRepo) ReplaceLinkedOAuthConnectionWithProfileIfRevision(ctx context.Context, modelID, connectionID string, expectedRevision int64, provider models.LLMProvider, accessToken, refreshToken string, expiresAt int64, accountID, displayName, principalHash string) (bool, error) {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return false, err
	}
	defer cleanup()

	displayName = strings.TrimSpace(displayName)
	principalHash = strings.TrimSpace(principalHash)
	result, err := tx.ExecContext(ctx, `
		UPDATE oauth_connections
			SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?,
				oauth_provider_display_name = ?, oauth_principal_hash = ?, oauth_principal_verified = CASE WHEN ? != '' THEN 1 ELSE 0 END,
			oauth_needs_reauth = 0, oauth_revision = oauth_revision + 1, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
		  )`, accessToken, refreshToken, expiresAt, accountID, displayName, principalHash,
		principalHash, connectionID, provider, expectedRevision, modelID, provider, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("conditionally replacing linked OAuth connection: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false, err
	}

	if principalHash != "" {
		if err := adoptVerifiedOAuthPrincipalTx(ctx, tx, connectionID, provider, expectedRevision+1, principalHash); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// UpdateLinkedOAuthConnectionProfileIfRevision persists current provider profile
// metadata and performs the same verified-principal adoption without rotating the
// connection generation.
func (r *LLMConfigRepo) UpdateLinkedOAuthConnectionProfileIfRevision(ctx context.Context, modelID, connectionID string, expectedRevision int64, provider models.LLMProvider, accountID, displayName, principalHash string) (bool, error) {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return false, err
	}
	defer cleanup()

	displayName = strings.TrimSpace(displayName)
	principalHash = strings.TrimSpace(principalHash)
	result, err := tx.ExecContext(ctx, `
		UPDATE oauth_connections
			SET oauth_account_id = ?, oauth_provider_display_name = ?, oauth_principal_hash = ?,
				oauth_principal_verified = CASE WHEN ? != '' THEN 1 ELSE 0 END, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
			  )`, accountID, displayName, principalHash, principalHash, connectionID, provider, expectedRevision,
		modelID, provider, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("conditionally updating linked OAuth connection profile: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return false, err
	}
	if principalHash != "" {
		if err := adoptVerifiedOAuthPrincipalTx(ctx, tx, connectionID, provider, expectedRevision, principalHash); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func adoptVerifiedOAuthPrincipalTx(ctx context.Context, tx *manualTx, canonicalID string, provider models.LLMProvider, canonicalRevision int64, principalHash string) error {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, oauth_revision
		FROM oauth_connections
			WHERE provider = ? AND oauth_principal_hash = ? AND oauth_principal_verified = 1 AND id != ?
		ORDER BY id`, provider, principalHash, canonicalID)
	if err != nil {
		return fmt.Errorf("listing verified OAuth principal connections: %w", err)
	}
	type sourceConnection struct {
		id       string
		revision int64
	}
	var sources []sourceConnection
	for rows.Next() {
		var source sourceConnection
		if err := rows.Scan(&source.id, &source.revision); err != nil {
			rows.Close()
			return fmt.Errorf("scanning verified OAuth principal connection: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, source := range sources {
		if _, err := tx.ExecContext(ctx, `
			UPDATE account_usage_snapshots
			SET oauth_connection_id = ?,
				oauth_config_revision = CASE WHEN oauth_config_revision = ? THEN ? ELSE -1 END
			WHERE provider = ? AND oauth_connection_id = ?`, canonicalID, source.revision, canonicalRevision, provider, source.id); err != nil {
			return fmt.Errorf("moving verified OAuth principal snapshots: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE agent_configs SET oauth_connection_id = ?, updated_at = datetime('now')
			WHERE provider = ? AND auth_method = ? AND oauth_connection_id = ?`, canonicalID, provider, models.AuthMethodOAuth, source.id); err != nil {
			return fmt.Errorf("moving verified OAuth principal models: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_connections WHERE id = ? AND provider = ?`, source.id, provider); err != nil {
			return fmt.Errorf("deleting adopted OAuth principal connection: %w", err)
		}
	}
	return nil
}

// AdoptUnrefreshableOpenAIConnectionIfPrincipalMatches records freshly verified
// ownership evidence for an expired Codex connection, then links it to a healthy
// connection only when their provider-verified principals match exactly.
func (r *LLMConfigRepo) AdoptUnrefreshableOpenAIConnectionIfPrincipalMatches(ctx context.Context, modelID, sourceID string, expectedRevision int64, principalHash string) (bool, error) {
	principalHash = strings.TrimSpace(principalHash)
	if principalHash == "" {
		return false, nil
	}
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return false, err
	}
	defer cleanup()

	var sourceRevision int64
	err = tx.QueryRowContext(ctx, `
		SELECT oauth_revision
		FROM oauth_connections
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND oauth_access_token != ''
		  AND (oauth_needs_reauth = 1 OR oauth_refresh_token = '' OR
		       (oauth_expires_at > 0 AND oauth_expires_at <= CAST(strftime('%s', 'now') AS INTEGER) * 1000))
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
		  )`, sourceID, models.ProviderOpenAI, expectedRevision,
		modelID, models.ProviderOpenAI, models.AuthMethodOAuth).Scan(&sourceRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("loading unrefreshable OpenAI OAuth connection: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE oauth_connections
		SET oauth_principal_hash = ?, oauth_principal_verified = 1, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?`, principalHash, sourceID, models.ProviderOpenAI, sourceRevision); err != nil {
		return false, fmt.Errorf("persisting verified OpenAI OAuth principal: %w", err)
	}

	type targetConnection struct {
		id       string
		revision int64
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, oauth_revision
		FROM oauth_connections
			WHERE provider = ? AND id != ? AND oauth_principal_hash = ? AND oauth_principal_verified = 1
		  AND oauth_needs_reauth = 0
		  AND oauth_access_token != '' AND oauth_refresh_token != ''
		  AND oauth_expires_at > CAST(strftime('%s', 'now') AS INTEGER) * 1000
		ORDER BY id`, models.ProviderOpenAI, sourceID, principalHash)
	if err != nil {
		return false, fmt.Errorf("finding matching healthy OpenAI OAuth connection: %w", err)
	}
	var targets []targetConnection
	for rows.Next() {
		var target targetConnection
		if err := rows.Scan(&target.id, &target.revision); err != nil {
			rows.Close()
			return false, fmt.Errorf("scanning matching healthy OpenAI OAuth connection: %w", err)
		}
		targets = append(targets, target)
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if len(targets) != 1 {
		if err := tx.Commit(); err != nil {
			return false, err
		}
		return false, nil
	}
	target := targets[0]
	if _, err := tx.ExecContext(ctx, `
		UPDATE account_usage_snapshots
		SET oauth_connection_id = ?,
			oauth_config_revision = CASE WHEN oauth_config_revision = ? THEN ? ELSE -1 END
		WHERE provider = ? AND oauth_connection_id = ?`, target.id, sourceRevision, target.revision, models.ProviderOpenAI, sourceID); err != nil {
		return false, fmt.Errorf("moving matched OpenAI OAuth snapshots: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_configs SET oauth_connection_id = ?, updated_at = datetime('now')
		WHERE provider = ? AND auth_method = ? AND oauth_connection_id = ?`, target.id, models.ProviderOpenAI, models.AuthMethodOAuth, sourceID); err != nil {
		return false, fmt.Errorf("moving matched OpenAI OAuth models: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_connections WHERE id = ? AND provider = ?`, sourceID, models.ProviderOpenAI); err != nil {
		return false, fmt.Errorf("deleting matched expired OpenAI OAuth connection: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

func (r *LLMConfigRepo) UpdateLinkedOAuthConnectionTokensIfRevision(ctx context.Context, modelID, connectionID string, expectedRevision int64, provider models.LLMProvider, accessToken, refreshToken string, expiresAt int64, accountID ...string) (bool, error) {
	identityAssignment := ""
	args := []any{accessToken, refreshToken, expiresAt}
	if len(accountID) > 0 {
		identityAssignment = ", oauth_account_id = ?"
		args = append(args, accountID[0])
	}
	args = append(args, connectionID, provider, expectedRevision, modelID, provider, models.AuthMethodOAuth)
	result, err := execBoundSQLite(ctx, r.db, `
		UPDATE oauth_connections
		SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?`+identityAssignment+`,
			oauth_needs_reauth = 0, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
		  )`, args...)
	if err != nil {
		return false, fmt.Errorf("conditionally updating linked OAuth connection tokens: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (r *LLMConfigRepo) UpdateLinkedOAuthConnectionAccountIDIfRevision(ctx context.Context, modelID, connectionID string, expectedRevision int64, provider models.LLMProvider, accountID string) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db, `
		UPDATE oauth_connections SET oauth_account_id = ?, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
		  )`, accountID, connectionID, provider, expectedRevision, modelID, provider, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("conditionally updating linked OAuth connection account identity: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (r *LLMConfigRepo) MarkLinkedOAuthConnectionNeedsReauthIfRevision(ctx context.Context, modelID, connectionID string, expectedRevision int64, provider models.LLMProvider) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db, `
		UPDATE oauth_connections SET oauth_needs_reauth = 1, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
		  )`, connectionID, provider, expectedRevision, modelID, provider, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("marking linked OAuth connection reauthentication required: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (r *LLMConfigRepo) DisconnectLinkedOAuthConnection(ctx context.Context, modelID, connectionID string, expectedRevision int64, provider models.LLMProvider) (bool, error) {
	result, err := execBoundSQLite(ctx, r.db, `
		UPDATE oauth_connections
			SET oauth_access_token = '', oauth_refresh_token = '', oauth_expires_at = 0,
				oauth_account_id = '', oauth_provider_display_name = '', oauth_principal_hash = '', oauth_principal_verified = 0,
			oauth_needs_reauth = 1,
			oauth_revision = oauth_revision + 1, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
		  )`, connectionID, provider, expectedRevision, modelID, provider, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("disconnecting linked OAuth connection: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
}

func (r *LLMConfigRepo) RenameOAuthConnection(ctx context.Context, connectionID, name string) error {
	connectionID = strings.TrimSpace(connectionID)
	name = strings.TrimSpace(name)
	if connectionID == "" {
		return fmt.Errorf("%w: connection is required", ErrOAuthConnectionNotFound)
	}
	if name == "" {
		return fmt.Errorf("OAuth connection name is required")
	}
	if len(name) > 200 {
		return fmt.Errorf("OAuth connection name must be at most 200 characters")
	}
	result, err := execBoundSQLite(ctx, r.db,
		`UPDATE oauth_connections SET name = ?, updated_at = datetime('now') WHERE id = ?`, name, connectionID)
	if err != nil {
		return fmt.Errorf("renaming OAuth connection: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("checking OAuth connection rename: %w", err)
	}
	if changed != 1 {
		return ErrOAuthConnectionNotFound
	}
	return nil
}

func (r *LLMConfigRepo) MoveModelsToOAuthConnection(ctx context.Context, modelIDs []string, connectionID string) error {
	connectionID = strings.TrimSpace(connectionID)
	if connectionID == "" {
		return fmt.Errorf("%w: connection is required", ErrOAuthConnectionInvalidMove)
	}
	seen := make(map[string]struct{}, len(modelIDs))
	ids := make([]string, 0, len(modelIDs))
	for _, id := range modelIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("%w: model identifiers must not be empty", ErrOAuthConnectionInvalidMove)
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return fmt.Errorf("%w: at least one model is required", ErrOAuthConnectionInvalidMove)
	}

	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("begin move OAuth models tx: %w", err)
	}
	defer cleanup()
	var provider models.LLMProvider
	if err := tx.QueryRowContext(ctx, `SELECT provider FROM oauth_connections WHERE id = ?`, connectionID).Scan(&provider); err != nil {
		if err == sql.ErrNoRows {
			return ErrOAuthConnectionNotFound
		}
		return fmt.Errorf("loading target OAuth connection: %w", err)
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(ids)), ",")
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	var compatible int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM agent_configs
		WHERE id IN (`+placeholders+`) AND provider = ? AND auth_method = ?`,
		append(args, provider, models.AuthMethodOAuth)...,
	).Scan(&compatible); err != nil {
		return fmt.Errorf("validating OAuth models for move: %w", err)
	}
	if compatible != len(ids) {
		return fmt.Errorf("%w: all selected models must exist and use %s OAuth", ErrOAuthConnectionInvalidMove, provider)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE agent_configs
		SET oauth_connection_id = ?, oauth_access_token = '', oauth_refresh_token = '',
			oauth_expires_at = 0, oauth_account_id = '', oauth_needs_reauth = 0,
			updated_at = datetime('now')
		WHERE id IN (`+placeholders+`)`, append([]any{connectionID}, args...)...); err != nil {
		return fmt.Errorf("moving models to OAuth connection: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit move OAuth models tx: %w", err)
	}
	return nil
}

func (r *LLMConfigRepo) DeleteOAuthConnection(ctx context.Context, connectionID string) error {
	tx, cleanup, err := beginImmediateTx(ctx, r.db)
	if err != nil {
		return fmt.Errorf("begin delete OAuth connection tx: %w", err)
	}
	defer cleanup()
	var linked int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM agent_configs WHERE oauth_connection_id = ?`, connectionID).Scan(&linked); err != nil {
		return fmt.Errorf("counting linked OAuth models: %w", err)
	}
	if linked != 0 {
		return fmt.Errorf("%w: %d model(s)", ErrOAuthConnectionLinked, linked)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_connections WHERE id = ?`, connectionID); err != nil {
		return fmt.Errorf("deleting OAuth connection: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete OAuth connection tx: %w", err)
	}
	return nil
}
