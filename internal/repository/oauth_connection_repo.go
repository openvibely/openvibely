package repository

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/openvibely/openvibely/internal/models"
)

func scanOAuthConnection(row interface{ Scan(...any) error }, connection *models.OAuthConnection) error {
	return row.Scan(
		&connection.ID, &connection.Provider, &connection.Name,
		&connection.AccessToken, &connection.RefreshToken, &connection.ExpiresAt,
		&connection.AccountID, &connection.NeedsReauth, &connection.Revision,
		&connection.CreatedAt, &connection.UpdatedAt, &connection.LinkedModels,
	)
}

const oauthConnectionColumns = `c.id, c.provider, c.name,
	c.oauth_access_token, c.oauth_refresh_token, c.oauth_expires_at,
	c.oauth_account_id, c.oauth_needs_reauth, c.oauth_revision,
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
			oauth_account_id, oauth_needs_reauth, oauth_revision
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id, created_at, updated_at`,
		connection.Provider, connection.Name, connection.AccessToken, connection.RefreshToken,
		connection.ExpiresAt, connection.AccountID, connection.NeedsReauth, connection.Revision,
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
	query := `SELECT ` + oauthConnectionColumns + ` FROM oauth_connections c`
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
	result, err := execBoundSQLite(ctx, r.db, `
		UPDATE oauth_connections
		SET oauth_access_token = ?, oauth_refresh_token = ?, oauth_expires_at = ?, oauth_account_id = ?,
			oauth_needs_reauth = 0, oauth_revision = oauth_revision + 1, updated_at = datetime('now')
		WHERE id = ? AND provider = ? AND oauth_revision = ?
		  AND EXISTS (
			SELECT 1 FROM agent_configs
			WHERE id = ? AND provider = ? AND auth_method = ? AND oauth_connection_id = oauth_connections.id
		  )`,
		accessToken, refreshToken, expiresAt, accountID, connectionID, provider, expectedRevision,
		modelID, provider, models.AuthMethodOAuth)
	if err != nil {
		return false, fmt.Errorf("conditionally replacing linked OAuth connection: %w", err)
	}
	changed, err := result.RowsAffected()
	return changed == 1, err
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
			oauth_account_id = '', oauth_needs_reauth = 1,
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
		return fmt.Errorf("OAuth connection is linked to %d model(s)", linked)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM oauth_connections WHERE id = ?`, connectionID); err != nil {
		return fmt.Errorf("deleting OAuth connection: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete OAuth connection tx: %w", err)
	}
	return nil
}
