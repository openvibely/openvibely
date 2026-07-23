---
name: product_vision_and_autonomy
type: project
created: 2026-06-10
updated: 2026-07-23
source: consolidation
source_id: memory_consolidation_2026_07_23
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
- The user wants GitHub issues and PRs to act as the durable mailbox/status board for autonomous product development: suggestions, approved work, active implementation, PR review, and completion should be visible in GitHub rather than only internal OpenVibely tasks.
- Desired review boundaries are GitHub-centered and manual at key gates: Offering Manager-style agents may open suggestion issues from `VISION.md`; Bug Finder, Optimization Finder, and Redundancy Finder scheduled tasks inspect focused components and open GitHub issues only; humans approve by assigning issues to the configured OpenVibely GitHub inbox identity; Dev Inbox creates OpenVibely implementation tasks from assigned issues; implementation tasks open PRs; and humans review/merge PRs in GitHub. Labels such as `approved`, `feature`, `bug`, `performance`, or `duplication` are useful organization/scope signals but are not required approval gates by default.
- Offering Manager suggestions should prioritize small gaps that materially deepen the core product loop of Chat coordination, task execution, review, learning, and human control. The user rejected the minor queued-worker-capacity UI suggestion in closed issue `openvibely/openvibely#11`; a stronger accepted direction was the product-defining Chat-to-task completion loop filed as `openvibely/openvibely#12`. A distinct reviewability gap is tracked in `openvibely/openvibely#28`: ordinary tasks expose execution and merge status plus transient revision comments, but no durable human review disposition such as awaiting review, changes requested, or approved; the bounded proposal keeps approval-as-a-merge-gate out of scope.
- `openvibely/openvibely#41` tracks a related surfacing gap found by the daily Offering Manager loop: linked GitHub PR status/URL (from `TaskPullRequest`) is persisted but only rendered deep in a task's Changes-tab dropdown, never on the Kanban board or task cards.
- `openvibely/openvibely#49` tracks Goal Agent observability on task cards: persisted goals have active, paused, achieved, blocked, failed, and cleared states, but the board currently collapses every non-cleared state into a generic Goal badge and misleading active-goal tooltip. The bounded suggestion is status-aware card visibility without changing Goal Agent behavior, filtering, or goal mutation.
- Read-only Chat schedule discovery is tracked canonically in `openvibely/openvibely#61`; its durable capability boundary lives in `chat_thread_system.md`.
- `openvibely/openvibely#72` tracks a bounded, read-only Chat action for inspecting prompt-safe task lifecycle evidence already represented in the task Lifecycle UI, including hook outcomes, selected skills, and recalled memories; exposing raw lifecycle prompts or outputs is out of scope.
- The bundled reusable `openvibely_github_autonomous_sdlc_bootstrap` is the supported minimum prompt-driven GitHub autonomous SDLC loop and is documented in `docs/github-autonomous-sdlc-user-guide.md`. Its maintained template and execution-invariant support files are the canonical prompt sources rather than duplicated prompt text in `SKILL.md`. It creates one visible scheduled task per loop role, converts actionable assigned issues into distinct implementation tasks, forwards authorized PR feedback, opens or reuses PRs, and leaves review and merge to humans. It intentionally excludes hidden daemons, instant webhook reaction, auto-merge, dashboards, and advanced event dedupe.
- The bootstrap contract uses PAT-owner assignment or configured GitHub Authorized Users as approval, keeps scanner tasks issue-only, gives persisted goals to implementation work rather than recurring loop tasks, avoids `openvibely:` labels, and keeps project-scoped skill overrides possible.
- The GitHub-centered approach should reuse the older Vision Driver ideas of durable goals, schedules, dynamic wakeups, user-priority-first handling, task chaining, and review-gated autonomy, but move external coordination/status to GitHub issues/PRs.
- The recorded hosted topology runs Dev Inbox hourly, code-finder tasks daily, and Loop Auditor weekly. Dev Inbox forwards authorized PR feedback before assigned-issue processing; finder prompts only open issues, bot/self comments are ignored, recurring loop tasks have no persisted goals, and correctness must be validated through the autonomous path rather than manual message forwarding or wakeups.
- GitHub autonomous-SDLC support should remain generic: provide reusable GitHub/runtime/control-plane tools that can be used in many workflows, avoid bespoke hidden SDLC daemons or workflow-specific services unless a generic primitive is actually needed, and make the bootstrap skill set up prompt-driven visible schedules/tasks such as “check GitHub for assigned issues matching the configured mailbox criteria” instead of hardcoded backend loops.
- OpenVibely also provides a supported native alternative through approval-based Alerts. The bundled `openvibely_native_autonomous_sdlc_bootstrap` skill uses generic project-scoped actionable notifications and the available approval, claim, task-linking, completion, failure, and retry tools to create implementation tasks only after human approval. The GitHub-backed workflow remains supported as an alternative.

Current product-completeness themes from `VISION.md` expected to require recurring work include outcome-to-work decomposition, multi-agent team coordination, reviewable autonomy UX, durable learning quality, external integrations, operational clarity, and provider/model normalization.
