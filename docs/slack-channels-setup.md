# Slack Channels Setup (Socket Mode + OAuth)

This runbook covers Slack setup for OpenVibely `/channels` integration and common local-dev OAuth issues.

## Required Slack App Values

From your Slack app config, you need:

- `Client ID`
- `Client Secret`
- `App-Level Token` (`xapp-...`) for Socket Mode
- Bot token (`xoxb-...`) only if you use manual override mode

OpenVibely uses Socket Mode, so the app-level token is always required.

## Required Slack App Configuration (Easy To Miss)

Even with valid tokens, OpenVibely will not receive messages unless Slack app settings are enabled correctly.

In your Slack app:

1. Enable `Socket Mode`
2. Create/use an app-level token with scope `connections:write` (`xapp-...`)
3. Enable `Event Subscriptions`
4. Subscribe to bot events:
   - `app_mention`
   - `message.im`
5. Reinstall the app to the workspace after changing scopes/events

If these are missing, you may see a valid token test but no message handling.

## Two Bot Token Modes in OpenVibely

In Slack modal, `Bot Token Source` has two options:

1. `OAuth Callback Token (Recommended)`
   - Click `Connect` on the Slack card.
   - Slack returns a bot token on callback and OpenVibely stores it.
2. `Manual Override Token`
   - Paste an `xoxb-...` token directly in `Bot Token Override`.
   - Useful when OAuth callback cannot be completed locally.

## Local OAuth: HTTPS Redirect Requirement

Some Slack apps enforce HTTPS-only redirect URLs.

Typical Slack UI errors:

- `redirect_uri did not match any configured URIs`
- `Please use a complete URL beginning with https (for security)`

If you see this while running locally (`http://localhost:3001/...`), use one of these:

1. Use an HTTPS public URL (tunnel) and add exact callback URL in Slack
   - Example callback: `https://<your-tunnel-domain>/channels/slack/callback`
   - In Slack app: `OAuth & Permissions` -> `Redirect URLs`
   - URL must match exactly (scheme, host, port, path, trailing slash).
2. Use `Manual Override Token` mode in OpenVibely for local dev.

## Quick Local Fallback (No OAuth Callback)

In `/channels` -> Slack modal:

1. Fill `Client ID`, `Client Secret`, and `App-Level Token (xapp-...)`
2. Set `Bot Token Source` = `Manual Override Token`
3. Paste `Bot Token Override (xoxb-...)`
4. Save and test connection

You can later switch `Bot Token Source` back to OAuth mode after setting up HTTPS callback.

## Troubleshooting: "Connected" But Bot Does Nothing

If you DM the bot or mention it and nothing happens:

1. Verify Socket Mode is turned on in Slack app settings
2. Verify Event Subscriptions are enabled with `app_mention` and `message.im`
3. Verify `xapp` and `xoxb` come from the same Slack app/workspace install
4. Reinstall app after any Slack scope/event changes

Important: OpenVibely's `Test` button validates bot auth/token health. It does not fully prove Socket Mode event delivery by itself.
