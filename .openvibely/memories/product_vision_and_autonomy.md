---
name: product_vision_and_autonomy
type: project
created: 2026-06-10
updated: 2026-09-03
source: consolidation
source_id: memory_consolidation_2026-09-03
confidence: high
title: Product Vision, Reviewable Autonomy, and Naming
---

OpenVibely's product direction is recursive, reviewable self-improvement: goals become tasks, Agents work in isolated worktrees, schedules and dynamic wakeups sustain progress, and skills/memory compound learning across runs.

Vision principles:
- The product should continuously move work toward `VISION.md` without acting as a hidden autonomous developer. Autonomy must be inspectable through task threads, lifecycle evidence, worktree diffs, schedules, goals, selected skills/memories, and review/merge boundaries.
- Humans retain product judgment, priority tradeoffs, credential/integration setup, and final merge/release decisions. Goal Agent, Loop Agent, scheduled tasks, chaining, Skill Curator, Memory Curator, and Automation Graphs are the intended recursive primitives.
- Automation Graphs is the visible orchestration surface for maintained Native and GitHub SDLC loops; durable goals, wakeups, schedules, task chaining, and curator agents remain underlying primitives.

User priority and bootstrap:
- Explicit user bug lists/specs outrank autonomously discovered `VISION.md` gaps. The preferred pattern is a durable User Priority Inbox plus triage schedule.
- Vision Driver-style loops inspect user-priority work first, safely promote focused P0/P1 items, and fall back to self-discovered work only when the user queue is empty or blocked.
- Bootstrap prompts such as “make this project autonomous” should create visible tasks, schedules, goals where appropriate, review/audit follow-ups, and curator loops through real control-plane tools. Source selection is conservative: use explicit paths or one obvious canonical root file, and ask when sources are missing/ambiguous rather than guessing.
- Bootstrap runs on visible task surfaces with lifecycle-selected skills and actual runtime-tool support. Tool contracts must match capabilities and remain idempotent where discovery APIs allow. Coordination is through durable OpenVibely state/control-plane actions, never direct task-to-task chat.

GitHub and Native SDLC:
- GitHub issues/PRs are the preferred durable mailbox/status board: finder roles open focused issues, human assignment approves implementation, Dev Inbox creates visible work, implementation tasks open/reuse PRs, and humans review/merge in GitHub.
- Native Alert SDLC is the in-app alternative using project-scoped actionable notifications, explicit approval, atomic claims, and implementation-task linkage. Either approval mechanism authorizes only configured downstream creation/activation, never merge, release, deployment, destructive remediation, credential changes, or arbitrary execution.
- Suggestions should prioritize small gaps that deepen Chat coordination, execution, review, learning, and human control over incidental polish. Bootstrap/Automation creates visible loops and review follow-ups; recurring discovery/inbox tasks do not carry persisted completion goals, implementation tasks do.
- GitHub support should use generic runtime/control-plane tools rather than hidden workflow daemons. Scheduled prompts/resources remain visible, project-scoped, and inspectable. Bundled GitHub/Native bootstrap skills remain supported but disabled by default for lifecycle routing; maintained Automation owns its prompt snapshots.

Naming and recurring themes:
- The cloud-infrastructure AI agent formerly called `Finn` is `Paver`. The AI benchmarking product formerly called `Finnsight` needs an independent replacement; no replacement name is selected.
- The benchmark product compares models, Agents, and broader AI systems. A viable name should be concise, memorable, easy to repeat, credible for developer/enterprise use, reasonably ownable, and compatible with OpenVibely. Favor coined/compound/evocative names around measurement, evaluation, clarity, arenas, standards, navigation, or performance; avoid generic names, awkward spellings, established-brand copies, and mechanically Paver-derived names.
- If Paver is reconsidered, retain the full recognizable string `Paver`; rejected candidates include `Paverdict`, `Paverify`, `Pavertex`, `Paverall`, and `Paverity`. Domain availability is irrelevant because the enterprise company already owns a domain; collision/ownability/trademark risk matter, but public checks are not legal clearance.
- Recurring `VISION.md` themes are outcome-to-work decomposition, multi-agent coordination, reviewable autonomy UX, durable learning quality, external integrations, operational clarity, and provider/model normalization.
