---
name: usage_analytics
type: project
created: 2026-06-03
updated: 2026-09-19
source: after_complete
source_id: d06503a3e9b632d05f3d4e2e79c070f9
confidence: high
title: Usage Analytics
---

OpenVibely analytics are local operational analytics, not third-party product telemetry. PostHog, Segment, Mixpanel, and Google Analytics are outside the intended architecture.

Data model and normalization:
- Model usage is persisted locally for Anthropic/OpenAI API or OAuth and OpenAI-compatible API-key Chat Completions. Usage is normalized into input/output/total/cached/reasoning fields and recorded once per completed provider call at the owning execution/call-site boundary, never per stream chunk.
- `llm_usage_events` stores usage; `skill_analytics_events` stores skill activity. Skill events distinguish `selected`, `loaded`, `viewed`, `created`, and `edited`; lifecycle starts record real `selected` events with lifecycle metadata and do not synthesize loaded/viewed. Skill create/import and overwrite/edit remain distinct.
- Lifecycle analytics preserve ordinary task foreign keys and store lifecycle execution ID in the analytics turn/thread field. Late attachment-bearing steering is requeued while the original successful call records usage. Costs are recorded only when provider data exists and are never silently estimated.
- OAuth account-limit snapshots use `account_usage_snapshots` plus `account_usage_extra_limits`. API-key accounts contribute usage but never receive subscription/account-limit snapshots or fabricated billing cards.
- Anthropic preserves input/output and cache creation/read fields; thinking counts as output. OpenAI preserves input/output/cached/reasoning Responses usage. Totals are not directly comparable because cache reads are treated differently. Historical `executions.tokens_used` backfills are total-only. Provider failures without counters create no usage row; counted HTTP 200 refusals may record failed usage.
- Date/hour grouping follows app/local timezone semantics at query time, matching Schedules. Prefer Go aggregation over indexed raw `occurred_at`; do not add persisted local-time buckets without a deliberate design.

Surfaces and isolation:
- `/analytics` combines project/task/productivity, LLM usage/account limits, and Skill Curator analytics. `/api/analytics/usage` and `/api/analytics/skills` back legacy views. Dashboard and supporting-record flows are project-scoped; direct evidence requests fail closed without `project_id` so cross-project event/task/execution identifiers are not exposed.
- `NormalizeUsageFilter`/`UsageFilterInput` is the shared web/Chat filter core for defaults, RFC3339/date-only parsing, positive `Nd` ranges, local month/all/default-30-day calculation, and unknown-range fallback. Surface adapters retain decoding, refresh, shaping, and limits. Skill Analytics reuses range parsing with skill-specific filters.
- The Analytics dashboard adds project-scoped `/api/analytics/dashboard` data for six URL-persisted canonical views: `Overview`, `Outcomes`, `Agents & Models`, `Automations`, `Learning`, and `Usage`. The former `All Metrics` view is intentionally absent. Shared date-range and comparison controls use half-open `[from,to)` windows for dashboard and legacy execution metrics; grouped legacy and skill metrics retain their grouping controls. OpenAI and Anthropic account-limit cards render above Overview metrics as account-wide, not date-filtered, context.
- Dashboard aggregation keeps technical completion, historical first-pass, follow-up/rework, persisted-goal achievement, cycle-time distributions, recorded-cost coverage, Agent-definition attribution, workflow state, recent task evidence, and deterministic insights separate. Technical terminal metrics include completed, failed, and cancelled executions; task completion never implies goal achievement; costs are shown only when recorded and disclose coverage. Skill-to-task outcomes are observational, while memory effectiveness remains unavailable.
- Dashboard requests include the active view; non-selected tabs load lazily; navigating away from Analytics aborts in-flight fetches. Overview avoids hidden evidence-total plus Outcomes-only follow-up-distribution work while preserving visible KPIs and recent rows. Analytics aggregate CTEs use narrow task/execution projections. Agent Performance avoids historical execution window materialization over large rows through task-keyed covering-index seeks for first terminal status and first start time, and aggregates usage for cost metrics only after restricting to achieved-goal tasks. Workflow Performance completion rates count all selected-period invocations; API/UI accounting exposes completed, failed, cancelled, skipped, and open counts from the same period-filtered set, while duration samples are limited to terminal statuses with valid start/completion timestamps.
- Skill-outcome drill-down rendering derives technical labels from selected-period terminal counts, so completed-then-failed retries remain numerator-faithful, and derives goal labels from period-aware eligibility/achievement flags, so currently achieved goals outside the selected period are excluded from the goal denominator. Recent dashboard evidence first selects the requested paginated task IDs, then enriches only that bounded set with historical terminal state, first-start timing, model IDs, usage, and terminal evidence.
- `view_usage_analytics` is read-only, current-project, local-only, compact, and safe in Plan/Orchestrate. It reports totals, cost availability, provider/model breakdowns, recent buckets, and sanitized account-limit summaries without raw payloads, IDs, response IDs, or external refreshes.

OAuth/account analytics:
- Account-limit cards are account-wide and separate from project-scoped usage. OpenAI ChatGPT/Codex uses `wham/usage`; Anthropic uses stable profile/organization identity when available, otherwise safe per-configuration display. Current identity and newest full snapshot own dynamic limits; older snapshots supply only safe descriptive metadata.
- For standard OAuth, connections own credentials and account-limit snapshots; usage events remain attributed to the actual model configuration/execution. Rendering rebinds historical snapshots only to a currently linked model with the recorded connection generation. Stale revisions and unlinked models fail closed, legacy model-ID account identities are sanitized, and private fields never reach the UI. Credential sharing, refresh coordination, revision fencing, migration, and safe identity rules are canonical in `provider_architecture.md`.
- Cooldown and pending failure persistence follow connection ownership when available, with model-scoped fallback for unlinked configurations. Separate connections remain isolated even when display identities match.

Security and presentation:
- Account cards never expose account IDs, emails, tokens, JWTs, auth headers, fingerprints, provider identity fields, or strong-principal hashes. Provider refresh bodies and callback errors are sanitized/fixed and never logged. Standard callbacks atomically replace account identity, clearing stale identity when no new identity is extracted; Anthropic callback/background profile resolution stores only a safe display name plus non-serialized principal hash for verified same-user adoption.
- Pre-duration OpenAI snapshots are normalized at view assembly. Supported five-hour, daily, weekly, monthly, and annual windows have stable ordering; reset labels are `Reset due` or omitted. Charts use Chart.js and theme-aware account bars.
- Dashboard labels are `Token Usage`, `Model Breakdown by Tokens`, and `Model Breakdown by Executions`; skill charts separate Used, Created, and Edited.
- Authoritative project context for Agent generation/repair, lifecycle, Insights, Pulse/Reflection, memory, and task-worktree commit-summary calls must be explicit and query-free. Work-directory fallback uses compact `id`/`repo_path` rows backed by `(repo_path,id)` and preserves nested/worktree/Windows path semantics.
- Skill-write analytics use importer-specific classification: imports with created entries are `created`, nil/empty import results are `edited`; deletion records only after package/index cleanup succeeds. Scope, agent identity, authorization, and persistence remain unchanged.

Known gaps:
- Chat model cards do not expose OAuth connection status, task-result analytics lack project-scoped task links, and memory effectiveness is not yet measured alongside skill analytics.
- Individual Skills cards do not surface recent usage context or links into Learning analytics. Insights list/projection and direct-analysis preparation also have duplicated implementation seams that may drift.
