---
name: usage_analytics
type: project
created: 2026-06-03
updated: 2026-09-03
source: consolidation
source_id: memory_consolidation_2026-09-03
confidence: high
title: Usage Analytics
---

OpenVibely analytics are local operational analytics, not third-party product telemetry. PostHog, Segment, Mixpanel, and Google Analytics are outside the intended architecture.

Durable data model:
- Model usage is persisted locally for Anthropic and OpenAI API/OAuth paths and OpenAI-compatible API-key Chat Completions. Usage is normalized into input/output/total/cached/reasoning fields and recorded once per completed provider model call at the owning execution/call-site boundary, never per stream chunk.
- `llm_usage_events` stores usage; `skill_analytics_events` stores skill activity. Skill events distinguish `selected`, `loaded`, `viewed`, `created`, and `edited`; lifecycle hook starts record real `selected` events with lifecycle metadata and do not synthesize loaded/viewed. Skill create/import and overwrite/edit telemetry remain distinct.
- Lifecycle analytics preserve ordinary task foreign keys and store lifecycle execution ID in the analytics turn/thread field. Late attachment-bearing steering is requeued while the original successful call still records usage. Provider cost is recorded only when provider data exists; costs are never silently estimated.
- OAuth account-limit snapshots live in `account_usage_snapshots` with model/extra limits in `account_usage_extra_limits`. API-key accounts contribute usage but never receive subscription/account-limit snapshots or fabricated billing cards.
- Date/hour grouping follows current app/local timezone semantics at query time, matching Schedules. The preferred direction for `#334` is Go aggregation of indexed raw `occurred_at`, not persisted localtime bucket columns/triggers.

Provider normalization:
- Anthropic preserves input/output and cache creation/read fields; thinking is treated as output because no separate counter exists. OpenAI preserves input/output/cached/reasoning Responses usage. OpenAI and Anthropic totals are not directly comparable: OpenAI input includes cached tokens, while Anthropic input excludes cache reads. Approximate uncached/context values must account for those differences.
- Historical rows backfilled from `executions.tokens_used` are total-only. Analytics group by stored provider/model strings rather than hardcoded allowlists; provider failures with no counters create no usage row, while counted HTTP 200 refusals can record failed usage.

Surfaces and isolation:
- `/analytics` combines local task/execution/productivity, LLM usage/account limits, and Skill Curator analytics. `/api/analytics/usage` and `/api/analytics/skills` back the views. Task/productivity queries, `llm_usage_events`, and `skill_analytics_events` are project-filtered; visible project labels and every endpoint use the selected project ID.
- `NormalizeUsageFilter`/`UsageFilterInput` is the shared web/Chat filter core for defaults, RFC3339/date-only parsing, positive `Nd` ranges, local month/all/default-30-day calculation, and unknown-range fallback. Surface adapters retain decoding, refresh, response shaping, and limits. Skill Analytics reuses `ParseUsageAnalyticsRangeDays` while keeping skill-specific filters/defaults.
- Chat `view_usage_analytics` is read-only, Plan/Orchestrate-safe, current-project, local-only, and compact. It reports usage totals, cost availability, top provider/model breakdowns, recent buckets, and sanitized account-limit summaries without raw payloads, account/config IDs, events, response IDs, or external refreshes.
- OAuth account-limit cards are account-wide, explicitly separated from project-scoped local usage. OpenAI ChatGPT/Codex usage uses the `wham/usage` endpoint and real `OAuthAccountID`; Anthropic account identity uses stable profile/org data when available, otherwise safer per-config display. When merging snapshots, current resolved account ID wins and newest full snapshot owns dynamic limits; older snapshots supply only safe descriptive metadata.
- Persisted OpenAI snapshots from before duration-based windows are normalized during view assembly. Supported five-hour, daily, weekly, monthly, and annual windows retain stable ordering; reset labels are `Reset due` or omitted, never `Resets: 0 minutes`. Charts use Chart.js with shared pointer-nearest highlighting; account bars use explicit theme-aware track/fill markup for packaged macOS light WebView reliability.
- Dashboard labels are `Token Usage`, `Model Breakdown by Tokens`, and `Model Breakdown by Executions`. Skill chart `Skill Activity Over Time` has `Used` (selected+loaded+viewed), `Created`, and `Edited` lines; filters default to all/hidden and least-active enabled skills remain visible.

Known gaps and ownership:
- Project memory recall effectiveness/follow-through is not yet shown beside skill analytics (`#85`). Task-result analytics lack project-scoped links to task details (`#841`).
- Failed-task patterns and Insights should share latest-error query/projection semantics. Insights list methods duplicate projection/query/scan assembly (`#913`); status/delete/link behavior remains project-scoped and preserves `resolved_at`.
- Chat does not expose compact OAuth connected/expired/not-connected status shown on model cards (`#695`). Account cards must never expose account IDs, emails, tokens, JWTs, auth headers, fingerprints, or provider identity fields.
