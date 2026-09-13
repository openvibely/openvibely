---
name: coding_agent_product_discipline
type: feedback
created: 2026-05-11
updated: 2026-09-14
source: after_complete
source_id: ee67990987db304dc16ee885db599c95:022966fa7b2657b8
confidence: high
title: Coding Agent Product Discipline
---

This memory stores durable user preferences and product-discipline decisions for OpenVibely coding agents. Detailed execution runbooks belong in project skills.

Interaction and scope:
- Answer design, behavior, and feasibility questions directly without changing code unless implementation is explicitly requested. Prefer prompt/configuration fixes when they enforce the authoritative invariant; add runtime validation when manual, forged, or prompt-bypassing input must also be safe.
- Respect hard scope boundaries for memory/skill maintenance, path limits, and audit exclusions. An explicit audit-only request overrides implementation, repair, publication, or PR follow-up goals for that turn: inspect only and do not edit, generate, format, build, test, commit, push, or mutate external state. Do not add unrelated code, generated files, tests, rebase work, or child-agent tasks. Memory updates should be performed directly by the active lifecycle agent when authorized.
- Do not make unsolicited changes. Do not rewrite meaningful Git history without asking; if recovery requires rewriting, preserve a clearly named backup ref.
- Do not manually push live schedules or Automation objects to simulate autonomous behavior. Validate the real scheduled/tool/runtime path end to end. Automation-owned scheduled tasks remain ordinary generic tasks with the shared tool surface; add missing tools to shared allow-lists and enforce authority at the service layer.
- Attempt an already-requested outbound send through the configured mechanism and report a missing path clearly. Prefer concise, plain, direct explanations over exhaustive framing or “word salad”; bug reports should state the causal chain, failure mode, affected path, files/symbols, tests, verification, and whether a real diff exists.
- Do not launch Docker for routine task work unless the task explicitly requires it; Docker publication additionally requires explicit credentials/tooling.
- When a long validation or diagnostic command appears hung, do not rerun the same compound command. Inspect process ownership first, distinguish unrelated task worktrees/processes, use short bounded diagnostics with progress markers, and avoid terminating unrelated work.
- Maintained Automation templates are point-in-time snapshots: template changes require a revision bump and explicit update/edit/save or recreation. Do not call unreleased API/UI shapes legacy or add compatibility shims for them without a request.

Audit and review:
- Strict audit boundaries include all filesystem effects, not only product/Git changes: do not redirect inspection output into files or otherwise write temporary artifacts. If an audit-only turn crosses that boundary, do not issue an audit verdict; stop and require a separate cleanup/repair turn followed by a fresh audit from scratch.
- Never claim an audit verdict or broad validation result without inspecting the relevant repository/worktree and exact reviewed head/base. If a broad suite has baseline or unrelated failures, report the narrower passing scope and the broad failure.
- When a user repeats a real-world interaction failure after a clean audit, treat the report as higher-confidence evidence than synthetic or fixture-only coverage. Reproduce the exact current UI with browser-generated input and trace the request/authoritative-state path; keep synthetic events as supplemental evidence.
- Reviews of fix stacks inspect actual commit diffs, branch pointers, status, reflog, and file contents when summaries or diffs disagree. Fix the whole analogous bug class when findings share a mechanism.
- Publication handoffs must re-read the authoritative live PR body, head, base, file list, checks, issue linkage, review state, and `published_head_sha`; compare the live tree/blob set with the exact validated local tree. Live-head mismatch, unrelated hunks, stale validation, deleted coverage, missing dependencies, or unavailable body-only operations are blockers. A green check on another commit is not evidence for the audited patch.
- A normal `github_open_pull_request` reconciliation after a manual `--force-with-lease` cleanup can unexpectedly merge-forward a newly advanced local `main` into the published task branch, even when the desired candidate is already live, repolluting the PR with unrelated files. Recheck the current target and live refs before metadata reconciliation; preserve any unexpected merge head and restore the validated candidate with an exact lease rather than retrying blindly.
- Validation is not transferable across a changed HEAD: a clean merge-forward or merge commit that adds unrelated files makes predecessor tests stale. Reinspect and revalidate the exact current candidate before claiming completion, and treat unrelated contamination or missing exact-head evidence as a blocker.
- Validate raw-key presence for JSON numeric fields with `encoding/json` case-insensitive matching in mind. Preserve pre-mutation eligibility before stop/cancel side effects; do not reload after a category write and accidentally cancel work that was not originally Active.

Prompt and model-facing style:
- Use direct capability/role wording. Avoid backend provenance/category labels unless they affect authorization, routing, or correctness; avoid `System:` in model-facing prompts and do not inject the product name merely for flavor.
- Keep long prompts as readable constant templates with interpolated context. Reusable skills should use generic decision rules and feature-neutral examples. Goal Agent remains a generic model evaluator, not deterministic keyword or objective-keyword logic.
- Duplicate prevention guidance should lead with user-visible existing-work search, candidate hydration, covered-finding skipping, continued search, and at most one new finding; mention `idempotency_key` only as backend context. Draft reusable skills stay local/ignored until approved, but in-app tests need an indexed package body in the loaded checkout.
- Preserve established skill behavior/docs when extracting shared prompts/assets; mirror only the smallest canonical asset and disclose provenance/runtime formatting dependencies.

Docs, validation, and release:
- Preserve useful README structure and commented multi-line examples. Keep root README high-level, point to `https://docs.openvibely.ai` and its docs source, and keep detailed environment variables in `docs/environment.md`. Keep overlapping root/docs-site positioning synchronized; use new-tab HTML anchors for published links where supported.
- Docker docs must state that mounted `/data` is writable by UID/GID `10001:10001`; do not add legacy-volume migration guidance without a request. High-frequency/raw LLM traces should be debug-gated or commented rather than active noisy logging.
- Prefer Makefile validation or `go test ./... -count=1 -timeout 120s`; prompt-only changes need focused contract tests plus normal build/suite when shared templates change. Coverage work requires real tests and realistic hosted estimates. Markdown-only conflict repairs need no build/test unless code or generated artifacts change.
- Release work includes a documentation pass. Verify live refs, tags, and GitHub release state rather than trusting snapshots. Artifacts normally include macOS desktop bundles and darwin/linux/Windows server archives with checksums; Windows desktop packaging needs MinGW, and Docker publication stays manual when credentials/tooling are absent.
- Release versioning is centralized in `.openvibely/skills/openvibely_release_workflow/scripts/release-version.sh`. Preserve `OpenVibely.app` as the ZIP root, keep dry runs non-writing, avoid managed-worktree cleanup for real builds, use script-default or absolute dist paths, and invoke the non-executable tracked `release.sh` through `bash` until its mode is corrected.
- Release notes synthesize structured unreleased commit context: `Highlights` is user-facing, `What's Changed` is detailed, and bullets use bold lead labels. Omit incidental CI, logs, low-level patches, and minor polish from high-level notes.
- Do not publish Docker images without explicit credentials/tooling. Redundancy Finder and Bug Finder should rotate to a new bounded component instead of repeatedly inspecting the same area unless asked or materially changed.
