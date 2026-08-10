---
name: usage_analytics
type: project
created: 2026-06-03
updated: 2026-08-10
source: consolidation
source_id: memory_consolidation_2026_08_10
confidence: high
title: Usage Analytics
---

OpenVibely usage analytics are local operational analytics, not third-party product telemetry. Third-party frontend tracking such as PostHog, Segment, Mixpanel, or Google Analytics is outside the intended architecture.

Durable analytics model:
- Persistent model-usage analytics exist for Anthropic and OpenAI OAuth/API-key paths, plus OpenAI-compatible Chat Completions API-key configs.
- OpenAI-compatible Chat Completions usage is parsed by the shared client/adapter into canonical input/output/total/cached/reasoning fields and persisted through `RecordUsageFromResult` for `ProviderOpenAICompatible` API-key configs.
- Skill analytics events are stored locally in `skill_analytics_events`.
- Usage rows are stored locally in `llm_usage_events`.
- Analytics date/hour buckets reflect current app/local timezone semantics at query time, matching Schedules. The `#334` direction avoids persisted `localtime` bucket columns/triggers; project/date-bounded aggregate paths should scan indexed raw `llm_usage_events.occurred_at` rows and aggregate bucket labels at read time so later server/process timezone changes preserve legacy SQLite `date/strftime(..., 'localtime')` behavior. Current `#334` publication remains blocked by Automation graph authorization; treat PR publication/reconciliation as separate from the local analytics-bucketing design.
- OAuth account-limit snapshots are stored in `account_usage_snapshots`, with extra/model-specific account limits stored in `account_usage_extra_limits`.
- Usage capture is one final row per completed provider model call at the owning execution/call-site boundary, not per streamed chunk.
- Late attachment-bearing steering discovered after the provider call returns is requeued and the original execution's second successful completion still records the final provider usage row.
- Skill analytics event semantics distinguish selection, consumption, and skill changes: `selected` records routed/available skills, `loaded` records successful full-body `skill_view` loads, `viewed` records successful `skill_view` access, `created` records skill creation, and `edited` records app-managed skill updates/mutations.
- Agent-owned lifecycle hook executions record real hook starts as `selected` skill analytics events with `source=lifecycle_hook`/`surface=lifecycle_hook`, `skill_scope=agent_owned`, and owning project/task/agent metadata; hook body resolution does not synthesize `loaded` or `viewed` events.
- Lifecycle hook analytics keep ordinary task-execution foreign keys intact by storing the lifecycle execution identifier in the analytics turn/thread field rather than `skill_analytics_events.execution_id`.
- Skill create/edit telemetry is separated: create/import UI paths record `created` only for new skills, overwrites record `edited`, mutation tools record `created` for create actions, and other successful mutations record `edited`.
- Provider cost fields are stored only when provider data exists; OpenVibely does not silently estimate provider costs.

Provider normalization facts:
- Anthropic API/OAuth usage preserves input/output tokens plus cache creation/read token fields when returned. Anthropic thinking/reasoning is not currently available as separate `reasoning_output_tokens` and is treated as output tokens.
- OpenAI API/OAuth usage preserves input/output tokens plus cached/reasoning token fields from Responses API usage.
- OpenAI and Anthropic token totals are not directly comparable without normalization: OpenAI `input_tokens` includes cached tokens with cached as a subcount, while Anthropic `input_tokens` excludes cache reads and reports cache creation/read separately.
- For normalized analysis, OpenAI uncached input is approximately `input_tokens - cached_input_tokens`, while Anthropic raw context touched is approximately `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
- Historical backfilled rows from `executions.tokens_used` are total-only and do not invent input/output/cache/cost breakdowns.
- Usage analytics group by stored provider/model strings rather than a hardcoded model allowlist. Claude Fable/Mythos usage appears as Anthropic rows when Anthropic returns counters; provider access failures with no counters produce no usage row.
- HTTP 200 Anthropic refusals can still be recorded as failed usage when the response includes counters.

Analytics surface facts:
- `/analytics` includes local task/execution/productivity analytics, LLM usage/account-limit views, and Skill Curator analytics.
- Project memory recall effectiveness and downstream follow-through are not currently inspectable alongside skill analytics; an Analytics addition is proposed in `openvibely/openvibely#85`.
- Known Insights dashboard gap tracked in [GitHub #272](https://github.com/openvibely/openvibely/issues/272): accepting an Insight on the dashboard only flips its status to "accepted" but never creates or links a follow-up task, despite `Insight.TaskID` and `AcceptInsight`/`LinkTask` already existing as dead code in the service layer.
- `/api/analytics/usage` backs the Analytics page usage section; `/api/analytics/skills` backs Skill Curator Analytics.
- The Analytics page sends the selected/current project ID to every local analytics endpoint it fetches, and the visible project label is tied to that same project ID.
- Local Analytics sections are project-scoped when labeled with the selected project: model usage filters `llm_usage_events.project_id`; Skill Curator analytics filters `skill_analytics_events.project_id`; task/execution/productivity analytics and failed-task patterns filter through `tasks.project_id`.
- Provider account-limit cards are intentionally account/account-wide OAuth snapshots from `account_usage_snapshots` and related extra-limit rows, not project-scoped data. The UI labels them separately from project-scoped local usage.
- Direct/background model calls infer `project_id` from workdir paths by matching exact project repo paths, paths inside repos, and conventional task worktrees under `.worktrees/task_*`; nested repos choose the most specific project match.
- Task commit-summary LLM calls pass isolated task worktree paths as `workDir` and are expected to resolve back to the owning project.
- Analytics date/hour buckets and the built-in `month` range use app/local timezone semantics matching Schedules.
- Provider account cards appear before usage charts/tables.
- Skill Curator Analytics appears immediately above the Failed Task Patterns card.
- The usage chart label is `Token Usage`; token-count breakdowns are `Model Breakdown by Tokens`; execution-count breakdowns are `Model Breakdown by Executions`.
- Token Usage chart card controls must stay within the card on narrow/mobile widths by keeping header/select wrappers shrink-safe and applying wider minimums only at larger breakpoints.
- Analytics line graphs use Chart.js canvas rendering. Token Usage, Skill Activity Over Time, and Success/Failure Rates share pointer-nearest series highlighting while preserving index-mode tooltips. Their shared plugin records the current pointer-nearest point in `beforeEvent`, before Chart.js lays out the tooltip, and a stable tooltip `itemSort` promotes that dataset's row to the top while preserving every other row's relative order. The same plugin renders the active marker as a pointer-transparent DOM overlay above the canvas tooltip, using responsive canvas-to-CSS coordinates, chart-area clipping, an opaque dataset color with contrasting halo, pointer-exit hiding, and chart-destroy cleanup.
- Account-limit cards use provider as the heading with normalized plan/subscription metadata underneath; raw plan types, account IDs, config IDs, emails, and provider identity fields are not public card labels.
- Account-limit horizontal usage bars intentionally use explicit shared-theme track/fill meter markup instead of native/DaisyUI `<progress>` rendering because packaged macOS desktop light-mode WebView made native bars invisible.

Skill Curator analytics UI facts:
- Trend chart label is `Skill Activity Over Time` and should show exactly three always-visible trend lines with no dropdown/series selector: `Used` (`selected + loaded + viewed`), `Created`, and `Edited`.
- The dashboard visually matches the existing Analytics page chart/card style and uses standalone cards, not one combined mega-card.
- Ordering: Skill Activity Over Time next to Top Skills; Follow-through/Selected Outcomes next to Top Agent/Skill Pairs; Least Active Enabled Skills as its own full-width horizontal card.
- Top Skills/Overview, Follow-through/Selected Outcomes, and Top Agent/Skill Pairs use consistent standard graph styling, not pie charts or extra table-heavy UI. Least Active Enabled Skills remains table-only.
- Skill-specific filters such as skill surface, scope, and agent are not shown by default; dashboard defaults those filters to all.
- Agent heatmap/cell activity counts `selected + loaded + viewed`; `edited` belongs in drilldown/tooltip context.
- Shared Analytics date-range selector preserves the 365-day/Last Year option for non-skill usage analytics while skill analytics may map supported ranges separately.

OAuth account facts:
- OpenAI ChatGPT/Codex account usage targets `https://chatgpt.com/backend-api/wham/usage`.
- OpenAI account grouping uses real `OAuthAccountID`; configs with the same account collapse to one card.
- Anthropic account usage and profile behavior is split: usage payloads provide windows/utilization, while profile metadata drives public subscription labels when available.
- Anthropic configs group by stable organization/account identity when profile lookup succeeds; otherwise per-config display is safer than guessing.
- API-key accounts contribute local usage but do not get subscription/account-limit snapshots or fake billing cards.
- OAuth access tokens, refresh tokens, JWTs, decoded claims, auth headers, local config IDs, raw account IDs, fingerprints, and provider identity fields are backend-only and absent from frontend, templates, logs, model tools, and tool results.
- Provider account-limit endpoints are fragile; refresh failures affect live account cards, not local token usage.
- OpenAI account-limit windows are identified and labeled from `limit_window_seconds`, not from `primary_window`/`secondary_window` source slots. Recognized 5-hour, daily, weekly, monthly, and annual durations use a 5% tolerance, seconds round upward to persisted minutes, and semantic keys/order remain stable when provider slots swap.
- When persisted account-limit snapshots are merged with current model configs, prefer the current config's resolved `OAuthAccountID` over stale `account_usage_snapshots.account_id`. Older rows may contain a local config ID or pre-profile placeholder; stale-first grouping can split one Anthropic subscription into duplicate cards and hide the row carrying `Claude Max (20x)` profile metadata.
- When multiple configs contribute historical views for one provider account, the newest timestamped full snapshot must exclusively own all dynamic account-limit state. Older snapshots may backfill only missing safe descriptive metadata such as normalized plan/subscription labels; they must never backfill or resurrect primary/secondary/extra/model-specific limits, `ExtraUsageLabel`, `ExtraUsageMonthlyUSD`, `ExtraUsageUsedUSD`, utilization, reset timestamps, usage-credit state or amounts, credit balances/badges, spend-control state, dynamic timestamps, or errors.
- Persisted OpenAI snapshots created before duration-based window normalization are repaired at account-view assembly time from stored window minutes, so this behavior requires no schema migration.
- Analytics renders canonical account-limit ordering from the normalized `limits` list and displays elapsed reset times as `Reset due` or omits the line, never `Resets: 0 minutes`.

