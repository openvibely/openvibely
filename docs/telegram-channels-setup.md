# Telegram Channels Setup

This guide covers Telegram setup from `/channels`.

## What Telegram Channel Is For

Telegram channel integration lets you interact with OpenVibely from Telegram instead of only from the web UI.

Why teams use it:

- Create or manage tasks from mobile/chat.
- Ask project questions quickly without opening the app.
- Receive task completion/failure notifications where you already communicate.

## What You Need

- A Telegram bot token from [BotFather](https://t.me/BotFather).
- A selected project in OpenVibely (for project-scoped authorized users).

## Add Telegram Channel

1. Open `/channels`.
2. Click `+ Add Channel`.
3. Choose `Telegram Bot`.
4. Paste your bot token.
5. Click `Save & Start Bot`.

After saving, the Telegram card should show connected/running.

## Test Connection

1. Open the Telegram card menu (kebab button).
2. Click `Test Connection`.
3. Confirm you see `Connection successful!`.

If it fails, verify token correctness and that the bot service is running.

## Authorized Users (Project Scoped)

Use `Authorized Users` in the Telegram modal to control who is allowed to interact with the bot for the selected project.

- Add users by Telegram username and/or Telegram user ID.
- Remove users from the list when access should be revoked.

Why this matters:

- Prevents unauthorized users from creating/running tasks.
- Keeps project automation scoped to the right people.

Important behavior: if no authorized users are configured, bot requests are blocked until users are added.

## Notification Setting

`Send task responses to Telegram` controls whether Telegram-created tasks send completion/failure responses back to the creator.

Turn it on when you want async feedback in Telegram. Turn it off if notifications are noisy.

## How Users Interact With the Bot

Users can send normal natural language messages (same idea as `/chat`).

Optional slash commands shown in UI:

- `/start`
- `/projects`
- `/switch <project_id>`

## Troubleshooting

- `Connection failed: Bot is not running`: Save token again from the Telegram modal.
- Bot receives messages but does nothing: verify the sender is in `Authorized Users` for the selected project.
- Wrong project context: use `/projects` and `/switch` from Telegram.
