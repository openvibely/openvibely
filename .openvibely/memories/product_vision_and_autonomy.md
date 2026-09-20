---
name: product_vision_and_autonomy
type: project
created: 2026-06-10
updated: 2026-09-18
source: consolidation
source_id: memory_consolidation_2026-09-18
confidence: high
title: Product Vision, Reviewable Autonomy, and Naming
---

OpenVibely's product direction is recursive, reviewable self-improvement: goals become tasks, Agents work in isolated worktrees, schedules and dynamic wakeups sustain progress, and skills/memory compound learning across runs.

Vision principles:
- Move work toward `VISION.md` without acting as a hidden autonomous developer. Autonomy must be inspectable through task threads, lifecycle evidence, worktree diffs, schedules, goals, selected skills/memories, and review/merge boundaries.
- Humans retain product judgment, priority tradeoffs, credential/integration setup, and final merge/release decisions. Goal/Loop agents, schedules, chaining, Skill Curator, Memory Curator, and Automation Graphs are recursive primitives, not substitutes for human control.
- Automation Graphs is the visible orchestration surface for maintained Native and GitHub SDLC loops; durable goals, wakeups, schedules, task chaining, and curator agents remain underlying primitives.

User priority and bootstrap:
- Explicit user bug lists/specs outrank autonomously discovered `VISION.md` gaps. Preferred direction is a durable User Priority Inbox plus triage schedule.
- Vision Driver-style loops inspect user-priority work first, safely promote focused P0/P1 items, and fall back to self-discovery only when the user queue is empty or blocked.
- “Make this project autonomous” bootstrap should create visible tasks, schedules, appropriate goals, review/audit follow-ups, and curator loops through real control-plane tools. Source selection uses explicit paths or one obvious canonical root file; ask when sources are missing/ambiguous rather than guessing.
- Bootstrap runs on visible task surfaces with lifecycle-selected skills and actual runtime-tool support. Contracts must match capabilities and remain idempotent where discovery allows. Coordination uses durable OpenVibely state/control-plane actions, never direct task-to-task chat.

GitHub and Native SDLC:
- GitHub issues/PRs are the preferred durable mailbox/status board: finder roles open focused issues, human assignment approves implementation, Dev Inbox creates visible work, implementation tasks open/reuse PRs, and humans review/merge in GitHub.
- Native Alert SDLC is the in-app alternative with project-scoped actionable notifications, explicit approval, atomic claims, and implementation-task linkage. Either approval mechanism authorizes only configured downstream creation/activation, never merge, release, deployment, destructive remediation, credential changes, or arbitrary execution.
- Suggestions should deepen Chat coordination, execution, review, learning, and human control over incidental polish. Discovery/inbox tasks do not carry persisted completion goals; implementation tasks do. GitHub uses generic runtime/control-plane tools rather than hidden workflow daemons, and scheduled prompts/resources remain visible, scoped, and inspectable.
- Bundled GitHub/Native bootstrap skills are supported but disabled by default for lifecycle routing; maintained Automation owns prompt snapshots.

Naming and recurring themes:
- The cloud-infrastructure AI agent is `Paver`, not `Finn`. The benchmarking product formerly called `Finnsight` needs an independent replacement; no name is selected.
- The benchmark product compares models, Agents, and broader AI systems. Candidate names should be concise, memorable, repeatable, credible for developer/enterprise use, reasonably ownable, and compatible with OpenVibely. Favor coined/compound/evocative names around measurement, evaluation, clarity, arenas, standards, navigation, or performance; avoid generic names, awkward spellings, established-brand copies, and mechanically Paver-derived names.
- If Paver is reconsidered, retain the full recognizable string `Paver`; rejected candidates include `Paverdict`, `Paverify`, `Pavertex`, `Paverall`, and `Paverity`. Domain availability is irrelevant because the enterprise company owns a domain; collision/ownability/trademark risk matter, but public checks are not legal clearance.
- Recurring `VISION.md` themes are outcome-to-work decomposition, multi-agent coordination, reviewable autonomy UX, durable learning quality, external integrations, operational clarity, and provider/model normalization.
- The maintained discovery workflow treats the repository-root `VISION.md` as the active project-direction source. The project Settings edit surface currently exposes project metadata and worker limits but does not show whether `VISION.md` exists or provide a bounded read-only preview; GitHub issue #1181 tracks this product suggestion.
- Project Settings should expose local repository path health before task failure; the durable implementation contract for `#1225` is canonical in `openvibely_architecture.md`.
- The Idea Grade surface persists five dimensions but the dashboard currently shows only three, hiding clarity and deployability feedback that would help users understand whether an idea is understandable and realistically shippable. GitHub issue #1232 tracks exposing the stored dimensions without requiring a new analysis run.
- The Insights “Project Management” health card persists an active pending-task count but currently omits it from the dashboard metrics, hiding how much work is still waiting. GitHub issue #1246 tracks exposing that stored pending count in the card.
- Models search should match visible endpoint/account context from model cards; provider/UI details for `#1239` are canonical in `provider_architecture.md`.
