# Discord Channels Setup

This guide covers Discord setup for the OpenVibely `/channels` integration.

## What You Need

From Discord, you need:

- A Discord application with a bot user.
- The bot token copied from the Discord Developer Portal.
- The bot invited to the server where you want to use OpenVibely.
- Numeric Discord user IDs for every OpenVibely user allowed to interact with the bot.

You can also seed the bot token with `DISCORD_BOT_TOKEN`; saved channel settings take over after configuration in the UI.

## Create The Discord Bot

1. Open the [Discord Developer Portal](https://discord.com/developers/applications).
2. Click **New Application**, name it, and create it.
3. Open **Bot** in the left sidebar.
4. Click **Add Bot** if Discord has not already created one.
5. Under the token section, click **Reset Token** or **Copy Token**.
6. Save that token somewhere temporary and private so you can paste it into OpenVibely.

Never commit or paste the bot token into logs, issues, screenshots, or shared chat.

## Enable Bot Intents

In the Developer Portal **Bot** page, enable:

- **Message Content Intent** so OpenVibely can read normal Discord message text.

You usually do not need **Presence Intent**. Enable **Server Members Intent** only if your deployment later needs member lookup behavior.

## Invite The Bot To A Server

1. In the Developer Portal, open **OAuth2** -> **URL Generator**.
2. Select the `bot` scope.
3. Optionally select `applications.commands` if you plan to add slash-command support later.
4. Select bot permissions:
   - `View Channels`
   - `Send Messages`
   - `Read Message History`
   - `Create Public Threads` if you want thread support
   - `Send Messages in Threads` if you want thread support
5. Copy the generated URL, open it in your browser, and invite the bot to your server.

You need **Manage Server** permission in the Discord server to invite the bot.

## Configure OpenVibely

1. Start OpenVibely.
2. Open **Channels** from the System section of the sidebar.
3. Choose **Discord**.
4. Paste the bot token.
5. Click **Save Discord Settings**.
6. Use **Test Connection** to verify the saved token is valid.

`Test Connection` verifies Discord REST authentication. The bot showing online also requires the Discord Gateway websocket to be running. If the token was wrong when OpenVibely started, save the corrected Discord settings again or restart OpenVibely so the gateway reconnects.

## Add Authorized Users

Discord inbound access is system-level and deny-by-default. Messages are rejected until at least one authorized Discord user is added; once added, that user can use the Discord channel across OpenVibely projects, subject to normal project switching and project existence rules.

Use numeric Discord user IDs, not usernames or display names:

1. In Discord, open **User Settings** -> **Advanced**.
2. Enable **Developer Mode**.
3. Right-click the user and choose **Copy User ID**.
4. Paste that numeric ID into the OpenVibely Discord authorized-users list.

A value like `jamesdubee_53308` is a username/display handle and will not authorize inbound Discord messages. The expected value looks like a long number, for example `123456789012345678`.

## Outbound Targets

Chat can send outbound Discord messages through the `send_message` tool when you save Discord destinations in `Channels` -> `Outbound Message Targets`. Outbound Message Targets are project-scoped and separate from system-level inbound Authorized Users. There are two types of outbound targets:

- **Channel** – paste the Discord channel ID (a long numeric snowflake) and optionally a thread ID, then give it a friendly name such as `ops` so Chat can target `discord:#ops`. For explicit channel references use `discord:channel:<channel_id>` or `discord:channel:<channel_id>:<thread_id>`.
- **User DM** – paste a Discord user ID (the same long numeric snowflake) to allow agents to send direct messages to that user. User DM targets **do not** require the user to be in Authorized Users; Authorized Users control who can give instructions to OpenVibely, while Outbound Targets control where agents may send messages. To reference a saved user DM target explicitly, use `discord:user:<user_id>`.

The bot must have the **Direct Messages** intent and `dm_channel` write permission to send DMs. These sends reuse the configured Discord bot token and permissions.

**Important distinction:** Adding a Discord user ID as an Authorized User allows them to control OpenVibely by DMing the bot. Adding the same ID as a User DM Outbound Target allows agents to send proactive messages to them. These are independent settings.

## Test The Interaction

Start with a direct message because it avoids server-channel mention rules:

1. Confirm the bot appears online in Discord.
2. DM the bot: `hello, can you see this?`
3. Expect a short `Thinking...` acknowledgement followed by a final OpenVibely response.
4. In a server channel, mention the bot explicitly: `@YourBot hello from this channel`.
5. Reply to a bot/task message to test task-thread follow-up behavior.

Normal server-channel and thread messages are ignored unless they mention the bot.

## Message Behavior

OpenVibely handles Discord messages through the same shared chat/task lifecycle used by Slack and Telegram:

- DMs to the bot can start project chat turns.
- Guild channel and thread messages require a bot mention.
- Bot mentions are stripped before the prompt is sent to the chat runner.
- If a chat turn is already active, additional Discord messages are queued and promoted FIFO through the shared queued-turn path.
- In Orchestrate mode, Discord Chat can save project-scoped Automation workflows from maintained templates, descriptions, or reviewed Automation YAML; Plan-mode Automation previews remain read-only.

## Attachments

Discord chat messages can include attachments. OpenVibely downloads Discord CDN attachment URLs directly, stores them with the chat turn, shows them on the Chat page, and passes supported images to the chat runner for vision-capable models.

- Up to 3 files are processed per message.
- Each file must be 10 MB or smaller.
- Images are passed as image attachments.
- Small non-image files up to 100 KB are included inline as text context.
- Larger non-image files are stored with the chat turn but are not inlined.

Attachment reading requires the same Discord gateway access as normal messages: `Message Content Intent` enabled, plus channel permissions to view the channel and read message history.

## Task Follow-Ups

Discord-origin task-thread replies use the shared task-thread queueing and steering behavior:

- Replies before the first task execution starts are durably queued and applied when execution begins.
- Replies during an active execution are steered into that run when possible.
- Replies after completion create a normal follow-up execution.
- Task goals, selected memory, lifecycle routing, cancellation, and agent resolution follow the same semantics as other channel-origin task turns.

## Troubleshooting

If the bot is offline:

1. Verify the saved bot token is current. Regenerate and save a new token if Discord reports authentication failure.
2. Save the Discord channel settings again or restart OpenVibely after correcting the token.
3. Check logs for `[discord] gateway started`.
4. A log error like `websocket: close 4004: Authentication failed` means Discord rejected the token used for the Gateway session.
5. Remember that **Test Connection** can pass for the currently saved token even if the Gateway is still offline from an earlier failed startup.

If the bot is online but does not respond:

1. Verify `Message Content Intent` is enabled.
2. Verify the bot has `View Channels`, `Send Messages`, and `Read Message History` in the target channel.
3. Verify the Discord user is authorized with their numeric user ID in the system-level Discord Authorized Users list.
4. In guild channels and threads, mention the bot explicitly.
5. Confirm the OpenVibely Channels card shows the Discord Gateway as running, not only that a token is configured.
