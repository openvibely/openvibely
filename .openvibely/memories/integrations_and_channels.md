---
name: integrations_and_channels
type: project
created: 2026-05-09
updated: 2026-09-27
source: consolidation
source_id: memory_consolidation_2026-09-27
confidence: high
title: Integrations and Channels
---

OpenVibely integrates with GitHub, Slack, Telegram, Discord, Email, X, generic inbound webhooks, and outbound message targets. UIs distinguish discovery/add from management, show explicit configured/running/connected state, and use confirmed deletion. Outbound Target cards remain separate from provider selection.

- Shared inbound Chat ingress handles attachments, compact model selection, queueing, first-turn task/execution creation, replies, broadcasts, history, execution, and queued promotion across Slack, Discord, Telegram, Email, and X. Channel-specific handlers override generic runtime actions by name. Inbound model lookup uses compact rows and hydrates only the selected full config.
- Channels expose prompt-safe status, never credentials. Tool-incapable provider/auth paths receive no channel tools or bracket-marker fallback. Email/Slack receipts use atomic deduplication and durable execution/queue handoff. Project deletion selectively evicts live Slack/Discord/Telegram selection caches only after commit.
- Channel/project/webhook/target resources are project-scoped and must validate ownership before side effects. Bulk provider removal is transactional; runtime teardown follows commit. Webhook disabled state and omitted fields are preserved; compact cards omit prompts/templates, secrets, and Agent assignments; dialogs load authorized details before Save.
- X is a first-class OAuth 1.0a user-context channel using API v2. Authenticated mention polling and outbound replies fail closed for incomplete credentials, unsupported operations, or invalid targets. Persistence is project-scoped; mention receipts are lease/ownership-fenced, identity and conversation context remain immutable through queueing, and reply delivery uses durable pending/posting/sent state. Provider-confirmed posts are not blindly retried when sent-state persistence is uncertain.
- Outbound `send_message` is project-scoped and audited for Slack, Telegram, Discord, Email, and X. By default it requires a saved or home target; arbitrary destinations require explicit policy. Targets reject duplicate platform/kind/destination/thread tuples and permit at most one Home per platform. Home is a delivery default, not inbound authorization or credential setup. Draft changes stage until Save; Test is immediate and non-mutating.
- GitHub task PR references are strict URLs matching the selected host and persisted number; repo identity comparison is case-insensitive. Automation and Chat share issue-action core; use runtime tools rather than shell/API substitutes. Issue discovery returns compact pages, hydrates likely candidates, searches existing work first, skips covered findings, and creates at most one new issue/task.
- Assignment to PAT owner/configured Authorized User approves implementation only, never merge/release. PR creation/reuse requires repository/branch/head match, required `## Summary` and `## Validation`, and exact `Closes #<issue>` provenance. Publication requires current authorization and live remote evidence; local work is not proof.
- Slack is allow-by-default without an authorized-user list. Discord is deny-by-default and project-scoped; Gateway running/error is readiness authority, not REST token validity. Telegram advances polling cursor only after terminal handling or durable handoff; ambiguous send failures stop to avoid duplicates. Email is deny-by-default/project-scoped, and blank secret saves preserve credentials.
- Remaining gaps include review-gated Backlog intake, webhook Automation triggers, complete selected-project ownership, delivery-history UI, consistent ordinary/Automation GitHub entry points, and full remote pagination. Exact approval claims are in `alerts_and_actionable_notifications.md`; Automation handoffs in `automation_graphs.md`.
