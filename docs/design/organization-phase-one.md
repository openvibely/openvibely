# Organization accounts: Phase 1

One installation has one organization. Open **Organization** in the sidebar to rename it, manage members, or create invitation links.

## Existing installations

Migration 217 creates a default organization and owner, associates existing projects with them, and moves existing `ui.*` settings to that owner's preferences. New projects receive the same default organization association. Project ownership and sharing rules are Phase 2.

With local authentication enabled, `AUTH_USERNAME` and `AUTH_PASSWORD` seed the owner's account once. Passwords are stored as bcrypt hashes. After initialization, changing those environment values does not change the stored account. Password reset/change UI is not included in this phase.

Existing browser sessions must sign in again after the upgrade. New local sessions are random tokens; only their hashes are stored. Hosted sessions retain signature validation and also require a stored, revocable session.

With authentication off, every request acts as the default local owner. Invitations and membership changes require local authentication. Startup refuses auth-off mode if another member exists or the default user is no longer the sole owner.

## Inviting someone

1. Sign in as the owner and open **Organization**.
2. Create an invitation for a member or admin.
3. Copy the link shown once and send it to the intended recipient.
4. They open it and create an account, enter an existing account's credentials, or accept while already logged in.

Links expire after seven days, work once, and can be revoked. Anyone possessing the link can accept it; invitations are not bound to an email address. Email delivery is not implemented. New usernames are 3–64 letters, numbers, dots, dashes, or underscores; passwords are 12–72 bytes.

Owners can change member roles and remove members. Admins can invite and remove ordinary members but cannot manage owners or admins. The last owner cannot be removed or demoted. Removing membership deletes sessions and prevents later requests; connected WebSockets also recheck membership and terminate. Unused invitations from removed members or members demoted out of administration are revoked.

Personal `ui.*` preferences follow the authenticated user. Other application settings remain shared. Background callers without a user context use the bootstrap owner's preferences; durable job attribution belongs to Phase 2.

## Hosted behavior

Verified hosted SSO binds the existing workspace owner to the local owner account. A second hosted identity is rejected. Hosted multi-member invitations and workspace SSO changes remain Phase 6. Logout revokes the local session as well as clearing the cookie.

## Limits

This phase establishes identities and organization administration. **All members still share access to all projects and existing global application operations.** Org roles only restrict organization management so far. Project permissions, resource-wide authorization, background-job revocation, separate credentials, and sandboxing remain later phases. Removing membership does not cancel work already running.

The implementation uses the database-neutral `OrganizationStore`; SQLite implements it with transactional invitation consumption and last-owner checks. Migration rollback restores the bootstrap owner's UI preferences to the legacy settings table; it drops the new organization account data.

## Validation

Tests cover migration upgrade/rollback with existing projects and settings, concurrent invitation acceptance, concurrent last-owner demotion, role escalation denial, expired and revoked invites, account login/logout, preference separation, auth-off restrictions, hosted identity binding and session revocation, HTTP invitation flows, cross-origin rejection, and removal during a WebSocket connection.
