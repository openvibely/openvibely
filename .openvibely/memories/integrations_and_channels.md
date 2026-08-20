---
name: integrations_and_channels
type: project
created: 2026-05-09
updated: 2026-08-19
source: after_complete
source_id: e66c64236d6c3b8344566d00dbb75c41:3e665d862de2f852
confidence: high
title: Integrations and Channels
---

OpenVibely integrates with GitHub, Slack, Telegram, Discord, Email, generic inbound webhooks, and outbound message targets. Integration UIs separate discovery/add flows from management cards, render explicit connection states, and use consistent destructive `Delete` language with confirmation.

Generic inbound webhooks:
- Open review-gated intake gap `#349`: inbound webhooks can create and submit Active tasks but lack a Backlog-for-human-review mode for less-trusted events.
- Open Automation-trigger gap `#665`: inbound webhooks are Channels integrations that create standalone Active tasks, but are not Automation trigger nodes/resources.
- Open project-boundary defect `#373`: webhook update/delete/rotate/test routes must enforce selected-project ownership for webhook IDs.
- Webhook delivery and Settings `Test` should share private webhook task create/assign/submit logic while preserving separate inbound authentication/parsing and UI rendering.
- Inbound webhook create/update Settings saves should share form normalization for name, enabled, system instructions, default priority, prompt templates, and ordered agent IDs, while preserving create/update-specific behavior.
- Inbound webhook compact cards should keep sensitive/detail-only fields, prompts/templates, secrets, and agent assignments out of card attributes; Edit modals lazy-load authorized full detail and disable Save until hydration succeeds.
- Open template substitution bug `#622`: webhook templates only replace tight placeholders like `{{event_type}}`; spaced variants such as `{{ event_type }}` remain verbatim. Pattern Library already handles this style.

Shared channel direction:
- Channel-origin Chat and task-thread behavior stays aligned with web/API lifecycle, queueing, steering, cancellation, task goals, agent resolution, selected memory, and swarm child follow-up rules. Canonical task/thread semantics live in `chat_thread_system.md`.
- `internal/service/channel_chat_ingress.go` owns reusable inbound Chat flow for Slack, Discord, Telegram, and Email, including attachment staging/linking, model selection, active-chat lookup/queue branching, first-turn task/execution creation, reply context, broadcasts, history assembly, runner invocation, and queued promotion.
- Shared generic channel image validation, pending session IDs, unique temp filenames, MIME sniffing, and decoder imports belong in neutral channel ingress code.
- `internal/service/chat_action_runtime.go` centralizes generic channel runtime handlers for task creation/edit/execution, goals, task-thread viewing/sending, project/list utilities including GitHub-backed `create_project`, schedule/personality/model utilities, completion, alerts, and capability formatting.
- Open `#699` channel wiring gap: Slack, Telegram, and Discord channel runtimes advertise `create_project` but currently omit the `CreateGitHubProjectRuntimeOptions` dependencies when building project handlers, so execution returns `project service is not configured`; Email shows the intended dependency wiring.
- The read-only `list_channels` action is the Chat/control-plane path for compact channel readiness: it summarizes GitHub, Slack, Telegram, Discord, Email, inbound webhook counts, outbound target counts by platform/kind, and explicit-target policy using prompt-safe booleans/counts/status strings only. Web/API Chat uses the full handler summary; Slack, Discord, and Telegram channel runtimes wire service-side handlers so advertised tools are covered.
- Resolved `#684`: Slack/Discord/Telegram service-side `list_channels` aligns GitHub status with Web/API behavior for GitHub App installations. Channel runtime GitHub App mode reports connected from stored installation state, does not use a PAT fallback to mark explicit App mode connected, and includes only prompt-safe account login/type metadata, not secrets.
- Resolved `#684` all-surface completeness: Slack/Discord/Telegram channel-provided `list_channels` handlers wire `EmailStatus`, `EmailAuthRepo`, `WebhookRepo`, and outbound target stores into the service-side summary, so channel surfaces report Email/Webhook/outbound target status and counts consistently with Web/API Chat without exposing passwords, webhook path tokens/secrets, or raw target credentials.
- Channel task-creation callback assembly should stay DRY across Slack, Discord, and Telegram while preserving platform-specific callbacks.
- Slack, Telegram, Discord, and Email own their `switch_project` authorization and active-project persistence callbacks. Slack, Discord, and Email use shared channel runtime switch handler rather than Telegram-style direct `/project` commands.
- Channel-provided runtime tools take precedence by name, then fall back to generic tools a partial channel runtime does not implement.
- Runtime-tool-incapable provider/auth paths receive no channel action tools and no bracket-marker fallback.
- `internal/chatcontrol.DecodeRuntimeToolInput` is the production decoder for chat-action JSON inputs across web Chat, Automation Chat, channels, GitHub runtime, outbound `send_message`, `list_tasks`, and `list_schedules`.
- Authorized-user/sender handlers share generic CRUD helper patterns where schemas align; Slack's composite-key user-project repo is an intentional exception.
- Open channel task-thread runtime gap `#326`: Slack/Telegram/Discord/Email task-thread runtimes receive caller task ID but do not resolve `task_id="current"` or omitted task references through it.
- Open scheduled capability gap `#341`: scheduled tasks can advertise broader tools through `list_capabilities` than the executor allows.

Outbound message targets:
- `send_message` is a first-class outbound runtime tool for Slack, Telegram, Discord, and Email. It sends through existing channel services/configuration and records project-scoped send audits.
- Outbound targets are distinct from inbound authorization allowlists. Default policy requires saved targets or saved home target; arbitrary explicit targets require project setting `send_message_allow_explicit_targets`.
- Saved target kinds are `channel`, `user`, `chat`, and `email`. Slack/Discord user-DM destinations are first-class saved targets.
- Backward compatibility: project-authorized Slack/Discord user IDs can be outbound DMs for that same project when no saved target exists. Cross-project authorized IDs must fail closed.
- `Home` marks a platform/project default destination for platform-only calls. It does not authorize inbound users or configure credentials.
- Outbound Message Targets is a permanent top Channels card. Its edit dialog stages policy toggles, Add Target, and per-row Delete until Save; cancel/X/backdrop/ESC discard drafts by reloading persisted state.
- Per-target Test is immediate and non-mutating. Draft targets can be tested through a non-persisting path using only the fixed OpenVibely test message.
- Target names are optional; multiple unnamed targets are valid. Duplicate non-empty names and duplicate destinations per project/platform/kind/target/thread are invalid.
- Reconciliation deletes omitted rows before upserting submitted rows. Save enforces at most one Home target per project/platform by clearing existing homes; duplicate homes outside app paths resolve most-recently-updated home.
- Per-target actions must enforce project ownership for saved target IDs and preserve displayed `project_id` context.
- Open consolidation gap `#296` and broader `#496`: saved-target form, test-send, runtime resolution, and persistence normalization/validation should be consolidated without changing policy.
- Issue `#535`: outbound target resolution uses lookup indexes for home, name, and target/thread queries; preserve query-plan coverage and saved-target/explicit-target behavior.
- Open correctness defect `#667`: Discord typed targets can resolve wrong saved target row because legacy lookup ignores `target_kind`. Colliding numeric channel/user IDs need typed send regressions.

GitHub integration:
- Stored task PRs are routed from strictly parsed persisted `https://{host}/{owner}/{repo}/pull/{number}` URLs. Host must match selected project repository host and embedded number must match persisted PR number; malformed/foreign/query/fragment/number-mismatched records are skipped.
- Enterprise PR references inherit selected project's custom API base URL. Fetch, dedupe, and persistence use each PR record's repository identity case-insensitively.
- GitHub issue read/comment/label operations share authenticated JSON request helpers; operation-specific validation/endpoints/payloads stay at call sites.
- Interactive Chat and Automation runtimes share canonical issue-action service core for inbox, authorization, reads/comments/labels, PR-associated listing, and assigned-issue operations. Automation-only filtering/provenance/duplicate prevention/PR behavior stays outside generic core.
- Model-facing `github_create_issue` no longer advertises `idempotency_key`; generic Chat and SDLC finder prompts should not ask the model to invent one.
- Manually created OpenVibely tasks referencing an existing GitHub issue use ordinary task PR flow and do not need GitHub SDLC Automation issue-task provenance.
- Explicit-assignee list tools require assignee to be configured GitHub Authorized User before provider calls. PAT-owner scanning uses `github_list_my_assigned_issues`.
- Scheduled GitHub Dev Inbox treats assignment to PAT owner or configured Authorized User as approval to implement. It does not require approved label, existing PR, or prior Automation mapping. Workflow labels should stay unprefixed, such as `task-created`, `in-progress`, `blocked`, `needs-human`, and `pr-opened`.
- Assigned-issue list entries skip detail hydration only when raw GitHub JSON is explicitly complete for task creation; incomplete/unknown entries hydrate that issue after repository/issue deduplication.
- Open stale-PR gap `#233`: ordinary task PR records are not reconciled with GitHub after creation, so closed/merged remote PRs can still receive forwarded feedback or be reported reusable.
- GitHub SDLC hygiene gap: closed issues can retain workflow labels/authorized-assignee state and stale local PR records can mislead inbox/review workflows. Verify live GitHub PR/issue state and current OpenVibely task records before continuing/deduplicating inbox issues.
- Open duplication gaps include PR-feedback forwarding assembly (`#167`), PR-feedback runtime wrappers (`#302`), ordinary vs Automation open/replace PR entry points (`#203`), GitHub plugin source normalization (`#425`), and duplicate repository resolution before `TaskPullRequestService.OpenForTask` (`#561`).
- Direct and Automation GitHub tools share repository-selection logic for explicit repo URL, selected project repo, endpoint scoping, and Automation-bound behavior. Automation-bound tools ignore model-supplied repo overrides for the selected project, allow same-host Enterprise explicit repositories, and reject cross-host endpoint overrides.
- PR open/replace tools share target resolution for task selectors, project ownership/loading, and Automation repository validation.
- Open task-thread PR bug `#536`: PR tools are available in follow-ups and resolve explicit `task_id="current"`, but omitted task selectors still fail.
- PR opening publishes the branch, records published remote head SHA, live-verifies stored open PR rows before reuse, ignores closed branch PRs, and creates/reuses only when live PR is open on the task branch/repository and `head.sha` matches recorded published head.
- PR publication is the only stale-origin GitHub mutation allowed to use durable Automation issue-task provenance after graph replacement. Branch replacement and other writes require current graph authorization.
- Publication/review state is volatile: verify live `main`, PR head/file list/checks, issue closure, and task PR publication evidence before claiming workflow completion.
- Shared paginated GitHub reads cover PR issue comments, reviews, review comments, assigned issues, and issue-to-PR cross-reference lookup with authenticated headers, API error decoding, same-origin Link traversal, cycle prevention, and whole-read failure on later-page errors.
- `ListPullRequestFeedback` fetches issue comments, reviews, and review comments concurrently through one cancellable context, merges fixed source order, stably sorts by creation time, and returns no partial feedback on first source error.

Slack facts:
- Slack uses shared channel ingress/runtime behavior and project-aware active-project persistence.
- Slack removal preserves `SlackSettingBotTokenSource=oauth` while resetting other configured channel state.
- Slack authorization remains allow-by-default when no authorized-user list exists, unlike Email and Discord deny-by-default.
- Open duplication gap `#81`: Slack and Telegram independently build similar Markdown completion payloads for direct replies and persisted task contexts.

Discord facts:
- Discord is a first-class bot/gateway integration with Settings configure/test/delete UI, migrations, docs, and `discordgo`.
- Discord authorized-user enforcement is project-scoped and deny-by-default. Authorized entries use numeric Discord user IDs copied with Developer Mode.
- Inbound Discord supports DMs and bot-mentioned guild/channel/thread messages. Non-DM messages must mention the bot; default channel IDs, free-response allowlists, and require-mention toggles are unsupported/ignored.
- Discord project switching persists active project per user. Failed persistence preserves prior cache/default state; nil repository path is in-memory-only.
- Discord attachments use shared channel attachment flow with trusted CDN/media validation, image persistence, distinct duplicate filenames, and queued pending sessions.
- Discord replies preserve channel/thread/message/user metadata for queued promotion and completion callbacks.
- Discord outbound `send_message` uses saved/explicit/home policy. Saved targets use `channel` for channel/thread IDs and `user` for DMs; thread IDs are destination channels, not reply message IDs.
- Open cancellation defect `#183`: outbound APIs discard accepted contexts, so pre-cancel/mid-send cancellation may not stop transport calls.
- Discord user IDs and channel IDs are numeric; bare numeric targets resolve only as saved targets or authorized user DMs. Unsaved explicit channel sends require typed `discord:channel:` syntax when explicit targets are enabled.
- REST token validation only proves token validity; connected status should require Gateway running and surface last Gateway start error when configured but offline.
- Deleting Discord clears current project's authorized-user allowlist. Authorized-user Add/Delete controls inside settings modal update only modal fragment and should not close/save main settings.

Telegram facts:
- Telegram attachment and command behavior is project-aware and uses shared runner/queued task-thread paths.
- Startup-created and Settings-created Telegram services need equivalent shared-runner, AgentRepo, and queued-promoter wiring.
- Telegram polling advances update offset only after terminal handling or durable handoff confirms execution/queued-input persistence; failed handoff remains retryable and stops later updates in the batch.
- Open attachment-ingress cancellation defect `#195`: attachment transfer uses `http.Get` instead of processing context.
- Telegram messages with no `Message.From` are terminally ignored and acknowledged before authorization/project selection/ingress.
- Telegram active-project cache is synchronized; per-user generations prevent slow DB/default cache population from overwriting newer explicit switches.
- Telegram `/project`, natural-language project switching, and runtime `switch_project` should share one project-selection core while preserving command/runtime formatting.
- Explicit project switches and `/start` default initialization persist before updating cache; failed persistence leaves persisted/cache selection unchanged.
- Telegram inbound authorization is system-wide despite active project selection; repository errors fail closed.
- Username-only inbound authorization should use the lowercased unknown-ID index while preserving numeric-ID authorization and username backfill.
- Telegram cleans downloaded temp attachments on post-download failures and byte-sniffs vague image bytes.
- Telegram outbound `send_message` supports saved chat IDs plus optional topic/thread IDs.
- Telegram service `Start`/`Stop` are nil-safe and serialized through one operation mutex; relaunch waits for old poller drain.
- Telegram Rich Messaging V2 is default-on in Settings. Blank existing setting resolves enabled unless explicitly saved false.
- When Rich V2 is disabled, sends/edits use escaped MarkdownV2 first, then raw plain text fallback if rejected.
- Rich outbound delivery uses rich payloads with MarkdownV2/plain fallback only for clear rich rejection; ambiguous transport errors stop to avoid duplicate delivery.
- Telegram Desktop for macOS 12.8 can crash on Rich V2 payloads beginning with GitHub-style pipe tables. The user rejected automatic table fallback; do not assume it without re-approval.

Email facts:
- Email is a first-class channel with IMAP polling, SMTP replies, provider presets, settings UI, project-scoped authorized senders, inbound attachments, and shared runner/queueing behavior.
- Email reply context is durable: queued inputs and email-origin Chat tasks preserve message/thread headers so SMTP responses remain threaded after async promotion/completion.
- Email Chat sessions are scoped by normalized sender plus thread root/message ID, falling back to subject hash when threading headers are absent; do not collapse all mail from one sender into one global chat.
- Inbound parsing captures `In-Reply-To` from IMAP envelope and MIME headers. Session keys prefer `References`, then `In-Reply-To`, then current `Message-ID`/subject.
- Email authorized-sender enforcement is project-scoped and deny-by-default.
- Email address normalization is centralized in `repository.NormalizeEmailAddress` and backs authorization, sender-project persistence, session keys, and self-sender checks.
- Email project switching authorizes normalized mailbox against target project's authorized senders and persists selection per sender. Active-project lookup revalidates saved choice and falls back to scanning authorized projects.
- Preset-provider app passwords are normalized by removing whitespace on save/load; custom-provider passwords preserve internal spaces.
- Email Settings use shared masked secret inputs. Saved passwords render into the field for reveal/hide; blank submit preserves stored secret.
- Authorized-sender controls persist only through explicit Add using `authorized_email_address`; Save Email Settings must not add a typed sender.
- Email settings booleans use DaisyUI toggle styling while preserving checkbox semantics.
- Email outbound `send_message` sends new SMTP email without reply headers. Optional/default subjects are explicit; blank subjects default conservatively.
- Email runtime config loads one coherent settings snapshot through `SettingsRepo.GetMany` per SMTP-reaching operation.
- Open SMTP cancellation defect `#162`: production SMTP calls block in `smtp.SendMail` without honoring context.
- Email reply headers sanitize printable angle-bracket message IDs, reject injection/control input, bound/fold `References`, and fold `In-Reply-To` within RFC line-length limits.
- Email inbound attachments decode MIME attachments, honor skip settings, byte-sniff/stage through shared ingress, support vision model selection, link first-turn executions, queue pending sessions, and include text attachment context.
- Attachment-bearing emails with empty bodies should be processed; empty-body/no-attachment messages remain ignored.
- Inbound receipt insertion is atomic with durable execution or queued input writes. Existing receipts suppress duplicate work after interruption or IMAP `Seen` failure. No-`Message-ID` receipts use mailbox `UIDVALIDITY` plus UID; if identity is invalid, leave unread rather than false-deduplicate.
- Open performance gap `#529`: accepted inbound email polling can touch the same receipt row up to three times; optimize while preserving durable handoff atomicity and retry semantics.
- Outbound Email chat/task replies generate app-owned `Message-ID` headers and persist project-scoped normalized-sender aliases so replies with only `In-Reply-To` to an app-owned response ID resolve back to the original session.
