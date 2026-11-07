package sqlite

import (
	"context"
	"database/sql"
	"strconv"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Reload actor and target inside the write transaction; stale request roles
// cannot authorize a reassignment. Unmapped legacy rows need an administrator.
func authorizeSenderMutation(ctx context.Context, tx sqlExecutor, before, after string, exists bool) (string, error) {
	p, ok := models.PrincipalFromContext(ctx)
	if !ok {
		if after != "" {
			return "", persistence.ErrProjectDenied
		}
		return "", nil
	}
	actor, err := scanOrgUser(tx.QueryRowContext(ctx, orgUserSelect+`WHERE u.id=?`, p.UserID))
	if err != nil {
		return "", err
	}
	if actor == nil || actor.Disabled || actor.OrgID == "" || actor.Role == "" {
		return "", persistence.ErrProjectDenied
	}
	if !actor.IsOrgAdmin() && ((after != actor.UserID && !(exists && after == "")) || (exists && before != actor.UserID)) {
		return "", persistence.ErrProjectDenied
	}
	if after != "" {
		target, err := scanOrgUser(tx.QueryRowContext(ctx, orgUserSelect+`WHERE u.id=?`, after))
		if err != nil {
			return "", err
		}
		if target == nil || target.Disabled || target.OrgID != actor.OrgID || target.Role == "" {
			return "", persistence.ErrProjectDenied
		}
	}
	return actor.UserID, nil
}
func senderAudit(ctx context.Context, tx sqlExecutor, table, id, actor, action, before, after string) error {
	if actor == "" {
		return nil
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO channel_sender_audit(channel,sender_id,actor_user_id,action,before_user_id,after_user_id) VALUES(?,?,?,?,?,?)`, table, id, actor, action, before, after)
	return err
}
func syncSenderBinding(ctx context.Context, tx sqlExecutor, table, id string, m models.ChannelSenderMapping, actor string) error {
	switch table {
	case "slack_authorized_users":
		if _, err := tx.ExecContext(ctx, `DELETE FROM slack_sender_identities WHERE allowlist_id=?`, id); err != nil {
			return err
		}
		if m.ApplicationUserID != "" {
			if m.TeamID == "" {
				return persistence.ErrProjectDenied
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO slack_sender_identities(allowlist_id,team_id,application_user_id,verified_by) VALUES(?,?,?,?)`, id, m.TeamID, m.ApplicationUserID, actor)
			return err
		}
	case "discord_authorized_users":
		if m.ApplicationUserID != "" {
			var externalID string
			if err := tx.QueryRowContext(ctx, `SELECT discord_user_id FROM discord_authorized_users WHERE id=?`, id).Scan(&externalID); err != nil {
				return err
			}
			numeric, err := strconv.ParseUint(externalID, 10, 64)
			if err != nil || numeric == 0 || strconv.FormatUint(numeric, 10) != externalID {
				return persistence.ErrProjectDenied
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM discord_sender_identities WHERE allowlist_id=?`, id); err != nil {
			return err
		}
		if m.ApplicationUserID != "" {
			_, err := tx.ExecContext(ctx, `INSERT INTO discord_sender_identities(allowlist_id,application_user_id,verified_by) VALUES(?,?,?)`, id, m.ApplicationUserID, actor)
			return err
		}
	}
	return nil
}
func bindGlobalSender(ctx context.Context, db *sql.DB, table, id, user, team string) error {
	return withImmediateTx(ctx, db, func(tx sqlExecutor) error {
		var old string
		if err := tx.QueryRowContext(ctx, `SELECT application_user_id FROM `+table+` WHERE id=?`, id).Scan(&old); err != nil {
			return err
		}
		actor, err := authorizeSenderMutation(ctx, tx, old, user, true)
		if err != nil {
			return err
		}
		if table == "telegram_authorized_users" && user != "" {
			var numeric int64
			if err := tx.QueryRowContext(ctx, `SELECT telegram_user_id FROM telegram_authorized_users WHERE id=?`, id).Scan(&numeric); err != nil {
				return err
			}
			if numeric <= 0 {
				return persistence.ErrProjectDenied
			}
		}
		m := models.ChannelSenderMapping{ApplicationUserID: user, TeamID: team}
		if err := syncSenderBinding(ctx, tx, table, id, m, actor); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET application_user_id=?,team_id=?,mapping_review=0 WHERE id=?`, user, team, id); err != nil {
			return err
		}
		return senderAudit(ctx, tx, table, id, actor, "mapped", old, user)
	})
}
func (r *TelegramAuthRepo) BindSender(ctx context.Context, id, user, team string) error {
	return bindGlobalSender(ctx, r.db, "telegram_authorized_users", id, user, team)
}
func (r *SlackAuthRepo) BindSender(ctx context.Context, id, user, team string) error {
	return bindGlobalSender(ctx, r.allowlist.db, "slack_authorized_users", id, user, team)
}
func (r *DiscordAuthRepo) BindSender(ctx context.Context, id, user, team string) error {
	return bindGlobalSender(ctx, r.allowlist.db, "discord_authorized_users", id, user, team)
}
func (r *EmailAuthRepo) BindSender(ctx context.Context, id, user, team string) error {
	return bindGlobalSender(ctx, r.allowlist.db, "email_authorized_senders", id, user, team)
}

func (r *TelegramAuthRepo) ResolveApplicationUser(ctx context.Context, id int64) (string, error) {
	var user string
	err := r.db.QueryRowContext(ctx, `SELECT a.application_user_id FROM telegram_authorized_users a JOIN users u ON u.id=a.application_user_id JOIN org_memberships m ON m.user_id=u.id AND m.org_id='default' WHERE a.telegram_user_id=? AND a.telegram_user_id>0 AND a.mapping_review=0 AND u.disabled=0`, id).Scan(&user)
	if err == sql.ErrNoRows {
		return "", persistence.ErrProjectDenied
	}
	return user, err
}
