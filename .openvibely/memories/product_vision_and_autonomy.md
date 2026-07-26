---
name: product_vision_and_autonomy
type: project
created: 2026-06-10
updated: 2026-07-25
source: consolidation
source_id: memory_consolidation_2026_07_25
confidence: high
title: Product Vision and Reviewable Autonomy
---

OpenVibely's product direction centers on recursive, reviewable self-improvement: goals become tasks, agents execute work in isolated worktrees, schedules and dynamic wakeups keep progress moving, and skills/memory compound learning across runs.

Durable vision principles:
- The system should help continuously drive work toward `VISION.md`, but not behave as a hidden autonomous developer.
- Autonomy should remain inspectable and review-gated through task threads, lifecycle evidence, worktree diffs, schedules, goals, selected skills, selected memories, and review/merge boundaries.
- Humans remain responsible for product judgment, priority tradeoffs, credentials/integration setup, and final merge/release decisions.
- Goal Agent, Loop Agent, scheduled tasks, task chaining, Skill Curator, and Memory Curator are the intended primitives for recursive self-improvement loops.
- The strongest current self-evolution pattern is a durable goal-driven Vision Driver task, dynamic loop wakeups for adaptive continuation, scheduled audits for reliable recurrence, task chaining for reviewable decomposition, and curator agents for compounding durable knowledge.

User-priority direction:
- Explicit user-provided bug lists and specs are higher-priority work sources than autonomously discovered `VISION.md` gaps.
- Preferred operating pattern is a durable User Priority Inbox plus triage schedule.
- Vision Driver-style autonomous loops should inspect user-priority tasks first, promote safe/focused P0/P1 user work before autonomous vision work, and only fall back to self-discovered gaps when the user queue is empty or blocked.

Bootstrap and orchestration direction:
- Reusable autonomy bootstrap skills should act as an easy button: short prompts such as “make this project autonomous” or “loop on this vision” should produce visible OpenVibely tasks, schedules, goals where appropriate, review/audit follow-ups, and curator loops through real control-plane tools rather than merely describing a workflow.
- Source-of-truth selection is conservative: use explicit paths or one obvious canonical root file. If vision, specification, or defect sources are missing or ambiguous, ask for the exact source instead of guessing or creating generic discovery work.
- Bootstrap execution belongs on a visible task surface with the required lifecycle-selected skills and runtime-tool support. Tool contracts must match actual capabilities and remain idempotent where discovery APIs permit; current task-thread, skill-routing, and schedule-discovery constraints are maintained in `chat_thread_system.md` and `agent_lifecycle_and_skills.md`.
- Autonomous task coordination is mediated through durable OpenVibely state and control-plane actions, not direct task-to-task communication.

GitHub-backed autonomous SDLC direction:
- GitHub issues and PRs are the preferred durable mailbox and status board for autonomous product development. Suggestion/finder roles open focused issues; humans approve implementation by assignment to the configured inbox identity; Dev Inbox creates visible implementation tasks; implementation tasks open or reuse PRs; and humans review and merge in GitHub.
- Human approval, whether represented by GitHub assignment or a Native Alert decision, authorizes only creation or activation of configured downstream work. It never authorizes merge, release, or deployment.
- Autonomous suggestions should prioritize small gaps that materially deepen Chat coordination, task execution, review, learning, and human control rather than minor incidental UI improvements. Active product gaps belong in their canonical feature memories or GitHub issues, not as an issue-by-issue list here.
- The bundled `openvibely_github_autonomous_sdlc_bootstrap` remains a supported prompt-driven setup path. The maintained GitHub SDLC Automation template owns its role prompts independently so it does not require the bootstrap skill package at runtime.
- The bootstrap and Automation paths create visible scheduled loops and implementation tasks, forward authorized PR feedback, and preserve human review/merge gates. Recurring discovery/inbox tasks do not carry persisted completion goals; implementation tasks do.
- GitHub autonomous-SDLC support should use generic runtime and control-plane tools rather than hidden workflow-specific daemons. Scheduled prompts and resources must remain visible, project-scoped, and inspectable.
- OpenVibely also supports a Native Alert alternative using project-scoped actionable notifications, explicit human approval, atomic claims, and implementation-task linkage. Detailed contracts live in `alerts_and_actionable_notifications.md` and `automation_graphs.md`.
- Both bootstrap skills require visible task and schedule creation. Optional Automation resource registration is used only when its runtime capability is exposed; absence is reported without inventing a fallback or invalidating otherwise complete setup.

Current product-completeness themes from `VISION.md` expected to require recurring work include outcome-to-work decomposition, multi-agent team coordination, reviewable autonomy UX, durable learning quality, external integrations, operational clarity, and provider/model normalization.
