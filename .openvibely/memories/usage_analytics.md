---
name: usage_analytics
type: project
created: 2026-06-03
updated: 2026-08-28
source: consolidation
source_id: memory_consolidation_2026_08_28
confidence: high
title: Usage Analytics
---

OpenVibely usage analytics are local operational analytics, not third-party product telemetry. Third-party frontend tracking such as PostHog, Segment, Mixpanel, or Google Analytics is outside the intended architecture.

Durable analytics model:
- Persistent model-usage analytics exist for Anthropic and OpenAI OAuth/API-key paths, plus OpenAI-compatible Chat Completions API-key configs.
- OpenAI-compatible Chat Completions usage is parsed into canonical input/output/total/cached/reasoning fields and persisted through `RecordUsageFromResult` for `ProviderOpenAICompatible` API-key configs.
- Skill analytics events are stored locally in `skill_analytics_events`; usage rows are stored locally in `llm_usage_events`.
- Analytics date/hour buckets reflect current app/local timezone semantics at query time, matching Schedules. Direction for `#334` is to avoid persisted `localtime` bucket columns/triggers and aggregate indexed raw `occurred_at` rows in Go.
- OAuth account-limit snapshots are stored in `account_usage_snapshots`, with extra/model-specific limits stored in `account_usage_extra_limits`.
- Usage capture is one final row per completed provider model call at the owning execution/call-site boundary, not per streamed chunk.
- Late attachment-bearing steering discovered after provider call returns is requeued and the original execution's successful completion still records the final provider usage row.
- Skill analytics event semantics distinguish `selected`, `loaded`, `viewed`, `created`, and `edited`.
- Agent-owned lifecycle hook executions record real hook starts as `selected` events with lifecycle metadata; hook body resolution does not synthesize `loaded` or `viewed` events.
- Lifecycle hook analytics keep ordinary task execution foreign keys intact by storing lifecycle execution ID in the analytics turn/thread field rather than `skill_analytics_events.execution_id`.
- Skill create/edit telemetry separates create/import from overwrite/edit/mutation events.
- Provider cost fields are stored only when provider data exists; OpenVibely does not estimate provider costs silently.

Provider normalization facts:
- Anthropic API/OAuth usage preserves input/output tokens plus cache creation/read token fields when returned. Anthropic thinking/reasoning is treated as output tokens because separate reasoning counters are not available.
- OpenAI API/OAuth usage preserves input/output tokens plus cached/reasoning token fields from Responses API usage.
- OpenAI and Anthropic token totals are not directly comparable: OpenAI `input_tokens` includes cached tokens as a subcount, while Anthropic `input_tokens` excludes cache reads and reports cache creation/read separately.
- For normalized analysis, OpenAI uncached input is approximately `input_tokens - cached_input_tokens`; Anthropic raw context touched is approximately `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
- Historical backfilled rows from `executions.tokens_used` are total-only and do not invent input/output/cache/cost breakdowns.
- Usage analytics group by stored provider/model strings rather than hardcoded allowlists. Claude Fable/Mythos usage appears as Anthropic rows when counters exist; provider access failures with no counters produce no usage row.
- HTTP 200 Anthropic refusals can still be recorded as failed usage when response includes counters.

Analytics surface facts:
- `/analytics` includes local task/execution/productivity analytics, LLM usage/account-limit views, and Skill Curator analytics.
- Project memory recall effectiveness and follow-through are not currently inspectable alongside skill analytics; proposed in issue `#85`.
- Open suggestion `#841`: Analytics task-result queries (`GetMostFrequentTasks`, `GetFailedTaskPatterns`, and the average-time query) already return task IDs, but the dashboard renders only titles, counts, and chart labels without links; add project-scoped read-only drill-down links to the matching task details. This is distinct from `#380`, which concerns navigation and context on an execution-detail page.
- Duplication issue `#884` tracks repeated `UsageFilter` date/range normalization and fallback handling across the web usage API and the Chat `view_usage_analytics` action; provider refresh and compact response shaping are intentionally separate responsibilities.
- Failed-task-pattern analytics and Insights should share task-level latest-error query/projection semantics while preserving API JSON fields, project scoping, limits, and minimum-failure behavior.
- `/api/analytics/usage` backs model usage; `/api/analytics/skills` backs Skill Curator Analytics.
- Read-only `view_usage_analytics` Chat/runtime action exposes current-project model usage, token totals, cost availability, top model/provider breakdowns, recent buckets, and sanitized stored account-limit summaries in Plan and Orchestrate modes. It uses locally stored analytics only through `BuildLocalAnalyticsUsage`, a compact model-account projection that excludes credentials and large provider-config columns, skips unused dashboard aggregates, does not refresh external provider usage, and omits raw payloads, account IDs, config IDs, events, and provider response IDs; the full web `/api/analytics/usage` path remains unchanged.
- The Analytics page sends selected/current project ID to every local analytics endpoint and ties the visible project label to that ID.
- Local Analytics sections are project-scoped when labeled with the selected project: model usage filters `llm_usage_events.project_id`; Skill Curator analytics filters `skill_analytics_events.project_id`; task/execution/productivity analytics and failed-task patterns filter through `tasks.project_id`.
- Provider account-limit cards are account-wide OAuth snapshots, not project-scoped data, and are visually labeled separately from local usage.
- Direct/background model calls infer `project_id` from workdir paths by matching exact project repo paths, paths inside repos, and conventional task worktrees under `.worktrees/task_*`; nested repos choose the most specific project match.
- Task commit-summary LLM calls pass isolated task worktree paths as `workDir` and should resolve to the owning project.
- Provider account cards appear before usage charts/tables. Skill Curator Analytics appears immediately above Failed Task Patterns.
- Usage chart label is `Token Usage`; token breakdowns are `Model Breakdown by Tokens`; execution breakdowns are `Model Breakdown by Executions`.
- Token Usage chart card controls must stay within the card on mobile.
- Analytics line graphs use Chart.js canvas rendering with shared pointer-nearest highlighting while preserving index-mode tooltips. DOM active markers should be clipped/responsive and cleaned up on chart destroy.
- Account-limit cards use provider heading plus normalized plan/subscription metadata; raw plan types, account IDs, config IDs, emails, and provider identity fields are not public labels.
- Account-limit bars use explicit shared-theme track/fill markup instead of native/DaisyUI progress because packaged macOS desktop light-mode WebView made native bars invisible.

Skill Curator analytics UI facts:
- Trend chart label is `Skill Activity Over Time` and shows exactly three always-visible lines: `Used` (`selected + loaded + viewed`), `Created`, and `Edited`.
- Dashboard visually matches Analytics page chart/card style and uses standalone cards, not one combined mega-card.
- Ordering: Skill Activity Over Time next to Top Skills; Follow-through/Selected Outcomes next to Top Agent/Skill Pairs; Least Active Enabled Skills full-width horizontal card.
- Top Skills/Overview, Follow-through/Selected Outcomes, and Top Agent/Skill Pairs use standard graph styling, not pie charts or table-heavy UI. Least Active Enabled Skills remains table-only.
- Skill-specific filters such as surface, scope, and agent are not shown by default; dashboard defaults them to all.
- Agent heatmap/cell counts include `selected + loaded + viewed`; `edited` belongs in drilldown/tooltip context.
- Shared date-range selector preserves 365-day/Last Year for non-skill analytics while skill analytics may map supported ranges separately.

OAuth account facts:
- OpenAI ChatGPT/Codex account usage targets `https://chatgpt.com/backend-api/wham/usage`.
- OpenAI account grouping uses real `OAuthAccountID`; configs with the same account collapse to one card.
- Anthropic account usage and profile behavior is split: usage payloads provide windows/utilization, profile metadata drives public subscription labels when available.
- Anthropic configs group by stable organization/account identity when profile lookup succeeds; otherwise per-config display is safer than guessing.
- API-key accounts contribute local usage but do not get subscription/account-limit snapshots or fake billing cards.
- OAuth tokens, JWTs, decoded claims, auth headers, local config IDs, raw account IDs, fingerprints, and provider identity fields are backend-only and absent from frontend/templates/logs/model tools/tool results.
- Provider account-limit endpoints are fragile; refresh failures affect live account cards, not local token usage.
- OpenAI account-limit windows are identified from `limit_window_seconds`; recognized 5-hour, daily, weekly, monthly, and annual durations use 5% tolerance and stable semantic keys/order.
- When merging persisted account snapshots with current configs, prefer current config's resolved `OAuthAccountID` over stale snapshot `account_id` to avoid duplicate Anthropic subscription cards.
- When multiple configs contribute historical views for one provider account, newest timestamped full snapshot exclusively owns dynamic limit state. Older snapshots may backfill only missing safe descriptive metadata, never limits/utilization/reset/credit/spend-control/error state.
- Persisted OpenAI snapshots created before duration-based window normalization are repaired at account-view assembly time without schema migration.
- Analytics renders canonical account-limit ordering from normalized `limits` and displays elapsed reset times as `Reset due` or omits the line, never `Resets: 0 minutes`.
