---
name: coding_agent_product_discipline
type: feedback
created: 2026-05-11
updated: 2026-09-15
source: consolidation
source_id: memory_consolidation_2026-09-15
confidence: high
title: Coding Agent Product Discipline
---

This topic stores durable user preferences and product-discipline decisions. Detailed execution runbooks belong in project skills.

Interaction and scope:
- Answer design, behavior, and feasibility questions directly without changing code unless implementation is explicitly requested. Prefer prompt/configuration fixes for authoritative invariants; add runtime validation when manual or forged input must also be safe.
- Respect hard scope boundaries. Do not make unsolicited changes, add unrelated code/tests/tasks, rewrite meaningful Git history, or manually push schedules/Automation objects to simulate autonomy. Automation-owned scheduled tasks remain ordinary generic tasks with shared tools; authority belongs in services.
- The user strongly prefers avoiding unnecessary manual clicks and repetitive UI repair paths; prefer safe automatic adoption or one account-level action over per-item edits or extra confirmations, without weakening authorization, credential-isolation, or privacy boundaries.
- Treat already-applied goose migrations as immutable for existing installations. Editing an old migration is only preventive for fresh databases or upgrades from before that version; existing-database repairs need a later migration or explicit recovery path.
- When the user explicitly narrows a turn to implementation, honor that boundary and do not inspect hosted checks, PR status, or other publication state unless they ask for it.
- An explicit audit-only request means inspect only: do not edit, generate, format, build, test, commit, push, or mutate filesystem/external state. Redirecting output into temporary files is also a filesystem mutation. If that boundary is crossed, do not issue an audit verdict; require cleanup and a fresh audit.
- Audit claims require inspection of the exact repository/worktree and reviewed head/base. Never transfer validation across a changed HEAD or claim broad success over known baseline failures. Real repeated user interaction failures outrank synthetic or fixture-only evidence; reproduce current UI with browser-generated input. An exact-head hosted check that is failed or still pending blocks a clean publication verdict; if its logs are inaccessible, leave the cause unresolved rather than labeling it baseline or transient.
- Do not launch Docker for routine work. Docker publication requires explicit credentials/tooling. When a diagnostic appears hung, inspect process ownership and use bounded diagnostics rather than rerunning or killing unrelated work.
- Maintained Automation templates are point-in-time snapshots: template changes require a revision bump and explicit update/edit/save or recreation. Do not add compatibility shims for unreleased shapes without a request.

Prompt and model-facing style:
- Use direct capability/role wording. Avoid backend provenance/category labels and `System:` in model-facing prompts unless they affect authorization, routing, or correctness; do not inject the product name for flavor.
- Keep long prompts readable and reusable. Skills should use generic decision rules and feature-neutral examples; Goal Agent remains a model evaluator, not keyword/objective-keyword logic.
- Duplicate prevention leads with existing-work search, candidate hydration, covered-finding skipping, continued search, and at most one new finding. `idempotency_key` is backend context, not the main model instruction.
- Preserve established skill behavior when extracting shared prompts/assets, mirror only the smallest canonical asset, and disclose provenance/runtime formatting dependencies.

Docs, validation, and release:
- Preserve useful README structure and commented examples. Keep root README high-level, link to `https://docs.openvibely.ai`, and keep detailed environment variables in `docs/environment.md`. Synchronize overlapping root/docs-site positioning.
- Docker documentation must state that mounted `/data` is writable by UID/GID `10001:10001`; avoid legacy migration guidance without a request. Raw LLM/user content and high-frequency traces must be debug-gated.
- Prefer Makefile validation or `go test ./... -count=1 -timeout 120s`; prompt changes need focused contract tests plus normal build/suite when shared templates change. Markdown-only conflict repairs need no build/test.
- Release work includes documentation and verification of live refs, tags, releases, artifacts, and checks. Standard artifacts cover macOS desktop bundles and darwin/linux/Windows server archives with checksums; Windows desktop packaging needs MinGW. Do not publish Docker without explicit credentials.
- Release versioning is centralized in `.openvibely/skills/openvibely_release_workflow/scripts/release-version.sh`. Preserve `OpenVibely.app` as ZIP root, keep dry runs non-writing, and invoke the tracked non-executable `release.sh` with `bash` until its mode is corrected. Release notes use structured commit context with user-facing `Highlights` and detailed `What's Changed`.
- Redundancy/Bug Finder work should rotate to a new bounded component instead of repeatedly inspecting the same area unless asked or materially changed.
