---
name: coding_agent_product_discipline
type: feedback
created: 2026-05-11
updated: 2026-07-15
source: consolidation
source_id: memory_consolidation_2026_07_15
confidence: high
title: Coding Agent Product Discipline
---

This memory stores durable user preferences and product-discipline decisions for coding agents working on OpenVibely. Full execution runbooks belong in project skills.

User interaction preferences:
- For design, behavior, or feasibility questions, answer directly without making implementation changes unless explicitly requested.
- Do not describe unreleased feature contracts as legacy or preserve compatibility shims for unreleased API/UI shapes unless the user explicitly asks for migration compatibility.
- If a prior response made an unsolicited code change, acknowledge it and revert or ask before proceeding.
- When diagnosing autonomous integration loops, do not manually forward, wake, or otherwise push one live message through as a substitute for fixing the product path. The user wants the implementation to work properly end-to-end; validation should prove the scheduled/tool/runtime behavior works without ad hoc live intervention unless the user explicitly asks for a one-off operational action.
- When the user has already explicitly requested an outbound action such as sending an email/message, attempt the available configured/runtime mechanism instead of asking for redundant confirmation; if no viable send path exists, report the completed work and the send limitation clearly.
- Prefer plain, direct explanations over jargon-heavy phrasing; if the user asks for “no word salad,” respond with a terse concrete summary rather than audit-style detail. For user-facing UI/docs copy, avoid internal tool-name or architecture jargon unless it is clearly marked as advanced/reference text.
- Bug explanations should include the causal chain, concrete failure mode, and exact affected path when the user asks for detail.
- Summaries should be concrete: cite specific files, symbols, handlers, tests, behavior affected, verification performed, and whether a real git diff exists when available.
- If a user challenges why there is no diff after a claimed coding change, inspect branch pointers, status, reflog, and file contents, then plainly correct any prior summary based on non-persisted or stale output.
- Broad reviews should actively look for mistakes, unintended diff, dead code, and verification gaps. If repeated reviews find one issue at a time, the user may request audit-only mode: return a consolidated ranked problem list before making fixes.
- When multiple findings are variants of one bug class, fix or audit the whole analogous class instead of narrowly addressing one instance.
- When reviewing a messy fix stack after a difficult bug, inspect the actual commit diffs and rerun relevant validation before recommending keep/drop; distinguish the commit that directly fixed the observed symptom from defensive hardening that may still be useful.
- Draft reusable skills that the user has not approved for publication should stay local/ignored by default. If the user wants in-app testing before release, keep the skill indexed and ensure the package body exists in the checkout the app loads; do not hide it by removing the index.

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
- Root README should stay succinct and high-level, point to `https://docs.openvibely.ai` plus the docs source repo, and keep detailed environment-variable reference in `docs/environment.md`.
- Published docs links in README/project-facing docs should use new-tab HTML anchors where supported; local relative links stay normal Markdown.
- Very high-frequency or low-value debug traces should be commented `applog.Debugf` examples instead of active gated calls when method-call overhead could accumulate, especially per LLM chunk, SSE delta, HTMX poll, diff broadcast tick, or action-routing check.
- Full validation should prefer project Makefile targets or `go test ./... -count=1 -timeout 120s`; detailed test-state and timeout caveats live in `testing_coverage_and_performance.md`.
- Release workflow must include a documentation update pass for new or meaningfully changed features before publishing/tagging.
- Release agents should install missing required local tools such as `gh` when feasible instead of treating them as immediate blockers; hand back only if installation/authentication fails or requires unavailable credentials/permissions.
- Docker image publishing remains documented as a manual/pending release step unless explicit Docker credentials/tooling are present.

Release-note preferences:
- Release notes are AI-synthesized from structured unreleased commit context because commit subjects are often terse.
- `Highlights` must summarize what is new in the target release from the changelog/commit range, not repeat static product feature bullets from previous releases; `What's Changed` is the detailed changelog section.
- Describe user-facing capability by what it does, not by incidental UI controls or generic labels.
- For the 0.2.0 skill work, describe the Skill Curator as a recursive self-improvement loop that creates/patches skills from task learnings, not as a generic “Skills overhaul.”
- Do not call out minor model-selector additions as standalone release-note features unless the user explicitly asks or the model integration is itself a major release theme.
- High-level release notes should omit CI/test infrastructure, terminal log verbosity, low-level bug/reliability patches, and minor UI polish unless the audience explicitly needs those details or the fix affects a core workflow.
- Release-note bullets should use bolded lead labels/sections, following `- **Feature or theme** — Details...`.

Current release facts and boundaries:
- Recorded release state: latest public release was `v0.3.0` (2026-06-24), whose canonical annotated tag targets `bfcc37d479e4f5f2c6784a900f5a6672ec754bdb`. `origin/main` was later fast-forwarded to the `v0.4.0` candidate `a13ab410c84a9ff26128d72ff6165841bcabfbf7`, but no `v0.4.0` tag or GitHub release had been created at the last recorded state. Verify live refs and GitHub release state before resuming release work.
- The recorded `v0.4.0` themes are multi-agent swarms, Discord/Email and project-scoped outbound messaging, autonomous GitHub issue-to-PR workflows, and Mixture of Models.
- Release artifacts normally cover macOS desktop bundles and darwin/linux/Windows server archives with checksums. Windows desktop packaging requires a MinGW cross-compiler; Docker publishing remains pending when credentials are unavailable.
- GitHub issue #29 was implemented in PR #35 on 2026-07-15: `.openvibely/skills/openvibely_release_workflow/scripts/release-version.sh` is now the side-effect-free shared `X.Y.Z`/optional-leading-`v` policy used by all five release entrypoints and `release-validate.sh`. The validation suite exercises the production helper and verifies accepted forms, early invalid-version rejection, and source safety without cwd or caller shell-option mutation.
- Release-build invariants include preserving `OpenVibely.app` as the zip root, making dry runs fully non-writing, avoiding managed worktree cleanup paths for real builds, and using script-default or absolute dist paths.
