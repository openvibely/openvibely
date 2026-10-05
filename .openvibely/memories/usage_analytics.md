---
name: usage_analytics
type: project
created: 2026-06-03
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Usage Analytics
---

OpenVibely Analytics are local operational analytics, not third-party product telemetry. PostHog, Segment, Mixpanel, and Google Analytics are outside the intended architecture.

- Provider usage is normalized into input/output/total/cached/reasoning fields and recorded once per completed provider call, not per stream chunk. Supported provider counters are preserved as reported; totals may not be comparable across providers. Historical execution backfills are total-only. Costs are recorded only when provider data exists and are never silently estimated.
- `llm_usage_events` stores model usage; `skill_analytics_events` records selected, loaded, viewed, created, and edited separately. Lifecycle selection is a real `selected` event; do not synthesize loaded/viewed. Skill create/import differs from overwrite/edit.
- OAuth account-limit snapshots are separate from API-key usage. API-key accounts do not receive subscription snapshots or fabricated billing cards. OAuth connections own account-limit snapshots; usage remains attributed to actual model/execution.
- `/analytics` combines project/task outcomes, LLM usage/account limits, and Skill Curator analytics. The six URL-persisted canonical views are Overview, Outcomes, Agents & Models, Automations, Learning, and Usage. Direct evidence requests fail closed without project scope; do not expose cross-project task/execution/event identifiers.
- Date filters use half-open `[from,to)` windows, local timezone semantics, and shared web/Chat range normalization. Prefer Go aggregation over indexed raw timestamps; do not persist local-time buckets without an explicit design.
- Keep technical completion, first-pass outcomes, follow-up/rework, persisted-goal achievement, cycle time, recorded-cost coverage, Agent attribution, and workflow states distinct. Task completion does not imply goal achievement; cost coverage must be disclosed. Skill-to-task outcomes are observational, and memory effectiveness is not measured.
- Queries and supporting records use narrow projections, bounded recent evidence, pagination, and query-plan-aware aggregation. Workflow completion rates count all selected-period terminal invocations, while duration samples require valid terminal timestamps.
- `view_usage_analytics` is read-only, current-project, local-only, compact, and safe for Plan/Orchestrate. It returns totals, cost availability, provider/model breakdowns, recent buckets, and sanitized account-limit summaries without raw payloads, identifiers, or external refreshes.
- Account-limit UI must never expose account IDs, emails, tokens, JWTs, auth headers, provider identity fields, or principal hashes. Refresh/callback errors and payloads are sanitized. OAuth snapshot rebinding is revision- and currently-linked-model-fenced; connection ownership and safe identity rules are canonical in `provider_architecture.md`.
- Dashboard charts/KPIs are display-only, not evidence navigation; use explicit tabs, filters, and table/evidence links. Chat model cards still do not expose OAuth connection status, task-result analytics lack project-scoped task links, and memory effectiveness remains unavailable.
