---
name: product_vision_and_autonomy
type: project
created: 2026-06-10
updated: 2026-08-28
source: consolidation
source_id: memory_consolidation_2026_08_28
confidence: high
title: Product Vision, Reviewable Autonomy, and Naming
---

OpenVibely's product direction centers on recursive, reviewable self-improvement: goals become tasks, agents execute work in isolated worktrees, schedules and dynamic wakeups keep progress moving, and skills/memory compound learning across runs.

Durable vision principles:
- The system should help continuously drive work toward `VISION.md`, but not behave as a hidden autonomous developer.
- Autonomy should remain inspectable and review-gated through task threads, lifecycle evidence, worktree diffs, schedules, goals, selected skills, selected memories, and review/merge boundaries.
- Humans remain responsible for product judgment, priority tradeoffs, credentials/integration setup, and final merge/release decisions.
- Goal Agent, Loop Agent, scheduled tasks, task chaining, Skill Curator, and Memory Curator are the intended primitives for recursive self-improvement loops.
- Automation Graphs is the primary visible orchestration surface for maintained Native and GitHub SDLC loops. Durable goals, dynamic loop wakeups, scheduled tasks, task chaining, and curator agents remain underlying primitives.

User-priority and bootstrap direction:
- Explicit user-provided bug lists and specs are higher-priority work sources than autonomously discovered `VISION.md` gaps.
- Preferred operating pattern is a durable User Priority Inbox plus triage schedule.
- Vision Driver-style autonomous loops should inspect user-priority tasks first, promote safe/focused P0/P1 user work before autonomous vision work, and only fall back to self-discovered gaps when the user queue is empty or blocked.
- Reusable autonomy bootstrap skills should act as an easy button: short prompts such as “make this project autonomous” or “loop on this vision” should produce visible OpenVibely tasks, schedules, goals where appropriate, review/audit follow-ups, and curator loops through real control-plane tools.
- Source-of-truth selection is conservative: use explicit paths or one obvious canonical root file. If vision/spec/defect sources are missing or ambiguous, ask for the exact source instead of guessing or creating generic discovery work.
- Bootstrap execution belongs on a visible task surface with lifecycle-selected skills and runtime-tool support. Tool contracts must match actual capabilities and remain idempotent where discovery APIs permit.
- Autonomous task coordination is mediated through durable OpenVibely state and control-plane actions, not direct task-to-task communication.

GitHub and Native SDLC direction:
- GitHub issues and PRs are the preferred durable mailbox and status board for autonomous product development: finder roles open focused issues, humans approve implementation by assignment, Dev Inbox creates visible implementation tasks, implementation tasks open/reuse PRs, and humans review/merge in GitHub.
- Native Alert SDLC is the in-app alternative using project-scoped actionable notifications, explicit approval, atomic claims, and implementation-task linkage.
- Human approval, whether GitHub assignment or Native Alert decision, authorizes only creation or activation of configured downstream work. It never authorizes merge, release, deployment, destructive remediation, credential changes, or arbitrary execution.
- Autonomous suggestions should prioritize small gaps that materially deepen Chat coordination, task execution, review, learning, and human control rather than minor incidental UI polish.
- Bootstrap and Automation paths create visible scheduled loops and implementation tasks, forward authorized PR feedback, and preserve human review/merge gates. Recurring discovery/inbox tasks do not carry persisted completion goals; implementation tasks do.
- GitHub autonomous-SDLC support should use generic runtime and control-plane tools rather than hidden workflow-specific daemons. Scheduled prompts/resources must remain visible, project-scoped, and inspectable.
- The bundled `openvibely_github_autonomous_sdlc_bootstrap` and `openvibely_native_autonomous_sdlc_bootstrap` remain supported prompt-driven setup paths but are disabled by default for lifecycle routing; maintained Automation templates own their prompts independently.

Related product naming context:
- The cloud-infrastructure AI agent formerly named `Finn` is now `Paver`.
- The AI benchmarking product formerly called `Finnsight` needs a replacement independent of Finn; it need not incorporate Paver or construction imagery.
- The benchmark product compares models, agents, and broader AI systems. Its name should be concise, memorable, easy to say repeatedly, credible for developer/enterprise use, reasonably ownable, and compatible with the OpenVibely ecosystem.
- Favor coined names, compounds, or evocative real words around measurement, trials, evaluation, intelligence, clarity, arenas, standards, navigation, or performance. Avoid generic names, awkward spellings, established-brand copies, and mechanically Paver-derived names.
- If a Paver-linked name is reconsidered, preserve the full string `Paver` in a genuine recognizable overlap. Prior candidates `Paverdict`, `Paverify`, `Pavertex`, `Paverall`, and `Paverity` are not finalists. A strong standalone benchmark name is preferred unless constraints change.
- Domain availability is not a criterion because the enterprise company already has a domain. Check product-name collisions, ownability, and trademark risk, while noting public collision checks are not legal clearance.
- No replacement benchmark name has been selected.

Recurring product-completeness themes from `VISION.md` include outcome-to-work decomposition, multi-agent team coordination, reviewable autonomy UX, durable learning quality, external integrations, operational clarity, and provider/model normalization.
