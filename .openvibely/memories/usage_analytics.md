---
name: usage_analytics
type: project
created: 2026-06-03
updated: 2026-08-30
source: after_complete
source_id: 40e607e3815c48800073cc6f9674d3c7:0b7169a7ae9950d2
confidence: high
title: Usage Analytics
---

OpenVibely usage analytics are local operational analytics, not third-party product telemetry. Third-party frontend tracking such as PostHog, Segment, Mixpanel, or Google Analytics is outside the intended architecture.

Durable analytics model:
- Persistent model-usage analytics exist for Anthropic and OpenAI OAuth/API-key paths, plus OpenAI-compatible Chat Completions API-key configs. OpenAI-compatible usage is normalized into input/output/total/cached/reasoning fields and persisted through `RecordUsageFromResult`.
- Skill analytics events are stored locally in `skill_analytics_events`; usage rows are stored locally in `llm_usage_events`.
- Analytics date/hour buckets reflect current app/local timezone semantics at query time, matching Schedules. The direction for `#334` is to aggregate indexed raw `occurred_at` rows in Go rather than add persisted `localtime` bucket columns or triggers.
- OAuth account-limit snapshots are stored in `account_usage_snapshots`, with extra/model-specific limits in `account_usage_extra_limits`.
- Usage capture is one final row per completed provider model call at the owning execution/call-site boundary, not per streamed chunk. Late attachment-bearing steering discovered after a provider call is requeued while the original successful execution still records usage.
- Skill analytics distinguish `selected`, `loaded`, `viewed`, `created`, and `edited`. Agent-owned lifecycle hook executions record real hook starts as `selected` with lifecycle metadata; hook resolution does not synthesize `loaded` or `viewed`.
- Lifecycle hook analytics keep ordinary task foreign keys intact by storing lifecycle execution ID in the analytics turn/thread field rather than `skill_analytics_events.execution_id`.
- Skill create/edit telemetry separates create/import from overwrite/edit/mutation events. Provider cost fields are stored only when provider data exists; OpenVibely does not estimate costs silently.

Provider normalization facts:
- Anthropic API/OAuth usage preserves input/output tokens plus cache creation/read fields when returned. Anthropic thinking/reasoning is treated as output because separate reasoning counters are unavailable.
- OpenAI API/OAuth usage preserves input/output plus cached/reasoning fields from Responses API usage.
- OpenAI and Anthropic totals are not directly comparable: OpenAI `input_tokens` includes cached tokens as a subcount, while Anthropic `input_tokens` excludes cache reads and reports cache creation/read separately. Approximate normalized values are OpenAI uncached input `input_tokens - cached_input_tokens` and Anthropic raw context `input_tokens + cache_creation_input_tokens + cache_read_input_tokens`.
- Historical rows backfilled from `executions.tokens_used` are total-only and do not invent input/output/cache/cost breakdowns.
- Analytics group by stored provider/model strings rather than hardcoded allowlists. Claude Fable/Mythos usage appears as Anthropic rows when counters exist; provider failures with no counters produce no usage row. HTTP 200 Anthropic refusals can still record failed usage when counters exist.

Analytics surface facts:
- `/analytics` includes local task/execution/productivity analytics, LLM usage/account-limit views, and Skill Curator analytics. `/api/analytics/usage` backs model usage; `/api/analytics/skills` backs Skill Curator Analytics.
- Project memory recall effectiveness and follow-through are not currently inspectable alongside skill analytics; proposed in issue `#85`.
- Analytics task-result queries already return task IDs, but the dashboard currently renders only titles, counts, and chart labels without links; issue `#841` proposes project-scoped read-only drill-down links to matching task details. This is distinct from execution-detail navigation gaps.
- `NormalizeUsageFilter`/`UsageFilterInput` is the shared service-layer core for web usage analytics and Chat `view_usage_analytics`. It owns grouping defaults, RFC3339/date-only parsing, positive `Nd` ranges, local `month`/`all`/default-30-day calculation, and unknown-range fallback; surface adapters retain decoding, refresh policy, response shaping, and limits.
- Skill Analytics now reuses `service.ParseUsageAnalyticsRangeDays` with the raw `range` query value; the local `skillAnalyticsRangeDays` duplicate was removed in issue `#916`/PR `#931`. Skill-specific filters, date construction, defaults including whitespace fallback, grouping, limits, and response shaping remain separate and unchanged. This is distinct from `#884`, which covers Usage filter normalization across the web API and Chat.
- Failed-task-pattern analytics and Insights should share task-level latest-error query/projection semantics while preserving API fields, project scoping, limits, and minimum-failure behavior.
- `InsightsRepo` list methods currently duplicate the full `insights` projection, row/query-context lifecycle, and `scanInsights` path while differing only in predicate, ordering, and limit; duplication issue `#913` proposes consolidating that shared list-query assembly without changing filters or ordering.
- Insights status/accept/link/delete and knowledge-entry deletion are project-scoped through handler, service, repository, and rendered action URLs. Foreign/unknown IDs return controlled not-found responses without mutation or leakage; same-project status transitions preserve `resolved_at`.
- Read-only `view_usage_analytics` exposes current-project model usage, token totals, cost availability, top model/provider breakdowns, recent buckets, and sanitized stored account-limit summaries in Plan and Orchestrate modes. It uses local data through `BuildLocalAnalyticsUsage`, compact model-account projections, no external provider refresh, and no raw payloads, account IDs, config IDs, events, or provider response IDs.
- The Analytics page sends selected/current project ID to every local analytics endpoint and ties the visible project label to that ID. Model usage filters `llm_usage_events.project_id`; Skill Curator analytics filters `skill_analytics_events.project_id`; task/productivity analytics filter through `tasks.project_id`.
- Provider account-limit cards are account-wide OAuth snapshots, not project-scoped data, and are labeled separately from local usage. Direct/background calls infer project ID from exact repo paths, paths inside repos, and conventional `.worktrees/task_*` paths; nested repos choose the most specific project.
- Provider account cards appear before usage charts/tables, and Skill Curator Analytics appears immediately above Failed Task Patterns. Chart labels are `Token Usage`, `Model Breakdown by Tokens`, and `Model Breakdown by Executions`.
- Analytics line graphs use Chart.js canvas rendering with shared pointer-nearest highlighting while preserving index-mode tooltips. Account-limit cards use normalized provider/plan metadata and never expose raw account IDs, config IDs, emails, tokens, JWTs, auth headers, fingerprints, or provider identity fields.
- Account-limit bars use explicit shared-theme track/fill markup because native/DaisyUI progress bars can be invisible in packaged macOS desktop light-mode WebView.

Skill Curator analytics UI:
- Trend chart is `Skill Activity Over Time` with exactly three visible lines: `Used` (`selected + loaded + viewed`), `Created`, and `Edited`.
- Dashboard uses standalone cards and standard graph styling. Ordering is activity next to Top Skills, Follow-through/Selected Outcomes next to Top Agent/Skill Pairs, and Least Active Enabled Skills full-width.
- Skill-specific filters such as surface, scope, and agent default to all and are not shown by default. Agent heatmap/cell counts include selected/loaded/viewed; edited belongs in drilldown or tooltip context.
- The shared date-range selector preserves 365-day/Last Year for non-skill analytics while skill analytics may map supported ranges separately.

OAuth account facts:
- OpenAI ChatGPT/Codex account usage targets `https://chatgpt.com/backend-api/wham/usage`; grouping uses real `OAuthAccountID`, so configs for one account collapse to one card.
- Anthropic usage payloads provide windows/utilization, while profile metadata drives public subscription labels when available. Stable organization/account identity is used when profile lookup succeeds; otherwise per-config display is safer than guessing.
- API-key accounts contribute local usage but do not receive subscription/account-limit snapshots or fake billing cards.
- When merging persisted snapshots with current configs, prefer the current resolved `OAuthAccountID` over stale snapshot `account_id`. For multiple configs in one account, the newest full snapshot exclusively owns dynamic limit state; older snapshots may backfill only safe descriptive metadata.
- Persisted OpenAI snapshots created before duration-based window normalization are repaired at account-view assembly time without schema migration. Recognized 5-hour, daily, weekly, monthly, and annual windows use `limit_window_seconds` with tolerance and stable ordering.
- Analytics renders canonical account-limit ordering from normalized `limits` and displays elapsed reset times as `Reset due` or omits the line, never `Resets: 0 minutes`.
