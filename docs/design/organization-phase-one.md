# Organization accounts: Phase 1

One installation has one organization. Open **Settings → Organization** to rename it, manage members, or create invitation links.

## Existing installations

Migration 217 creates a default organization and owner, associates existing projects with them, and moves existing `ui.*` settings to that owner's preferences. New projects receive the same default organization association. Project ownership and sharing rules are Phase 2.

With local authentication enabled, `AUTH_USERNAME` and `AUTH_PASSWORD` seed the owner's account once. Passwords are stored as bcrypt hashes. After initialization, changing those environment values does not change the stored account. Password reset/change UI is not included in this phase.

Existing browser sessions must sign in again after the upgrade. New local sessions are random tokens; only their hashes are stored. Hosted sessions retain signature validation and also require a stored, revocable session. Each new hosted login includes a random session ID, so simultaneous logins remain independent and logging out of one does not revoke another. Existing signed sessions without that ID remain readable and still require their stored session record.

With authentication off, every request acts as the default local owner. Invitations and membership changes require local authentication. Startup refuses auth-off mode if another member exists or the default user is no longer the sole owner.

## Inviting someone

1. Sign in as the owner and open **Settings → Organization**.
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

## Message attribution

Migration 217 also records the sending account ID and username for new execution messages and queued/steering inputs. The initial task prompt retains its creator's name, even when another member starts it. Thread bubbles use the saved name for live responses and reloaded history; queue promotion preserves the original sender. Existing messages without recorded attribution continue to display “User”; their sender is not guessed. This is display attribution, not project authorization or durable background-job run-as enforcement.

## Login limits and trusted proxies

Local login allows 10 failed attempts per account/client-IP combination in a
15-minute window. Broader limits allow 100 failures per account across IPs and
100 failures per IP across accounts in that window. Successful logins do not
consume failure limits. In-flight checks reserve capacity until they finish.
Signup has a separate limit of 30 submissions per client IP per 15 minutes.
Counters are in memory and reset on restart.

By default, client IP means the connection's peer address; forwarded headers
are ignored. Direct access (including localhost) needs no configuration.
If a reverse proxy fronts the app, configure its addresses explicitly:

```sh
AUTH_TRUSTED_PROXIES="127.0.0.1/32,::1/128"
```

This example trusts a proxy on the same host. For Docker or remote proxies,
list their actual IPs or dedicated CIDR ranges instead. Invalid entries prevent
startup. The proxy must overwrite or append the actual connecting client's IP
to `X-Forwarded-For`. The app walks that chain from right to left, skipping only
trusted proxy addresses, and uses the first untrusted address. Malformed chains
fall back to the peer address. Do not trust arbitrary client networks or all IPs.
The setting affects authentication rate limits only; it does not configure
forwarded host or scheme handling.
