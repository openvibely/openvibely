---
name: coding_agent_product_discipline
type: feedback
created: 2026-05-11
updated: 2026-08-19
source: after_complete
source_id: fd37f46bb8f14052236f06e2550dd109:7058dea71bb8db9d
confidence: high
title: Coding Agent Product Discipline
---

This memory stores durable user preferences and product-discipline decisions for coding agents working on OpenVibely. Full execution runbooks belong in project skills.

User interaction and scope preferences:
- For design, behavior, or feasibility questions, answer directly without making implementation changes unless explicitly requested.
- Prefer prompt or configuration corrections when they are sufficient; change runtime/product code only when an authoritative invariant must cover manual, forged, or prompt-bypassing inputs.
- Do not describe unreleased feature contracts as legacy or preserve compatibility shims for unreleased API/UI shapes unless explicitly requested.
- If a prior response made an unsolicited code change, acknowledge it and revert or ask before proceeding.
- Ask before rewriting or collapsing meaningful Git history. Preserve a clearly named backup ref when history must be recovered or rewritten, but do not treat that backup as approval.
- Treat memory/skill-maintenance-only requests, explicit path limits, and audit exclusions as hard scope boundaries. Do not add repository changes, generated files, tests, rebase work, or implementation work outside the requested boundary.
- When managed memory is explicitly excluded from an audit, do not inspect, cite, reconcile, or use it as evidence. When memory/skill drift must be ignored, inspect repository/worktree evidence first and ground the verdict there.
- Do not delegate tasks or create child-agent work unless the user explicitly asks. This applies especially to memory updates.
- When diagnosing autonomous loops or system-owned schedules, do not manually push live objects through as a substitute for fixing the product path; validation should prove the scheduled/tool/runtime behavior works end-to-end.
- Do not introduce role/task-type-specific runtime tool narrowing for Automation-owned scheduled tasks. Keep them ordinary generic OpenVibely tasks with the shared tool surface; add missing tools to shared allow-lists and tighten service-layer authorization when a tool is too permissive.
- If the user already requested an outbound send action, attempt the configured/runtime mechanism instead of asking for redundant confirmation; if no viable send path exists, report the limitation clearly.
- Prefer plain, direct explanations over jargon. If the user asks for “no word salad,” give a terse concrete summary and one or two examples.
- Bug explanations should include the causal chain, concrete failure mode, and affected path when the user asks for detail.
- Summaries should be concrete: cite files/symbols/tests/behavior/verification and whether a real git diff exists when relevant.

Audit and review discipline:
- Broad reviews should actively look for mistakes, unintended diff, dead code, and verification gaps. If repeated reviews find one issue at a time, the user may request audit-only mode with a consolidated ranked problem list before fixes.
- When a task requires a final audit-only review, perform it in a separate strictly read-only turn after implementation and fixes are complete. If it finds a material issue, stop after reporting it; fix it only later, then run a fresh audit-only review from scratch.
- Do not present an audit verdict, “no material issues” conclusion, or “strictly read-only” compliance claim unless repository inspection and validation were actually performed in that turn. If build/tests/generation checks are skipped, state the limitation.
- When multiple findings are variants of one bug class, fix or audit the whole analogous class.
- Audit-only turns must use available repository/file-reading tools before claiming a tooling blocker. Capability listing alone is not evidence that coding tools are unavailable.
- Code review/audit work may use memory or skills only when not explicitly excluded, and only as background context; verdicts must be grounded in repository/publication evidence.
- When reviewing a messy fix stack, inspect actual commit diffs and rerun relevant validation before recommending keep/drop.
- If a user challenges a claimed coding change with no diff, inspect branch pointers, status, reflog, and file contents, then correct the summary plainly.

Prompt and model-facing preferences:
- Use direct role/capability wording over low-value internal labels unless the label affects authorization, routing, or correctness.
- Avoid backend provenance/category labels that do not help the LLM. Prefer behavior terms like “standalone skills,” “protected agents,” “user-managed agents,” and “assigned agent.”
- For protected or scheduled agents, `System:` may remain in storage/UI names when it is real identity, but model-facing prompts and hook inputs should avoid it unless behavior depends on it.
- Do not inject the product/project name into prompts merely to sound project-specific.
- Prefer long model prompts as readable const templates with dynamic context interpolated, not chains of `WriteString` calls.
- Reusable skills/runbooks should avoid naming specific current-release features as examples; encode generic decision rules and feature-neutral examples.
- Goal Agent behavior must preserve the generic model-evaluator design and avoid deterministic/objective-keyword completion logic.
- For GitHub/Native SDLC duplicate prevention, lead with user-visible behavior: list existing bot-created issues/notifications, compare candidates, hydrate only likely matches, skip covered findings, continue searching, and create at most one new finding per run. Do not frame the answer around `idempotency_key` unless explaining why it remains outside SDLC finder prompts.
- Draft reusable skills not yet approved for publication should stay local/ignored by default. If the user wants in-app testing before release, keep the skill indexed and ensure the package body exists in the checkout the app loads.
- When a feature consumes an established skill's prompts/assets, keep the skill behavior/docs intact unless redesign was requested. Prefer the smallest shared canonical asset extraction or mirroring needed by the new consumer, and disclose provenance/runtime formatting dependencies.

Documentation, logging, validation, and release preferences:
- Preserve useful README content and commented multi-line command examples unless there is a specific reason to trim them.
- Preserve liked README/docs structure while folding in stronger positioning/selling points. Keep root `docs/` in sync with README/docs-site positioning when overlapping product concepts change.
- Root README should stay succinct and high-level, point to `https://docs.openvibely.ai` plus the docs source repo, and keep detailed environment-variable reference in `docs/environment.md`.
- Docker storage docs should state the essential requirement that mounted `/data` be writable by UID/GID `10001:10001`; do not add legacy-volume migration prose/commands/tests/compatibility guidance unless the user reverses this.
- Published docs links in README/project-facing docs should use new-tab HTML anchors where supported; local relative links stay Markdown.
- Very high-frequency or low-value debug traces should be commented `applog.Debugf` examples instead of active gated calls when method-call overhead could accumulate.
- Full validation should prefer project Makefile targets or `go test ./... -count=1 -timeout 120s`; prompt-only changes use focused prompt/template contract tests plus normal build/suite when shared templates are affected.
- For Markdown-only merge-conflict resolutions, the user prefers skipping tests/build unless code or generated artifacts are touched.
- Release workflow must include a documentation update pass for new or meaningfully changed features before publishing/tagging.
- Release agents should install missing required local tools such as `gh` when feasible; hand back only if installation/authentication fails or requires unavailable credentials/permissions.
- Docker image publishing remains manual/pending unless explicit Docker credentials/tooling are present.

Release-note and release-boundary preferences:
- Release notes are AI-synthesized from structured unreleased commit context because commit subjects are often terse.
- `Highlights` must summarize what is new in the target release, not repeat static product feature bullets. `What's Changed` is the detailed changelog section.
- Describe user-facing capability by what it does, not by incidental controls or generic labels.
- Omit CI/test infrastructure, terminal log verbosity, low-level bug patches, and minor UI polish from high-level release notes unless they affect a core workflow.
- Release-note bullets should use bolded lead labels such as `- **Feature or theme** — Details...`.
- Verify live git refs, tags, and GitHub release state before resuming any release; stored release snapshots are not authoritative.
- Release artifacts normally cover macOS desktop bundles and darwin/linux/Windows server archives with checksums. Windows desktop packaging requires a MinGW cross-compiler; Docker publishing remains pending when credentials are unavailable.
- Release version policy is centralized in `.openvibely/skills/openvibely_release_workflow/scripts/release-version.sh`.
- Release-build invariants include preserving `OpenVibely.app` as the zip root, making dry runs fully non-writing, avoiding managed worktree cleanup paths for real builds, and using script-default or absolute dist paths.
- `.openvibely/skills/openvibely_release_workflow/scripts/release.sh` is tracked without executable bit; invoke release rehearsals through `bash` until mode is corrected.

Current durable rotation state:
- Redundancy Finder recently inspected task-goal runtime actions, alert handler refresh tails, Automation save validation, task/worktree Changes diff-state resolution, task-board kanban refresh, dashboard page-shell rendering, execution stream terminal mapping, attachment orphan cleanup, agent create/update dialog payload parsing, browser/runtime schedule mutations, project create/update repository-source handling, model/provider form normalization, channel connection-test feedback, channel authorization allowlist persistence, Task Templates dashboard refresh, Personality settings cards, Insights dashboard grade rendering, Analytics/Insights failed-task-pattern logic, execution status badge rendering, and HTMX toast notification/event-contract rendering (`#715`). Future Redundancy Finder runs should pick a different bounded component unless explicitly asked to revisit these workflows.
