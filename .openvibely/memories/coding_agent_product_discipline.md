---
name: coding_agent_product_discipline
type: feedback
created: 2026-05-11
updated: 2026-08-29
source: after_complete
source_id: b33987fc9bad30fe543abdb07057f162:1b619e9cce9d807b
confidence: high
title: Coding Agent Product Discipline
---

This memory stores durable user preferences and product-discipline decisions for coding agents working on OpenVibely. Full execution runbooks belong in project skills.

User interaction and scope preferences:
- For design, behavior, or feasibility questions, answer directly without making implementation changes unless explicitly requested.
- Prefer prompt or configuration corrections when they are sufficient; change runtime/product code only when an authoritative invariant must cover manual, forged, or prompt-bypassing inputs.
- Maintained Automation template changes require a template-revision bump plus an explicit update/edit/save/recreation path; do not hide point-in-time saved-template changes behind runtime swaps.
- Do not describe unreleased feature contracts as legacy or preserve compatibility shims for unreleased API/UI shapes unless explicitly requested.
- If a prior response made an unsolicited code change, acknowledge it and revert or ask before proceeding.
- Ask before rewriting or collapsing meaningful Git history. Preserve a clearly named backup ref when history must be recovered or rewritten, but do not treat that backup as approval.
- Treat memory/skill-maintenance-only requests, explicit path limits, and audit exclusions as hard scope boundaries. Do not add repository changes, generated files, tests, rebase work, or implementation work outside the requested boundary.
- For narrow rebase or fast-forward-conflict repair requests, do not expand into Docker/Linux reproduction or broad CI triage unless explicitly asked; complete the requested rebase/publication verification and report remaining CI state separately.
- When managed memory is explicitly excluded from an audit, do not inspect, cite, reconcile, or use it as evidence. Ground the verdict in repository/worktree evidence.
- Do not delegate tasks or create child-agent work unless explicitly asked, especially for memory updates. If an audit must stay in the current task, perform it here rather than creating or polling another task.
- When diagnosing autonomous loops or system-owned schedules, do not manually push live objects through as a substitute for fixing the product path; validation must prove scheduled/tool/runtime behavior end to end.
- Do not introduce role/task-type-specific runtime tool narrowing for Automation-owned scheduled tasks. Keep them ordinary generic tasks with the shared tool surface; add missing tools to shared allow-lists and enforce excessive authority at the service layer.
- If an outbound send was already requested, attempt the configured/runtime mechanism instead of asking for redundant confirmation; report a missing send path clearly.
- Prefer plain, direct explanations. When asked for “no word salad,” give a terse concrete summary and one or two examples.
- Detailed bug explanations should include the causal chain, concrete failure mode, and affected path. Summaries should cite files, symbols, tests, behavior, verification, and whether a real git diff exists when relevant.

Audit and review discipline:
- Broad reviews should look for unintended diff, dead code, mistakes, and verification gaps. When repeated reviews find one issue at a time, an audit-only turn may first produce a consolidated ranked problem list before fixes.
- A required final audit-only review is a separate strictly read-only turn after implementation and fixes. If it finds a material issue, stop and report it; fix later and run a fresh audit from scratch.
- Do not claim an audit verdict, “no material issues,” or strict read-only compliance without repository inspection and the validation actually performed. Disclose skipped build, tests, generation, or publication checks.
- Fix or audit the whole analogous bug class when findings share one mechanism.
- Audit-only turns must use repository/file-reading tools before claiming a tooling blocker. Capability listing alone is not evidence.
- Code review may use memory or skills only as background unless explicitly excluded; verdicts must rest on repository and publication evidence.
- When reviewing a fix stack, inspect actual commit diffs and rerun relevant validation before recommending keep/drop. If a claimed change has no diff, inspect branch pointers, status, reflog, and file contents, then correct the summary plainly.
- A strict audit must not edit files or run write-capable formatting, build, test, generation, or other mutating commands, even if the final worktree is clean. Disclose any violation and require a fresh audit.
- Raw-key presence checks for JSON numeric fields must account for `encoding/json` case-insensitive struct-field matching; explicit `{"Limit":0}` must not bypass invalid-zero validation through an omitted/default path.
- Stateful mutations must preserve and verify pre-mutation eligibility before stop/cancel side effects. Reloading after a category write can lose the origin state and cancel work that was not originally Active.
- Validation claims must match the exact reviewed head/base. If a broad suite fails on a baseline or unrelated regression, report the narrower passing scope and the broad failure rather than claiming the broad suite passed.
- PR handoffs have repeatedly exposed stale or contaminated publication metadata. Re-read the authoritative live PR body, head, file list, target, checks, and issue linkage; compare the live tree/blob content and changed-file set with the exact validated local head/base, not only filenames. A green check run on a different or contaminated commit does not validate the audited patch; a live-head mismatch, unrelated hunks, deleted coverage, stale validation claims, or missing dependency files is a material publication/scope blocker. If a supported reuse path ignores `pr_body` and no body-only operation exists, report the handoff blocker instead of unsafe branch publication or unauthorized API mutation.

Prompt and model-facing preferences:
- Use direct role/capability wording over low-value internal labels unless a label affects authorization, routing, or correctness.
- Avoid backend provenance/category labels that do not help the LLM. Prefer “standalone skills,” “protected agents,” “user-managed agents,” and “assigned agent.”
- `System:` may remain in storage/UI names for real identity, but model-facing prompts and hook inputs should avoid it unless behavior depends on it.
- Do not inject the product/project name into prompts merely to sound project-specific.
- Prefer long model prompts as readable const templates with interpolated dynamic context, not chains of `WriteString` calls.
- Reusable skills/runbooks should use generic decision rules and feature-neutral examples rather than naming current-release features.
- Goal Agent behavior must remain a generic model evaluator; do not add deterministic or objective-keyword completion logic.
- GitHub/Native SDLC duplicate prevention should lead with user-visible behavior: list existing bot-created work, compare candidates, hydrate only likely matches, skip covered findings, continue searching, and create at most one new finding per run. Mention `idempotency_key` only when explaining why it remains outside finder prompts.
- Draft reusable skills not yet approved for publication should stay local/ignored by default. For in-app testing, keep the skill indexed and ensure its package body exists in the checkout loaded by the app.
- When a feature consumes an established skill's prompts/assets, keep the skill behavior/docs intact unless redesign was requested; extract or mirror only the smallest canonical asset and disclose provenance/runtime formatting dependencies.

Documentation, logging, validation, and release preferences:
- Preserve useful README content and commented multi-line command examples unless there is a specific reason to trim them.
- Preserve liked README/docs structure while folding in stronger positioning. Keep root `docs/` synchronized with README/docs-site positioning when overlapping concepts change.
- Root README stays succinct and high-level, points to `https://docs.openvibely.ai` plus the docs source repo, and keeps detailed environment-variable reference in `docs/environment.md`.
- Docker storage docs must state that mounted `/data` be writable by UID/GID `10001:10001`; do not add legacy-volume migration prose, commands, tests, or compatibility guidance unless requested.
- Published docs links in README/project-facing docs should use new-tab HTML anchors where supported; local relative links stay Markdown.
- Very high-frequency or low-value debug traces should be commented `applog.Debugf` examples instead of active gated calls when method-call overhead could accumulate.
- Full validation should prefer project Makefile targets or `go test ./... -count=1 -timeout 120s`; prompt-only changes use focused prompt/template contract tests plus the normal build/suite when shared templates are affected.
- Coverage work requires real tests and realistic hosted-coverage estimates, not Codecov configuration/profile workarounds or optimistic local percentages.
- For Markdown-only merge-conflict resolutions, skip tests/build unless code or generated artifacts are touched.
- Release workflow includes a documentation update pass for new or meaningfully changed features before publishing/tagging.
- Release agents should install missing required local tools such as `gh` when feasible; hand back only if installation/authentication fails or requires unavailable credentials/permissions.
- Docker image publishing remains manual/pending unless explicit Docker credentials/tooling are present.

Release-note and release-boundary preferences:
- Release notes are AI-synthesized from structured unreleased commit context because commit subjects are often terse.
- `Highlights` summarizes what is new in the target release; `What's Changed` is the detailed changelog.
- Describe user-facing capability by what it does, not by incidental controls or generic labels.
- Omit CI/test infrastructure, terminal log verbosity, low-level patches, and minor UI polish from high-level notes unless they affect a core workflow.
- Release-note bullets use bolded lead labels such as `- **Feature or theme** — Details...`.
- Verify live git refs, tags, and GitHub release state before resuming a release; stored snapshots are not authoritative.
- Release artifacts normally cover macOS desktop bundles and darwin/linux/Windows server archives with checksums. Windows desktop packaging requires a MinGW cross-compiler; Docker publishing remains pending when credentials are unavailable.
- Release version policy is centralized in `.openvibely/skills/openvibely_release_workflow/scripts/release-version.sh`.
- Release-build invariants include preserving `OpenVibely.app` as the zip root, making dry runs fully non-writing, avoiding managed-worktree cleanup paths for real builds, and using script-default or absolute dist paths.
- `.openvibely/skills/openvibely_release_workflow/scripts/release.sh` is tracked without executable bit; invoke release rehearsals through `bash` until mode is corrected.

Current durable rotation guidance:
- Redundancy Finder and Bug Finder should choose a new bounded component rather than immediately rechecking recently inspected areas, unless explicitly asked to revisit them or their state materially changes.
