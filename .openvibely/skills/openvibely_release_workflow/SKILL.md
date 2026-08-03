---
kind: openvibely.agent_skill
version: 1
skill:
    key: openvibely_release_workflow
    name: OpenVibely Release Workflow
    scope: project
    description: Automate the OpenVibely release process — preflight, artifact builds, AI-synthesized release notes, docs updates, and GitHub release publishing — for a given semver version.
---

# OpenVibely Release Workflow

Use this skill when creating a new OpenVibely release for a given semver version, or when creating, reviewing, or modifying OpenVibely release automation and GitHub release notes. It orchestrates the end-to-end release pipeline using deterministic scripts checked into the repo under this skill's `scripts/` directory.

---

## How to Invoke

The minimum prompt to trigger this skill:

> Release version 0.1.1

The version can include or omit the `v` prefix — both are accepted:

> Release version v0.1.1

Modifier examples the agent will understand:

| Prompt | Behavior |
|--------|----------|
| `Release version 0.1.1` | Full pipeline — preflight, build, notes, docs, publish |
| `Release version 0.1.1 — dry run only` | Runs all steps with `DRY_RUN=1`; no git push, no GitHub release |
| `Release version 0.1.1 — create as draft` | GitHub release is created as a draft for review before publishing |
| `Release version 0.1.1 — skip Docker` | Suppresses the Docker reminder (still a manual step regardless) |
| `Release version 0.1.1 — build step only` | Runs preflight + build; stops before notes, docs, or publish |
| `Release version 0.1.1 — preflight already passed, start from build` | Skips preflight, runs build → notes → docs → publish |

The agent should always run `DRY_RUN=1` first, review the output, then run the full pipeline.

Before a real non-dry-run release, confirm the exact commit that will receive the release tag. Run `git status --short --branch`, `git log --oneline -3`, and compare HEAD with `origin/main`/`main`. Do not build or publish from a task worktree or unmerged task-only commit by accident. If HEAD includes release-task commits that are not intended for the release, either merge/rebase them to main first or reset/check out the intended main commit before continuing; ask the user only when the intended tag target is ambiguous.

---

## Required Inputs

| Input | Example | Notes |
|-------|---------|-------|
| Version | `0.1.1` or `v0.1.1` | Normalized to `X.Y.Z`; `v` prefix accepted |

---

## Required Environment / Tools

| Tool | Purpose | Install |
|------|---------|---------|
| `go` | Build server/desktop binaries | https://go.dev/dl |
| `git` | Tag, branch, commit range | pre-installed |
| `gh` (GitHub CLI) | Create GitHub release, upload assets | `brew install gh` or https://cli.github.com |
| `zip` | Package Windows and macOS archives | pre-installed macOS/Linux |
| `tar` | Package server tarballs | pre-installed |
| `sha256sum` / `shasum` | Generate SHA256SUMS | pre-installed (Linux/macOS) |
| `docker` *(optional)* | Build and push Docker image | https://docker.com |
| `x86_64-w64-mingw32-gcc` *(optional)* | Cross-compile Windows desktop-cli | `brew install mingw-w64` |

**Platform builds:** macOS desktop `.app` bundles require a macOS host. Linux desktop binaries require a native Linux build job or `OPENVIBELY_LINUX_DESKTOP_BINARY`. Windows desktop binaries require `mingw-w64` or `OPENVIBELY_WINDOWS_DESKTOP_BINARY`. Official release builds fail if any required desktop artifact is unavailable.

### Missing tools — install before proceeding

If preflight or a dry run reports a required release tool as missing, install it during the task before continuing. Do not stop at "tool missing" unless installation fails, requires unavailable privileges, or requires user-owned credentials/auth that cannot be completed non-interactively.

Common macOS installs:

```bash
brew install gh
brew install mingw-w64
```

After installing `gh`, run `gh auth status`; if unauthenticated, run `gh auth login` or ask the user to complete GitHub authentication. If the user has already requested the real release and later completes `gh` authentication or another credential step, treat the original release request as still active: verify auth and preflight, then continue the release pipeline. Do not stop with a status-only summary asking whether to proceed unless a new hard blocker remains or the user explicitly changed the request to dry-run/status mode. For platform-specific desktop builds, install the required compiler when practical or supply the corresponding signed/prebuilt artifact hook. Official releases cannot omit Windows or Linux desktop artifacts; the build stops if either cannot be produced.

---

## Preflight Checks

Run `release-preflight.sh <version>` first, or let `release.sh` run it automatically. Preflight validates:

1. **Semver format** — `X.Y.Z` or `vX.Y.Z` only; no pre-release suffixes.
2. **Required tools** — `go`, `git`, `zip`, `tar`, `sha256sum`/`shasum` present.
3. **GitHub CLI auth** — `gh auth status` passes; repo push permission confirmed.
4. **Worktree clean** — warns if uncommitted changes exist (does not block, but should be resolved before publishing).
5. **Tag target sanity** — confirm the current HEAD is the intended release commit, normally current `main`/`origin/main`, and not an accidental task-worktree follow-up commit.
6. **Tag collision** — fails if `v<version>` already exists locally or in GitHub releases.
7. **Previous tag** — finds latest `v*` tag for changelog range.
8. **Commit count** — lists unreleased commits for review.
9. **Build capability** — reports whether macOS, Windows, and Linux desktop artifacts can be built locally or must be supplied by their native release jobs, plus Docker availability.

---

## Release Flow

### Option A: Automated (recommended)

```bash
cd /path/to/openvibely
DRY_RUN=1 .openvibely/skills/openvibely_release_workflow/scripts/release.sh 0.1.1
# Review dry-run output, then:
.openvibely/skills/openvibely_release_workflow/scripts/release.sh 0.1.1
```

The orchestrator runs steps in sequence, with a mandatory AI synthesis + docs + review pause before publishing.

```text
Step 0: agent sanity check    — verify HEAD/tag target is intended main release commit
Step 1: release-preflight.sh  — validate environment, auth, tags, build caps
Step 2: release-build.sh      — build all artifacts → dist/0.1.1/ and verify archive contents
Step 3: release-notes.sh      — collect COMMITS.txt, render RELEASE_NOTES.md shell
Step 4: (agent)               — read COMMITS.txt, synthesize release-specific highlights and changelog, fill placeholders
Step 5: (agent)               — update docs for features added or meaningfully changed in this release
Step 6: (review)              — confirm RELEASE_NOTES.md, docs updates, and artifact sanity checks look correct
Step 7: release-publish.sh    — create GitHub release, upload artifacts
```

Step 4 is not automated — the orchestrating agent reads `COMMITS.txt` and writes both the `Highlights` and `What's Changed` sections using AI judgment (see Release Notes Generation below). Step 5 is also agent-owned — update the user docs before publishing so the release tag and public documentation include the features being announced.

### Option B: Step-by-step (for debugging or partial runs)

```bash
SCRIPTS=".openvibely/skills/openvibely_release_workflow/scripts"

# 0. Confirm the release tag target before doing real release work
git status --short --branch
git log --oneline -3

# 1. Preflight
bash $SCRIPTS/release-preflight.sh 0.1.1

# 2. Build artifacts. Official artifacts require the embedded Ed25519 trust
#    root. macOS builds require a Developer ID identity and notarytool profile.
#    Windows builds require external Authenticode sign and verification hooks;
#    the signing hook must timestamp, and verification must fail when absent.
#    Credentials are supplied by the release environment and are never generated.
export OPENVIBELY_RELEASE_KEY_ID=openvibely-release-1
export OPENVIBELY_RELEASE_PUBLIC_KEY='<base64-encoded-32-byte-Ed25519-public-key>'
export OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Example (TEAMID)'
export OPENVIBELY_MACOS_NOTARY_PROFILE='openvibely-notary'
export OPENVIBELY_WINDOWS_SIGN_COMMAND='/secure/path/to/authenticode-sign-and-timestamp'
export OPENVIBELY_WINDOWS_VERIFY_COMMAND='/secure/path/to/verify-authenticode-and-timestamp'
# On a non-Windows/non-Linux release coordinator, provide binaries built by
# the corresponding native release jobs. The build fails if either desktop
# artifact cannot be produced.
export OPENVIBELY_WINDOWS_DESKTOP_BINARY='/secure/path/to/openvibely-desktop.exe'
export OPENVIBELY_LINUX_DESKTOP_BINARY='/secure/path/to/openvibely-desktop'
bash $SCRIPTS/release-build.sh 0.1.1 ./dist/0.1.1

# 2b. Verify archive contents, especially macOS .app zips
#     The top-level extracted desktop bundle must end exactly in .app.
unzip -Z1 ./dist/0.1.1/OpenVibely_0.1.1_darwin_amd64.app.zip | head

# 3. Collect commits + render notes template
bash $SCRIPTS/release-notes.sh 0.1.1 v0.1.0 ./dist/0.1.1

# 4. (Agent step) Read commits, synthesize release-specific highlights and changelog, fill placeholders
#    Read:  ./dist/0.1.1/COMMITS.txt
#    Edit:  ./dist/0.1.1/RELEASE_NOTES.md  (replace AI_HIGHLIGHTS_PLACEHOLDER and AI_CHANGELOG_PLACEHOLDER blocks)

# 5. (Agent step) Update docs for new and changed user-facing features before publishing
#    Edit relevant in-repo docs/*.md files and the docs site repo if available.

# 6. Review the final RELEASE_NOTES.md and docs changes, then publish
bash $SCRIPTS/release-publish.sh 0.1.1 ./dist/0.1.1
```

### Dry-run mode

All scripts respect `DRY_RUN=1`. In dry-run mode, build commands, git operations, and GitHub API calls are printed but not executed. Run this first on a new release to confirm the expected behavior.

```bash
DRY_RUN=1 .openvibely/skills/openvibely_release_workflow/scripts/release.sh 0.1.1
```

When a user asks what the release notes would look like after a dry run, read the generated `COMMITS.txt` and `RELEASE_NOTES.md` from the dry-run/dist directory, synthesize both the `Highlights` and `What's Changed` sections in the response, and present the full filled-in notes preview without publishing or editing release state.

For a dry-run-only release rehearsal, still verify public state read-only: compare local tags, `git ls-remote --tags`, and `gh release view/list` output; confirm the new `v<version>` tag or release does not already exist; identify the previous public boundary before using it as the commit range. It is safe to generate temporary notes under `dist/<version>` for synthesis, but delete that dry-run output before finishing unless the user explicitly asked to keep files. End the report with the exact commands/actions needed for a real release, not just a status summary.

**Known dry-run caveat:** if `release-build.sh` prints `cp` or `chmod` errors while building macOS desktop bundles in `DRY_RUN=1`, treat them as a dry-run script leak unless the real build was requested. The `build_macos_app()` path may print the skipped binary build but still attempt bundle setup against a missing dry-run binary; report this as a script bug to fix before relying on clean dry-run output, but do not treat it as proof that an actual release build is blocked. If a dry-run rehearsal must exercise `release-build.sh` without allowing native macOS bundle filesystem writes, run the build script behind a temporary non-Darwin `uname` shim and record that macOS bundle behavior was intentionally bypassed because of the known leak.

### Post-release factual questions

When a user asks a factual follow-up about what a completed release included, keep the task read-only unless they explicitly ask for edits. Prefer the published tag or GitHub release as source of truth over the current working tree, because the checkout may contain unrelated uncommitted or post-release changes.

For model-provider counts, distinguish provider layers before answering:

- Backend provider type: count canonical `models.LLMProvider` additions such as `openai_compatible`.
- UI preset choices: count `openai_compatible_<slug>` options from the released tag/template or generated UI, not from memory.
- Named providers vs Custom: report `Custom OpenAI-Compatible` separately when present because it is a user-defined endpoint choice, not a named hosted/local provider preset.

Example read-only check against a release tag:

```bash
git show v<version>:web/templates/pages/models.templ \
  | sed -n '340,390p' \
  | grep -c '<option value="openai_compatible_'
```

### Published Release Note Updates

When a user asks to edit an already-published GitHub release body, treat it as public release-state mutation but keep the scope to the release notes unless they explicitly ask for artifacts, tags, Docker, or source changes. Confirm the target release exists, read the current body with `gh release view`, update only the release body, and avoid rebuilding or moving any tag.

Use the preferred style from `references/release-note-style.md` when rewriting release bullets: bold lead label, em dash, concise user-facing explanation. If the update mentions exact model-provider counts, verify the counts from the published tag first and distinguish backend provider types, UI preset choices, named provider presets, and local/self-hosted presets.

---

## Artifact Naming (matches v0.1.0 release pattern)

| Artifact | Description |
|----------|-------------|
| `OpenVibely_<version>_darwin_amd64.app.zip` | macOS desktop app bundle (Intel) |
| `OpenVibely_<version>_darwin_arm64.app.zip` | macOS desktop app bundle (Apple Silicon) |
| `openvibely_<version>_darwin_amd64_server.zip` | macOS server binary (Intel) |
| `openvibely_<version>_darwin_arm64_server.zip` | macOS server binary (Apple Silicon) |
| `openvibely_<version>_linux_amd64_server.tar.gz` | Linux server binary (x86_64) |
| `openvibely_<version>_linux_arm64_server.tar.gz` | Linux server binary (ARM64) |
| `openvibely_<version>_windows_amd64_server.zip` | Windows server binary zip |
| `openvibely_<version>_windows_amd64_desktop-cli.zip` | Signed Windows desktop binary zip |
| `openvibely_<version>_linux_amd64_desktop.tar.gz` | Linux desktop binary tarball |
| `SHA256SUMS` | SHA-256 checksums for all artifacts |

**Casing rule:** Desktop macOS bundles use `OpenVibely_` (capitalized). All server/binary artifacts use `openvibely_` (lowercase).

### macOS desktop app output

When asked where the desktop app is or what `make package-desktop-macos` creates, inspect `Makefile` and explain the concrete bundle output instead of only listing release zip names. The make target builds `bin/openvibely-desktop`, then creates `bin/OpenVibely.app` with this structure:

```text
bin/OpenVibely.app/
└── Contents/
    ├── MacOS/
    │   └── OpenVibely
    └── Info.plist
```

Treat `bin/openvibely-desktop` as a staging/intermediate Unix executable, not the user-facing desktop app and not a release asset by itself. It exists so the bundle can copy it into `Contents/MacOS/OpenVibely`; the useful macOS artifact is the `.app` bundle, packaged as `.app.zip` for GitHub releases.

The release script signs macOS bundles with hardened runtime and a secure timestamp, notarizes them, staples the notarization ticket, and verifies both the signature and Gatekeeper assessment before packaging.

### macOS app archive verification

Do not infer the contents of a desktop release zip from its filename or from the build script path alone. Before publishing, or whenever the user challenges what a desktop artifact contains, inspect the actual archive with `unzip -l`, `unzip -Z1`, or by extracting it to a temporary directory.

A valid macOS desktop release zip must extract to a top-level bundle directory whose name ends exactly in `.app` and contains an executable at `Contents/MacOS/OpenVibely`. A top-level directory like `OpenVibely.app_amd64/` or `OpenVibely.app_arm64/` is invalid because Finder will not recognize it as an application bundle; treat that as a release blocker and fix the packaging script before publishing.

This exact top-level directory-name requirement is macOS-specific. Linux and Windows server/CLI archives are flat binary artifacts; verify their binary names and executable bits where applicable, but do not apply `.app` bundle naming rules to non-macOS artifacts.

Example checks after a real build:

```bash
zip_path="dist/<version>/OpenVibely_<version>_darwin_amd64.app.zip"
unzip -Z1 "$zip_path" | head -20

tmpdir="$(mktemp -d)"
unzip -q "$zip_path" -d "$tmpdir"
find "$tmpdir" -maxdepth 2 -type d -name '*.app' -print
test -x "$tmpdir/OpenVibely.app/Contents/MacOS/OpenVibely"
rm -rf "$tmpdir"
```

Only state that the raw Unix executable is excluded from release assets after verifying the archive contents. The raw `bin/openvibely-desktop` / staging binary should not appear as a top-level shipped artifact inside the `.app.zip`.

---

## Release Notes Generation

`release-notes.sh` is deterministic: it collects raw commit data and renders the static template, but **does not synthesize release-specific highlights or the changelog**. Those steps require AI judgment and are explicitly delegated to the orchestrating agent.

Never reuse static product marketing bullets as release `Highlights`. The product intro can describe what OpenVibely is; `Highlights` must describe the 2–4 biggest user-facing changes that are new in this exact release compared to the previous tag.

### What the script does

1. Runs `git log <prev_tag>..HEAD` with full commit metadata (hash, date, author, subject, body) and writes it to `COMMITS.txt` in the dist dir.
2. Renders `RELEASE_NOTES.md` with static sections populated (Downloads table, Docker, Known Limitations) plus `AI_HIGHLIGHTS_PLACEHOLDER` in `Highlights` and `AI_CHANGELOG_PLACEHOLDER` in `What's Changed`.

### What the agent must do (AI synthesis step)

After `release-notes.sh` runs, the agent must:

1. **Read `dist/<version>/COMMITS.txt`** — full commit log since the previous release.
2. **Synthesize release-specific highlights** — 2–4 bullets, only for the most notable changes that are new in this release. Do not list evergreen features that existed in the prior release. Describe each highlight by what it **does** for the user — the functional capability or workflow it enables — not by what UI elements or files were added. Avoid using feature-specific examples in this skill; they rot as the product evolves. Prefer generic guidance such as: describe the user outcome, not the implementation detail or surface-level label.
3. **Synthesize a high-level, user-facing changelog** — not a dump of commit subjects. The agent should:
   - Identify what actually changed from a user's perspective.
   - Group related commits into coherent bullet points (3–8 total, plain English).
   - Leave out noise: internal refactors, memory updates, test changes, typo fixes, chore commits, CI/build/release-tooling changes, developer-only logging/observability flags, and anything else with no direct user impact.
   - Omit narrow bug fixes, reliability patches, and UI polish items unless they restore or materially improve a core user workflow that a release reader would choose to upgrade for.
   - Omit panel-detail changes, visual tweaks, and one-off light/dark-mode fixes from high-level notes; fold them into a broader feature entry only when they directly support a major release capability.
   - Apply the upgrade-decision test before adding a bullet: would a developer or operator deciding whether to upgrade care about this item at release-note level? If not, leave it out even if the commit is user-visible.
   - Combine multiple small commits about the same feature into one bullet.
   - If commit messages are terse or technical, infer intent from context (file paths, bodies, task references).
   - Inspect related diffs or touched paths for ambiguous commits when practical instead of guessing from subjects alone.
   - Sanity-check product and model names against the actual changed code/docs before publishing; do not rely on ambiguous commit subjects for exact marketing names or version numbers.
   - **Omit minor model version additions** (e.g. "Model X added to model selector") unless the model is a primary release feature. Adding a model to a dropdown is a maintenance change, not a release highlight. Omit it entirely or fold it into a generic "model updates" entry only if there are several such additions.
4. **Replace both placeholder blocks** in `RELEASE_NOTES.md`: `AI_HIGHLIGHTS_PLACEHOLDER` and `AI_CHANGELOG_PLACEHOLDER`.
5. Confirm the notes look correct before running `release-publish.sh`: no placeholder text, no duplicated commit-derived bullets, no old highlights from a previous release, no leaked secrets, and no claims about Docker/artifacts/features that the scripts did not actually build or verify.

### Why this approach

Commit messages in this repo are not always detailed or conventional. A regex-based grouper produces noisy, low-signal output. The agent reading the full commit context (including bodies and file diffs if needed) produces a far better high-level summary that's actually useful to users reading the GitHub release page.

---

## Release Notes Template

The generated notes follow this structure:

```text
# OpenVibely <version>

<product intro paragraph>

## Highlights
<AI_HIGHLIGHTS_PLACEHOLDER: 2–4 bullets for the biggest new changes in this release>

## What's Changed
<AI_CHANGELOG_PLACEHOLDER: grouped user-facing commit summary>
Full changelog: <link>

## Downloads
<SHA256 table from SHA256SUMS>

## Docker
<docker pull / run commands>

## Known Limitations
<static limitation bullets>
```

---

## Docs Update

After synthesizing the release notes, update documentation for features that are new or meaningfully changed in this release before publishing. Use `openvibely_docs_editing_workflow` when selected or available, and keep edits conservative.

### What to update

Update both documentation surfaces when they exist and are relevant:

1. **In-repo docs** — `docs/*.md`, `README.md`, and generated API docs when the release changes user-facing behavior, setup, environment variables, integrations, scheduling, skills, agents, task flows, or screenshots.
2. **Docs site repo** — the sibling `openvibely-doc` repository, usually at `/Users/dubee/go/src/github.com/openvibely/openvibely-doc`, for public docs pages that mirror or expand the in-repo guides.

### Decision rules

- New user-facing feature: find the closest existing guide and add usage/setup details instead of creating a new page by default.
- Changed behavior: search docs for stale descriptions and update them to match the release behavior.
- New or changed environment variables/config: update `docs/environment.md` and the corresponding docs-site page if present.
- New or changed channel integration behavior: update the relevant GitHub, Slack, Telegram, or webhook guide.
- Internal-only changes, tests, refactors, and tooling changes with no user-visible impact: do not add release docs just to mention them.
- Screenshots: update only when the documented UI materially changed and local screenshot generation is practical; otherwise note the screenshot gap in the release summary.

### Validation

Before publishing, verify the docs changes are included in the release branch/tag plan and run the repo's normal docs validation where practical. At minimum, search for stale terminology introduced by the release changes and ensure release notes do not announce a feature that has no corresponding docs when docs are expected.

---

## Known Limitations (documented in release notes)

These are preserved from v0.1.0 and should appear in every release's notes unless resolved:

1. **Linux desktop** — Built natively on Linux with GTK/WebKit dependencies or supplied through `OPENVIBELY_LINUX_DESKTOP_BINARY`; an official release fails if the artifact is unavailable.
2. **Windows desktop-cli** — Artifact is a signed executable zip, not an installer. Build it with `mingw-w64` or supply it through `OPENVIBELY_WINDOWS_DESKTOP_BINARY`; an official release fails if the artifact is unavailable.
3. **Docker / VPS storage** — Mount `/data` as a persistent volume; do not rely on the container filesystem for database or repos.

---

## Docker Image (manual step)

Automated Docker image publishing is not wired to CI yet. After the GitHub release is created, publish the OpenVibely server and coding-agent image manually:

```bash
docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile \
    -t openvibely/openvibely:<version> \
    -t openvibely/openvibely:latest \
    --push .
```

`release-publish.sh` prints these commands as a reminder after completing. If Docker Hub access is not set up, note this as a pending step in the GitHub release body.

---

## Rollback / Retry Behavior

| Failure point | Recovery action |
|---------------|----------------|
| Preflight fails | Fix the reported issue and re-run `release.sh` — no state was changed |
| Build fails mid-way | Fix the error; re-run `release-build.sh` (dist dir is recreated each run) |
| Notes generation fails | Re-run `release-notes.sh`; it overwrites `RELEASE_NOTES.md` |
| GitHub release creation fails | If tag was pushed, delete it: `git tag -d v<ver> && git push <remote> :refs/tags/v<ver>`; delete the remote release if partially created via `gh release delete v<ver>`; then retry |
| Partial asset upload | Use `gh release upload v<ver> <files>` to add missing assets to an existing release |

### Repairing an existing published release

If a release is already published and the user explicitly asks to fix or reuse the same version, treat it as an in-place release repair instead of creating a new version. Because this mutates public release state, first confirm the problem against the live release with `gh release view`, `git ls-remote --tags`, and by downloading or inspecting the affected assets. Do not assume a published asset is broken from the script or filename alone.

Use the smallest repair that fixes the public artifact:

1. Land the fix on the intended release branch or `main` commit using a normal push when possible.
2. Move the existing local tag to the fixed commit with `git tag -f v<ver> <commit>`.
3. Force-update only the release tag after explicit user authorization: `git push <remote> v<ver> --force`.
4. Rebuild affected artifacts from the fixed commit; rebuild all artifacts if `SHA256SUMS` must represent a fresh full dist directory.
5. Replace only affected GitHub release assets plus `SHA256SUMS`: delete old assets with `gh release delete-asset v<ver> <asset> --yes`, then upload replacements with `gh release upload v<ver> <files>`.
6. Verify the live release after upload by listing assets and downloading/inspecting at least one replaced asset from GitHub, not just the local `dist/` copy.

Never silently force-move a tag or delete release assets just because preflight reports a tag collision. Only do this for an already-published release repair after the user has asked to keep the same version or otherwise authorized mutation of the existing release.

### Release branch cleanup after publish

After a successful release, the `v<version>` tag is the canonical source reference for the shipped code. A temporary `release/v<version>` branch can usually be deleted, but inspect it before deletion instead of assuming it is redundant:

```bash
git fetch --prune <remote>
git log --oneline <remote>/release/v<version> ^<remote>/main
git show --stat <commit>
```

Preserve any durable source, docs, or skill commits that exist only on the release branch by cherry-picking or otherwise landing them on `main` first. Do not merge or cherry-pick committed `dist/<version>/` binary artifacts into `main`; release binaries belong in GitHub release assets, not source history. Once needed non-artifact commits are on `main` and the release tag points at the intended commit, delete the temporary branch with `git push <remote> --delete release/v<version>`.

---

## Expected Output

After a successful run:

```text
dist/<version>/
├── OpenVibely_<version>_darwin_amd64.app.zip
├── OpenVibely_<version>_darwin_arm64.app.zip
├── openvibely_<version>_darwin_amd64_server.zip
├── openvibely_<version>_darwin_arm64_server.zip
├── openvibely_<version>_linux_amd64_server.tar.gz
├── openvibely_<version>_linux_arm64_server.tar.gz
├── openvibely_<version>_windows_amd64_server.zip
├── openvibely_<version>_windows_amd64_desktop-cli.zip
├── openvibely_<version>_linux_amd64_desktop.tar.gz
├── SHA256SUMS
└── RELEASE_NOTES.md
```

GitHub release `v<version>` is created with title `OpenVibely <version>`, the generated notes, and all artifacts attached.

---

## Validation Tests

Run `release-validate.sh` to verify script-level logic without building or publishing:

```bash
bash .openvibely/skills/openvibely_release_workflow/scripts/release-validate.sh
```

Tests cover:
- Semver normalization (strip `v` prefix)
- Semver validation (valid and invalid inputs)
- Artifact naming conventions (all required server and desktop artifact patterns)
- Tag format (`v<version>`)
- Release title format (`OpenVibely <version>`)
- Release branch naming (`release/v<version>`)
- Release notes template structure (`AI_HIGHLIGHTS_PLACEHOLDER` and `AI_CHANGELOG_PLACEHOLDER` present, all required sections)
- `COMMITS.txt` output structure (hash, date, author, subject, body fields present)
- Real-build artifact contents for macOS `.app.zip` archives: extracted top-level bundle must end exactly in `.app` and contain `Contents/MacOS/OpenVibely`; archive listings like `OpenVibely.app_amd64/` are invalid even if the zip filename is correct.
- If deterministic changelog grouping is replaced with AI synthesis, remove obsolete regex bucket tests and keep tests proving the placeholders and `COMMITS.txt` structure exist.

---

## Reference Release: v0.1.0

- Tag: `v0.1.0` — commit `d654a06`
- GitHub release title: `OpenVibely 0.1.0`
- Assets published: darwin amd64/arm64 desktop `.app.zip`, darwin/linux amd64/arm64 server tarballs, windows amd64 server zip, windows amd64 desktop-cli zip, SHA256SUMS
- Docker image: `openvibely/openvibely:0.1.0`
