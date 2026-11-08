package sqlite

import (
	"context"

	"github.com/openvibely/openvibely/internal/models"
	"github.com/openvibely/openvibely/internal/persistence"
)

// Copy only database-owned launch evidence before graph replacement cascades.
// No fallback is invented for graphs deleted before this feature was installed.
func archiveAutomationLaunchAuthority(ctx context.Context, tx sqlExecutor, projectID, automationID, retainedVersion string) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO automation_launch_authority
 (invocation_id,node_id,project_id,automation_id,version_id,publisher_id,initiator_id,publisher_team,publisher_sender,initiator_team,initiator_sender,initiator_discord_sender,publisher_discord_sender)
 SELECT i.id,n.id,i.project_id,i.automation_id,i.version_id,COALESCE(p.user_id,''),COALESCE(u.user_id,''),COALESCE(sp.team_id,''),COALESCE(sp.external_user_id,''),COALESCE(si.team_id,''),COALESCE(si.external_user_id,''),COALESCE(di.external_user_id,''),COALESCE(dp.external_user_id,'')
 FROM automation_invocations i JOIN automation_nodes n ON n.version_id=i.version_id AND n.automation_id=i.automation_id AND n.project_id=i.project_id
 LEFT JOIN automation_version_publishers p ON p.version_id=i.version_id
 LEFT JOIN automation_invocation_users u ON u.invocation_id=i.id
 LEFT JOIN slack_publisher_provenance sp ON sp.version_id=i.version_id
 LEFT JOIN slack_invocation_provenance si ON si.invocation_id=i.id
 LEFT JOIN discord_invocation_provenance di ON di.invocation_id=i.id
 LEFT JOIN discord_publisher_provenance dp ON dp.version_id=i.version_id
 WHERE i.project_id=? AND i.automation_id=? AND i.version_id<>?
 ON CONFLICT(invocation_id,node_id) DO NOTHING`, projectID, automationID, retainedVersion)
	return err
}

func requireArchivedLaunchAuthority(ctx context.Context, tx sqlExecutor, projectID string, b models.AutomationBinding) error {
	var publisher, initiator, pt, ps, it, is, discordSender, discordPublisher string
	err := tx.QueryRowContext(ctx, `SELECT publisher_id,initiator_id,publisher_team,publisher_sender,initiator_team,initiator_sender,initiator_discord_sender,publisher_discord_sender FROM automation_launch_authority WHERE invocation_id=? AND node_id=? AND project_id=? AND automation_id=? AND version_id=?`, b.InvocationID, b.NodeID, projectID, b.AutomationID, b.VersionID).Scan(&publisher, &initiator, &pt, &ps, &it, &is, &discordSender, &discordPublisher)
	if err != nil {
		return err
	}
	for _, v := range []struct{ user, team, sender string }{{publisher, pt, ps}, {initiator, it, is}} {
		if err := requireAutomationContinuationAuthor(ctx, tx, projectID, v.user); err != nil {
			return err
		}
		if v.team != "" || v.sender != "" {
			mapped, err := resolveSlackApplicationUser(ctx, tx, projectID, v.team, v.sender)
			if err != nil || mapped != v.user || v.user == "" {
				return persistence.ErrProjectDenied
			}
		}
	}
	for _, identity := range []models.DiscordIngressIdentity{
		{ProjectID: projectID, ExternalUserID: discordSender, UserID: initiator},
		{ProjectID: projectID, ExternalUserID: discordPublisher, UserID: publisher},
	} {
		if identity.ExternalUserID == "" {
			continue
		}
		saved := models.WithDiscordIngressIdentity(models.WithPrincipal(ctx, models.Principal{UserID: identity.UserID}), identity)
		if err := authorizeDiscordTask(saved, tx, models.Task{ProjectID: projectID, AutoMerge: true}); err != nil {
			return err
		}
	}
	if actor, ok := models.PrincipalFromContext(ctx); ok {
		if err := requireAutomationContinuationAuthor(ctx, tx, projectID, actor.UserID); err != nil {
			return err
		}
	}
	if identity, ok := models.SlackIngressIdentityFromContext(ctx); ok {
		if identity.ProjectID != projectID {
			return persistence.ErrProjectDenied
		}
		return authorizeSlackHandoff(ctx, tx, identity.EventKey)
	}
	return nil
}
