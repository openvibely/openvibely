---
name: coding_agent_product_discipline
type: feedback
created: 2026-05-11
updated: 2026-08-10
source: update_memory
source_id: 8d5b3bfbbe566f1705eceea0442c8cc9:bee8fb5ef555e021
confidence: high
title: Coding Agent Product Discipline
---

This memory stores durable user preferences and product-discipline decisions for coding agents working on OpenVibely. Full execution runbooks belong in project skills.

User interaction preferences:
- For design, behavior, or feasibility questions, answer directly without making implementation changes unless explicitly requested.
- Prefer prompt or configuration corrections when they are sufficient; do not change runtime or product code merely because a model-facing prompt can express the intended behavior. Add code enforcement only when an authoritative invariant must also cover manual, forged, or otherwise prompt-bypassing inputs.
- Do not describe unreleased feature contracts as legacy or preserve compatibility shims for unreleased API/UI shapes unless the user explicitly asks for migration compatibility.
- If a prior response made an unsolicited code change, acknowledge it and revert or ask before proceeding.
- Ask before rewriting or collapsing meaningful Git history, including rebuilding a long task branch as one net-tree commit to avoid rebase conflicts. Preserve a clearly named backup ref when history must be recovered or rewritten, but do not treat that backup as a substitute for user approval.
- Treat requests explicitly limited to memory or skill maintenance as a hard scope boundary: do not add implementation, generated-file, test, rebase, or other repository changes unless the user separately requests them; clearly distinguish any pre-existing or later-instructed code work when reporting the diff.
- Treat an explicit path-limited change request as a hard diff boundary. Remove or revert unrelated in-branch work when necessary so the final net diff contains only the named path or paths, even when the original task requested broader artifacts.
- Treat explicit audit exclusions as equally hard scope boundaries. In particular, when the user excludes managed memory, do not inspect, cite, reconcile, or use managed or tracked memory as audit evidence; audit only the requested repository/publication artifacts. When the user says to ignore or “forget” a PR/publication state during an audit, scope the audit to the requested implementation or repository evidence and do not block the audit verdict on PR status, diff, or issue-publication gaps.
- Do not delegate tasks or create child-agent work. This prohibition applies especially to memory updates; use only direct capabilities available in the current task and report a capability blocker when direct work is unavailable.
- When diagnosing autonomous integration loops or system-owned schedules, do not manually forward, wake, patch, or otherwise push one live object through as a substitute for fixing the product path. The user wants the implementation to work properly end-to-end; validation should prove the scheduled/tool/runtime behavior works without ad hoc live intervention unless the user explicitly asks for a one-off operational action.
- When the user has already explicitly requested an outbound action such as sending an email/message, attempt the available configured/runtime mechanism instead of asking for redundant confirmation; if no viable send path exists, report the completed work and the send limitation clearly.
- Prefer plain, direct explanations over jargon-heavy phrasing; if the user asks for “no word salad,” respond with a terse concrete summary and one or two representative examples rather than audit-style detail. For user-facing UI/docs copy, avoid internal tool-name or architecture jargon unless it is clearly marked as advanced/reference text.
- Bug explanations should include the causal chain, concrete failure mode, and exact affected path when the user asks for detail.
- Summaries should be concrete: cite specific files, symbols, handlers, tests, behavior affected, verification performed, and whether a real git diff exists when available.
- If a user challenges why there is no diff after a claimed coding change, inspect branch pointers, status, reflog, and file contents, then plainly correct any prior summary based on non-persisted or stale output.
- Broad reviews should actively look for mistakes, unintended diff, dead code, and verification gaps. If repeated reviews find one issue at a time, the user may request audit-only mode: return a consolidated ranked problem list before making fixes.
- When a task requires a final audit-only review, perform it in a separate strictly read-only turn after implementation and fixes are complete. If that audit finds a material issue, stop after reporting it; fix it only in a later turn, then run a fresh audit-only review from scratch. A task is not complete until such an audit reports no material bugs, regressions, or missing requirements.
- Do not present an audit verdict, “no material issues” conclusion, or “strictly read-only” compliance claim as verified unless the supporting repository inspection and validation were actually performed in that turn. If build/tests/generation checks are not run because they would be out of scope, modifying, unavailable, or intentionally skipped, state that limitation plainly instead of implying verification.
- When multiple findings are variants of one bug class, fix or audit the whole analogous class instead of narrowly addressing one instance.
- Audit-only turns must use available repository/file-reading tools before claiming a tooling blocker. Route continuations requiring code inspection to a coding-capable runtime; a capability listing alone is not evidence that ordinary coding tools are unavailable.
- When an audit goal excludes managed memory or skills, make zero memory/skill tool calls for the entire audit turn, including orientation. If memory/skills are allowed but their drift must be ignored, gather repository/worktree evidence before any memory or skill lookup; memory-first or skill-first orientation invalidates a clean audit verdict. A violated audit is invalid and must be repeated in a fresh compliant turn.
- Redundancy Finder rotation state as of 2026-08-10: recent inspected areas include outbound targets, GitHub PR feedback, channel test/auth adapters, Automation schedules, Models/OAuth callback/manual completion, task-goal runtime actions across web/API Chat and shared channel runtimes, the alert/notification handler post-mutation refresh path, Automation Graph save compilation/materialization validation, the task/worktree Changes rendering workflow, task-board HTMX kanban refresh assembly across mutation handlers, and dashboard page-shell project selection/rendering across Analytics, Insights, Backlog, Autonomous, and Architect handlers. The task-goal runtime action duplication was published as GitHub issue `#333`, the alert handler refresh duplication as issue `#351`, the Automation save validation duplication as issue `#356`, the task/worktree Changes diff/state resolution duplication as issue `#378`, the task-board kanban refresh duplication as issue `#383`, and dashboard page-shell duplication as issue `#392`; future redundancy-finder runs should pick a different bounded component unless explicitly asked to revisit those workflows.
- When reviewing a messy fix stack after a difficult bug, inspect the actual commit diffs and rerun relevant validation before recommending keep/drop; distinguish the commit that directly fixed the observed symptom from defensive hardening that may still be useful.
- Draft reusable skills that the user has not approved for publication should stay local/ignored by default. If the user wants in-app testing before release, keep the skill indexed and ensure the package body exists in the checkout the app loads; do not hide it by removing the index.
- When a feature must consume an established skill's prompts or assets, keep the skill's existing behavior and documentation intact unless redesign was explicitly requested. Prefer the smallest shared canonical asset extraction or mirroring needed by the new consumer; do not substantially rewrite the skill or replace broad skill-content tests as incidental feature work. If the implementation reuses prompt bodies from an unchanged canonical file, explicitly state that provenance, explain why the bodies are absent from the diff, and disclose any runtime dependency on that file's formatting.

Model-facing prompt preferences:
- Use direct role/capability wording over low-value internal/product labels unless the label affects authorization, routing, or correctness.
- Avoid backend provenance/category labels that do not help the LLM, including “generated skill(s),” “protected/system agents,” “non-protected agents,” and “manually assigned agent.” Prefer behavior terms like “standalone skill(s),” “protected agents,” “user-managed agents,” and “assigned agent.”
- For protected or scheduled agents, `System:` may remain in storage/UI names when it is a real identity, but model-facing prompt bodies and hook inputs should avoid `System:` headings or prefixes unless they affect behavior.
- Do not inject the product/project name into prompts merely to make them sound project-specific.
- Prefer long model prompts as readable const templates with dynamic context interpolated, rather than chains of `WriteString` calls.
- Reusable skills/runbooks should avoid naming specific current-release features as examples; encode generic decision rules and feature-neutral examples instead.
- Goal Agent behavior must preserve the generic model-evaluator design and avoid deterministic or objective-keyword completion logic; detailed boundaries live in `agent_lifecycle_and_skills.md`.

Documentation, logging, and validation preferences:
- Preserve useful README content and commented multi-line command examples unless there is a specific reason to trim them.
- Preserve liked README/docs structure while folding in stronger positioning/selling points.
- Keep root `docs/` in sync with README/docs-site positioning when overlapping product concepts change. When syncing with `/Users/dubee/go/src/github.com/openvibely/openvibely-docs`, audit recent docs-site content and propagate overlapping product-concept updates beyond a narrow README/environment pass.
- Root README should stay succinct and high-level, point to `https://docs.openvibely.ai` plus the docs source repo, and keep detailed environment-variable reference in `docs/environment.md`. Keep Docker storage guidance to the essential requirement that mounted `/data` be writable by UID/GID `10001:10001`; do not add legacy-volume ownership migration prose, commands, tests, or compatibility guidance unless the user explicitly reverses this decision.
- Published docs links in README/project-facing docs should use new-tab HTML anchors where supported; local relative links stay normal Markdown.
- Very high-frequency or low-value debug traces should be commented `applog.Debugf` examples instead of active gated calls when method-call overhead could accumulate, especially per LLM chunk, SSE delta, HTMX poll, diff broadcast tick, or action-routing check.
- Full validation should prefer project Makefile targets or `go test ./... -count=1 -timeout 120s`; detailed test-state and timeout caveats live in `testing_coverage_and_performance.md`.
- For merge-conflict resolutions confined to Markdown files, the user prefers skipping tests/build unless code or generated artifacts are touched.
- Match validation intensity to the change. For prompt-only changes, use focused prompt/template contract tests plus the normal build and, when shared templates are affected, the normal suite; do not run the race detector unless concurrency code changed or the user explicitly requests it.
- Release workflow must include a documentation update pass for new or meaningfully changed features before publishing/tagging.
- Release agents should install missing required local tools such as `gh` when feasible instead of treating them as immediate blockers; hand back only if installation/authentication fails or requires unavailable credentials/permissions.
- Docker image publishing remains documented as a manual/pending release step unless explicit Docker credentials/tooling are present.

Release-note preferences:
- Release notes are AI-synthesized from structured unreleased commit context because commit subjects are often terse.
- `Highlights` must summarize what is new in the target release from the changelog/commit range, not repeat static product feature bullets from previous releases; `What's Changed` is the detailed changelog section.
- Describe user-facing capability by what it does, not by incidental UI controls or generic labels.
- Do not call out minor model-selector additions as standalone release-note features unless the user explicitly asks or the model integration is itself a major release theme.
- High-level release notes should omit CI/test infrastructure, terminal log verbosity, low-level bug/reliability patches, and minor UI polish unless the audience explicitly needs those details or the fix affects a core workflow.
- Release-note bullets should use bolded lead labels/sections, following `- **Feature or theme** — Details...`.

Release boundaries:
- Verify live git refs, tags, and GitHub release state before resuming any release; stored release snapshots are not authoritative.
- Release artifacts normally cover macOS desktop bundles and darwin/linux/Windows server archives with checksums. Windows desktop packaging requires a MinGW cross-compiler; Docker publishing remains pending when credentials are unavailable.
- Release version policy is centralized in `.openvibely/skills/openvibely_release_workflow/scripts/release-version.sh`: all release entrypoints and validation use the same side-effect-free `X.Y.Z`/optional-leading-`v` rules and reject invalid versions before performing release work.
- Release-build invariants include preserving `OpenVibely.app` as the zip root, making dry runs fully non-writing, avoiding managed worktree cleanup paths for real builds, and using script-default or absolute dist paths.
- `.openvibely/skills/openvibely_release_workflow/scripts/release.sh` is currently tracked without its executable bit; invoke release rehearsals through `bash` until the repository mode is corrected. Direct execution fails with exit 126 before the script starts.
