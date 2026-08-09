---
name: integrations_and_channels
type: project
created: 2026-05-09
updated: 2026-08-09
source: update_memory
source_id: b300a40908e1f0b835d6ead0aa77fd99:5acc267327f9dbda
confidence: high
title: Integrations and Channels
---

OpenVibely integrates with GitHub, Slack, Telegram, Discord, Email, generic inbound webhooks, and outbound message targets. Integration UIs separate discovery/add flows from management cards, render explicit connection states, and use consistent destructive `Delete` language with confirmation.

Generic inbound webhook gap:
- Open review-gated intake gap `#349`: inbound webhooks create real tasks and submit Active work when workers are available, but configuration lacks a “create in Backlog for human review” mode for less-trusted external events.

Shared channel direction:
- Channel-origin Chat and task-thread behavior stays aligned with web/API lifecycle, queueing, steering, cancellation, task-goal, agent-resolution, selected-memory, and swarm child follow-up rules. Canonical shared semantics live in `chat_thread_system.md` and `managed_memory.md`.
- Supported channel integrations keep task-goal controls at parity with web/API Chat where the surface supports them.
- `internal/service/channel_chat_ingress.go` owns the reusable production inbound Chat flow for Slack, Discord, Telegram, and Email, including attachment staging/linking, model selection after attachment classification, active-chat lookup/queue branching, first-turn task/execution creation, reply-context persistence, Chat Page broadcast, history assembly, runner invocation, and queued promotion.
- Shared generic channel image validation, pending-session IDs, unique temp filenames, MIME sniffing, and decoder imports belong in neutral channel ingress code rather than channel-specific files.
- `internal/service/chat_action_runtime.go` centralizes generic channel runtime handlers for task creation/edit/execution, task goals, task-thread viewing/sending, project/list utilities, schedule/personality/model utilities, completion, and shared alert/capability formatting.
- Slack, Telegram, Discord, and Email each own their `switch_project` authorization and active-project persistence callbacks. Channel-specific runtime tools must handle project switching so future messages persist under the correct channel identity.
- Shared runtime-tool merge in `processStreamingResponse` gives channel-provided tools precedence by name, then falls back to generic tools a partial channel runtime does not implement.
- Open channel task-thread runtime gap `#326`: shared Slack/Telegram/Discord/Email task-thread runtimes receive the caller task ID but do not resolve `task_id="current"` or omitted task references through it, so channel follow-up tools can fail with `task current not found` where equivalent web task-thread tools work.
- `internal/chatcontrol.DecodeRuntimeToolInput` is the production decoder for chat-action JSON inputs across web Chat, automation Chat, channels, GitHub runtime, outbound `send_message`, `list_tasks`, and `list_schedules`. Action validation/authorization/formatting remains at call sites.
- Runtime-tool-incapable provider/auth paths receive no channel action tools and no bracket-marker fallback.
- Slack, Telegram, Discord, and Email authorization UIs share templ primitives for list/container rendering and add-form wiring while preserving channel-specific copy, parsing, validation, repositories, and fragments.
- Authorized-user/sender handlers share a private generic CRUD helper and Echo adapter wrapper for configured-repository checks, project-ID extraction, list/load error mapping, mutation orchestration, authorization-record project lookup for deletion fallback, and reload. Channel callbacks retain transport-specific parsing and validation.
- Resolved issue `#317` / PR `#323`: Slack, Telegram, Discord, and Email authorized-user/sender HTTP adapters delegate common configured guard, project plumbing, CRUD dispatch shape, and delete fallback wiring to the shared authorized-user helper while preserving platform-specific parsing and validation.
- Authorization repositories share unexported SQL helpers for common delete/count/user-project/task-context operations where schemas align. Slack's composite-key user-project repo intentionally remains a documented exception.
- Telegram, GitHub, Slack, Discord, and Email removal handlers share ordered settings reset behavior; reset failures return controlled server errors without successful refresh/redirect.
- Open duplication gap `#335`: multiple Channels/settings configure, disconnect, and remove handlers repeat the same successful completion response branch: HTMX requests trigger a Channels refresh, while non-HTMX requests redirect to `/channels`. Consolidate only that response tail into a small private helper while preserving channel-specific cleanup/reset/error behavior.
- Resolved issue `#311` / PR `#322`: Slack, Discord, and Email connection-test handlers share standard success/failure feedback rendering while preserving channel-specific service checks and Telegram's richer distinct feedback.
- Attachment-bearing channel turns should choose the model config after downloads and byte classification so persisted execution or queued `thread_inputs.AgentConfigID` matches actual runner config.
- Resolved issue `#279` / PR `#309`: queued Slack, Email, and Discord task-context persistence is centralized in the dedicated task-context repositories. `ThreadInputRepo` delegates through transaction-executor upsert methods instead of owning channel-specific inline upsert SQL.

Outbound message targets:
- `send_message` is a first-class outbound runtime tool for Slack, Telegram, Discord, and Email. It sends through existing channel services/configuration, not duplicate credentials, and records project-scoped send audits.
- Outbound targets are distinct from inbound authorization allowlists. Default policy requires saved targets or a saved home target; arbitrary explicit targets require project setting `send_message_allow_explicit_targets`.
- Saved target kinds are `channel`, `user`, `chat`, and `email`. Slack/Discord user-DM destinations are first-class saved targets using `slack:user:<id>` / `discord:user:<id>`.
- Backward compatibility: project-authorized Slack/Discord user IDs can be outbound DMs for that same project when no saved target exists. Cross-project authorized IDs must fail closed rather than falling through to explicit raw send, even when explicit targets are enabled.
- `Home` marks a platform/project default destination for tool calls naming only the platform. It does not authorize inbound users or configure credentials.
- Outbound Message Targets is a permanent top Channels card. The card opens editable controls but exposes no delete/remove action itself.
- The edit dialog stages policy toggles, Add Target, and per-row Delete until Save Settings. Cancel/X/backdrop/ESC discard drafts by reloading persisted state.
- Add target type is platform-aware: Slack/Discord expose Channel and User DM; Telegram is chat/channel; Email is email.
- Save Settings submits the full staged list plus policy and reconciles atomically. Immediate add/delete routes should not persist independently of the dialog save contract.
- Per-target Test is immediate and non-mutating. Draft targets can be tested through a non-persisting path that sends only the fixed OpenVibely test message; progress/completion stays inside the Test button.
- Target names are optional. Multiple unnamed targets are valid; duplicate non-empty names are invalid per project/platform; duplicate destinations are invalid per project/platform/kind/target/thread.
- Reconciliation deletes omitted rows before upserting submitted rows so delete/save/re-add does not hit unique constraints.
- Outbound target modal mutation fragments stay modal-only. Top card refreshes separately after Save Settings.
- Save path enforces at most one Home target per project/platform by clearing existing homes. If duplicate homes appear outside app paths, `send_message` resolves the most recently updated home.
- Per-target actions must enforce project ownership for saved target IDs and preserve displayed `project_id` context. Cross-project target IDs must not dispatch messages or move targets.
- Open consolidation gap `#296`: `ChannelTargetRepo.Upsert` and `ReplaceProjectTargets` duplicate per-target normalization, home clearing, and upsert SQL; consolidate with a private helper while preserving bulk reconciliation behavior.

GitHub integration:
- Stored task PRs are routed from strictly parsed persisted `https://{host}/{owner}/{repo}/pull/{number}` URLs. Host must match selected project repository host and embedded number must match persisted PR number; malformed/foreign/query/fragment/number-mismatched records are skipped without blocking valid ones.
- Enterprise PR references inherit the selected project's custom API base URL. Fetch, deduplication, and persistence use each PR record's repository identity, compared case-insensitively and canonicalized lowercase for new rows.
- GitHub issue read/comment/label operations share authenticated JSON request helpers; operation-specific validation/endpoints/payloads stay at call sites.
- Interactive Chat handlers and Automation runtimes share a canonical issue-action contract and service-level core for inbox, authorization, issue reads/comments/labels, PR-associated listing, and assigned-issue operations. Automation-only filtering/provenance/duplicate prevention/PR behavior stays outside the generic core.
- Manually created OpenVibely tasks that reference an existing GitHub issue should use the ordinary task PR flow and do not need GitHub SDLC Automation issue-task provenance. The provenance guard applies only to tasks running as GitHub SDLC Automation implementation tasks.
- Explicit-assignee list tools require the assignee to be a configured GitHub Authorized User before repository resolution/provider calls. PAT-owner scanning uses `github_list_my_assigned_issues`.
- Open stale-PR gap `#233`: ordinary task PR records are not reconciled with GitHub after creation, so closed/merged remote PRs can still receive forwarded feedback or be reported reusable. A 2026-08-08 Loop Auditor pass found 15 local `task_pull_requests` rows marked open even though GitHub showed those PRs closed: `#37`, `#185`, `#186`, `#187`, `#188`, `#189`, `#190`, `#191`, `#280`, `#282`, `#283`, `#307`, `#308`, `#309`, and `#310`.
- Current GitHub SDLC hygiene findings from 2026-08-08: a Loop Auditor pass found stale closed-issue workflow metadata has grown to 71 closed issues still carrying workflow labels (`task-created=71`, `in-progress=71`, `pr-opened=6`, `blocked=3`, `needs-human=3`), and all 71 are still assigned to authorized inbox identities (`dubee` or `openvibely`). Human-review notification `98875c5d916956f46a24f85964c43274` covers this cleanup class but is stale/incomplete because it recorded 65 closed labeled issues, 47 closed assigned issues, and 11 stale local PR rows. A later GitHub Dev Inbox run on 2026-08-08 found assigned open issues `#321`, `#314`, `#311`, and `#317`, created distinct Active implementation tasks for each, set the required implementation/audit lifecycle goal, and applied unprefixed `task-created` plus `in-progress` labels; PR feedback routing scanned 14 OpenVibely-created task PRs and forwarded no new authorized feedback.
- Open duplication gaps: PR-feedback forwarding assembly across interactive/Automation runtimes (`#167`), the narrower PR-feedback forwarding runtime-tool wrapper duplication between the web handler and Automation runtime paths (`#302`), and ordinary vs Automation service entry points for opening/replacing PR branches (`#203`).
- `github_open_pull_request` and `github_replace_pull_request_branch` share private target resolution for task selectors, current-project ownership, project loading, and Automation repository validation while retaining mutation-specific decoding and confirmations. After the 2026-08-08 stale-origin fix, PR publication is the only stale-origin GitHub mutation allowed to use durable Automation issue-task provenance after graph replacement; branch replacement and other writes still require current Automation graph authorization.
- Shared paginated GitHub reads cover PR issue comments, reviews, review comments, assigned issues, and issue-to-PR cross-reference lookup. They enforce authenticated headers, API error decoding, same-origin Link traversal, cycle prevention, and fail whole reads on later-page errors.
- `ListPullRequestFeedback` fetches issue comments, reviews, and review comments concurrently through one cancellable context, merges in fixed source order, stably sorts by creation time, and returns no partial feedback on first source error.
- Scheduled GitHub Dev Inbox treats assignment to the PAT owner or configured Authorized User as approval to implement. It does not require an approved label, existing PR, or prior Automation-created/mapping owner row. Manual assigned issues are eligible; GitHub issue mailbox-owner mappings were retired on 2026-08-08 and migration 145 deletes existing `github_issue` owner rows.

Slack facts:
- Slack uses shared channel ingress/runtime behavior and project-aware active-project persistence.
- Slack removal preserves `SlackSettingBotTokenSource=oauth` while resetting the rest of the configured channel state.
- Slack's authorization model remains allow-by-default when no authorized-user list exists, unlike Email and Discord deny-by-default behavior.
- Open duplication gap `#81`: Slack and Telegram independently build the same Markdown completion payload for direct replies and persisted task contexts; consolidate formatting while keeping routing/settings/transport channel-specific.

Discord facts:
- Discord is a first-class bot/gateway integration with Settings configure/test/delete UI, migrations, docs, and `github.com/bwmarrin/discordgo`.
- Discord authorized-user enforcement is project-scoped and deny-by-default. Authorized entries must use numeric Discord user IDs copied with Developer Mode.
- Inbound Discord supports DMs and bot-mentioned guild/channel/thread messages. Non-DM messages must mention the bot; default channel IDs, free-response channel allowlists, and require-mention toggles are unsupported/ignored.
- Discord project switching persists active project per Discord user. Failed persistence preserves prior cache/default state; nil-repository path is intentionally in-memory-only.
- Discord attachments use shared channel attachment flow with trusted CDN/media validation, image persistence, distinct duplicate filenames, and queued pending sessions.
- Discord replies preserve channel/thread/message/user metadata for queued promotion and completion callbacks. Initial Chat stores the bot acknowledgement so final response can edit `Thinking...`.
- Discord outbound `send_message` uses saved/explicit/home target policy. Saved targets use `channel` for channel/thread IDs and `user` for DMs; thread IDs are destination channels, not reply message IDs.
- Open cancellation defect `#183`: outbound APIs discard accepted contexts, so pre-cancel and mid-send cancellation may not stop transport calls.
- Discord user IDs and channel IDs are both numeric; bare numeric targets resolve only as saved targets or authorized user DMs. Unsaved explicit channel sends require `discord:channel:<channel_id>` or `discord:channel:<channel_id>:<thread_id>` when explicit targets are enabled.
- REST token validation only proves the token is valid; connection status should treat Discord as connected only when Gateway is running and surface last Gateway start error when configured but offline.
- Deleting Discord clears the current project's authorized-user allowlist. Authorized-user Add/Delete controls inside the settings modal update only the modal fragment and should not close/save main settings.

Telegram facts:
- Telegram attachment and command behavior is project-aware and uses shared runner/queued task-thread production paths.
- Startup-created and Settings-created Telegram services need equivalent shared-runner, AgentRepo, and queued-promoter wiring.
- Telegram polling advances update offset only after terminal handling or durable handoff confirms execution/queued-input persistence. Failed handoff remains retryable and stops later updates in the batch.
- Open attachment-ingress cancellation defect `#195`: attachment transfer uses `http.Get` instead of the processing context, so stalled downloads can block durable handoff and the sequential poller.
- Telegram messages with no `Message.From`, including sender-chat/channel posts, are terminally ignored and acknowledged before authorization/project selection/ingress.
- Telegram active-project cache is synchronized; per-user generations prevent slow DB/default cache population from overwriting newer explicit `switch_project`.
- Explicit project switches and `/start` default initialization persist before updating cache. Failed persistence leaves persisted/cache selection unchanged and surfaces failure. Nil-repository path is intentionally in-memory-only.
- Telegram authorization fails closed on configured-repository lookup errors across project-specific, global, and cross-project fallback checks.
- Telegram cleans downloaded temp attachments on post-download failures and byte-sniffs vague `application/octet-stream` image bytes.
- Telegram outbound `send_message` supports saved chat IDs plus optional topic/thread IDs.
- Telegram `Start`/`Stop` are nil-safe. `TelegramService` lifecycle is serialized through one operation mutex, and relaunch waits for old `GetUpdatesChan` poller drain to avoid duplicate poller conflicts.
- Telegram Rich Messaging V2 is default-on and exposed in Settings. Existing blank setting resolves enabled unless explicitly saved false.
- When Rich V2 is disabled, sends/edits use escaped MarkdownV2 first, then raw plain text fallback if Telegram rejects MarkdownV2.
- Rich outbound delivery uses Telegram rich payloads with MarkdownV2/plain fallback only for clear rich rejection; ambiguous transport errors stop to avoid duplicate visible delivery.
- Rich draft previews and final delivery coordinate per acknowledgement message. Final delivery stops active preview, avoids duplicate surfaces, persists/falls back only when appropriate, and clears stale `Thinking...` placeholders only after complete fallback success.
- Telegram Desktop for macOS 12.8 can crash on Rich V2 payloads beginning with GitHub-style pipe tables. The user rejected an automatic table fallback; do not assume that workaround is desired without re-approval.

Email facts:
- Email is a first-class channel with IMAP inbound polling, SMTP outbound replies, provider presets, settings UI, project-scoped authorized senders, inbound attachments, and shared runner/queueing behavior.
- Email reply context is durable: queued inputs and email-origin Chat tasks preserve message/thread headers so SMTP responses remain threaded after async promotion/completion.
- Email Chat sessions are scoped by normalized sender plus thread root/message ID, falling back to subject hash when threading headers are absent; do not collapse all mail from one sender into one global chat.
- Email authorized-sender enforcement is project-scoped and deny-by-default.
- Email sender identity normalization is centralized in `repository.NormalizeEmailAddress`; it backs authorization, sender-project persistence, session keys, and self-sender checks.
- Email project switching authorizes normalized mailbox against target project's Email authorized senders and persists selection per sender. Active-project lookup revalidates saved choice and falls back to scanning authorized projects.
- Preset-provider app passwords are normalized by removing whitespace on save/load; custom-provider passwords preserve internal spaces.
- Email Settings use shared masked secret inputs. Saved passwords render into the field for reveal/hide; blank submit preserves stored secret.
- Authorized-sender controls are embedded in the Email settings form but persist only through explicit Add using `authorized_email_address`; Save Email Settings must not add a typed sender.
- Email settings booleans use DaisyUI toggle styling while preserving checkbox semantics.
- Email outbound `send_message` sends new SMTP email without reply headers. Optional/default subjects are explicit; blank subjects default conservatively.
- Email runtime config loads one coherent current snapshot of all settings through `SettingsRepo.GetMany` per SMTP-reaching operation.
- Open SMTP cancellation defect `#162`: production SMTP calls block in `smtp.SendMail` without honoring context, so pre-canceled/timed-out operations may remain blocked or deliver after abandonment.
- Email reply headers sanitize printable angle-bracket message IDs, reject injection/control input, bound and fold `References`, and fold `In-Reply-To` within RFC line-length limits.
- Email inbound attachment parsing decodes MIME attachments, honors skip settings, byte-sniffs/stages files through shared chat ingress, supports model vision selection, links first-turn executions, queues pending sessions, and includes text attachment context.
- Attachment-bearing emails with empty bodies should be processed; empty-body/no-attachment messages remain ignored.
- Email inbound receipt insertion is atomic with durable execution or queued input writes. Existing receipts suppress duplicate work after interruption or IMAP `Seen` failure. No-`Message-ID` receipts use mailbox `UIDVALIDITY` plus UID; if neither identity is valid, leave unread rather than false-deduplicate.

