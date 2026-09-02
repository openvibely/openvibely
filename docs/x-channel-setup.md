# X (formerly Twitter) Channel Setup

This guide covers the supported X integration from `/channels`.

## Supported Scope

OpenVibely uses X API v2 with OAuth 1.0a user-context credentials to:

- Verify the authenticated X account with `GET /2/users/me`.
- Poll mentions of that account with `GET /2/users/:id/mentions`.
- Post assistant replies and explicit outbound posts with `POST /2/tweets`.

X API products and access tiers change independently of OpenVibely. Your developer app and account must have access to the endpoints above and read/write user authentication. OpenVibely does not create credentials, perform an authorization grant on your behalf, or bypass provider access restrictions.

## Configure X

1. Create or select an X developer app in the X Developer Portal.
2. Enable OAuth 1.0a user authentication with read and write permissions.
3. Generate a consumer key, consumer secret, access token, and access token secret for the account OpenVibely will use.
4. Open `/channels`, choose `X (formerly Twitter)`, and enter all four values.
5. Choose a polling interval from 15 to 300 seconds and save.
6. Use `Test connection` to verify the authenticated account.

Credential fields are write-only. Leaving a field blank preserves its saved value; saved secrets are never displayed by the settings page. Removing the integration deletes all four credentials and stops mention polling.

## Readiness and Failures

The channel is ready only after all credentials are present and X verifies the authenticated user. A configured card can still show disconnected when credentials are invalid, the app lacks endpoint access, the account's API tier does not support mention reads, or X is unavailable. In those states OpenVibely keeps polling stopped or reports the provider failure rather than accepting unauthenticated traffic.

Mention reads retry bounded rate-limit and temporary server responses. Tweet creation is not automatically retried after ambiguous transport or server failures because doing so could create duplicate posts. Polling and retry waits stop promptly during shutdown or reconfiguration.

## Authorize Inbound Users

Inbound mentions are denied by default. Add each allowed author's numeric X user ID under `Authorized mention authors for this project`. The allowlist is project-scoped; a user can switch only to another project where the same numeric X user ID is authorized.

Usernames are optional labels and are not used as security identities. Use the immutable numeric user ID from X.

OpenVibely durably records mention receipt IDs before handing work to the shared channel ingress path. Completed or in-flight receipts are not processed again, and the mention cursor advances only after the fetched batch has been durably handled.

## Replies and Outbound Posts

`Post assistant responses as replies` controls replies for X-created chat and task turns. X responses are limited to 280 weighted characters. OpenVibely follows X's entity accounting: recognized URLs count as 23 characters, emoji sequences count as one entity, and characters outside X's one-unit ranges count as two. Longer assistant output is truncated without splitting recognized entities.

For explicit `send_message` actions, create an outbound target with platform `X`, target ID `me`, and a project-local name such as `announcements`. X outbound targets intentionally support only the authenticated account (`x:me`); arbitrary usernames, user IDs, direct messages, and thread IDs are unsupported.

## Project and Task Behavior

Authorized mentions use the same channel ingress, runtime-action, task, queued-input, and cancellation lifecycle as other OpenVibely channels. The selected project is persisted per numeric X user, but it is revalidated against that project's allowlist for every new mention. Disabling or removing X stops polling; previously created OpenVibely tasks remain available in their projects.
