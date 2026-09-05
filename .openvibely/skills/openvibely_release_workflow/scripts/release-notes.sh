#!/usr/bin/env bash
# release-notes.sh — Collect commits and render the RELEASE_NOTES.md shell for an
# OpenVibely release. The "Highlights" and "What's Changed" sections are
# intentionally left as placeholders — the orchestrating agent is responsible for
# reading COMMITS.txt and synthesizing release-specific notes using AI judgment.
#
# Usage:
#   ./release-notes.sh <version> <prev_tag> [dist_dir]
#   ./release-notes.sh 0.1.1 v0.1.0 ./dist/0.1.1
#   ./release-notes.sh 0.1.1 "" ./dist/0.1.1   # first release (no prev tag)
#
# Outputs (written to dist_dir):
#   COMMITS.txt         — raw git log for AI synthesis input
#   RELEASE_NOTES.md    — template with placeholders; agent must fill highlights + changelog
#
# Environment variables:
#   REPO_URL=<url>      Override GitHub repo URL (default: detected from git remote).

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[notes]${NC} $*"; }
warn() { echo -e "${YELLOW}[notes]${NC} $*"; }
err()  { echo -e "${RED}[notes]${NC} $*" >&2; }
info() { echo -e "${CYAN}[notes]${NC} $*"; }
fail() { err "$*"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=release-version.sh
source "${SCRIPT_DIR}/release-version.sh"

###############################################################################
# 1. Arguments
###############################################################################

if [[ $# -lt 2 ]]; then
    fail "Usage: $0 <version> <prev_tag> [dist_dir]"
fi

RAW_VERSION="$1"
VERSION="$(normalize_release_version "$RAW_VERSION")"
PREV_TAG="${2:-}"   # empty string = first release
DIST_DIR="${3:-./dist/${VERSION}}"

if ! is_valid_release_version "$VERSION"; then
    fail "Invalid semver: '$RAW_VERSION'."
fi

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || fail "Not in a git repository.")"
mkdir -p "$DIST_DIR"

###############################################################################
# 2. Detect repo URL
###############################################################################

REPO_URL="${REPO_URL:-}"
if [[ -z "$REPO_URL" ]]; then
    REMOTE_URL="$(git remote get-url upstream 2>/dev/null || git remote get-url origin 2>/dev/null || true)"
    if [[ "$REMOTE_URL" =~ github\.com[:/]([^/]+/[^/.]+) ]]; then
        REPO_SLUG="${BASH_REMATCH[1]}"
        REPO_URL="https://github.com/${REPO_SLUG}"
    else
        REPO_URL="https://github.com/openvibely/openvibely"
        warn "Could not detect repo URL from remote — using default: $REPO_URL"
    fi
fi

log "Repo URL: $REPO_URL"
log "Collecting commits for v${VERSION} (since ${PREV_TAG:-beginning})..."

###############################################################################
# 3. Collect raw commits → COMMITS.txt
#
# The agent will read this file and synthesize the changelog. We collect:
#   - commit hash + date + author
#   - subject line
#   - full body (may contain task details, PR refs, co-authors, etc.)
#
# This gives the agent maximum context to determine what was actually shipped
# and whether a commit is user-facing, internal, or noise.
###############################################################################

if [[ -n "$PREV_TAG" ]]; then
    COMMIT_RANGE="${PREV_TAG}..HEAD"
else
    COMMIT_RANGE="HEAD"
fi

COMMITS_FILE="${DIST_DIR}/COMMITS.txt"

{
    echo "# Commits for OpenVibely v${VERSION}"
    echo "# Range: ${COMMIT_RANGE}"
    echo "# Generated: $(date -u '+%Y-%m-%dT%H:%M:%SZ')"
    echo ""
    git log "$COMMIT_RANGE" \
        --format="----%ncommit %H%nDate:    %ad%nAuthor:  %an <%ae>%nSubject: %s%n%nBody:%n%b" \
        --date=short \
        --reverse \
        2>/dev/null || true
} > "$COMMITS_FILE"

COMMIT_COUNT="$(git log "$COMMIT_RANGE" --oneline 2>/dev/null | wc -l | tr -d ' ' || echo 0)"
log "Commits collected: $COMMIT_COUNT → $COMMITS_FILE"

###############################################################################
# 4. Build Downloads table from SHA256SUMS (if present)
###############################################################################

DOWNLOADS_TABLE=""
SUMS_FILE="${DIST_DIR}/SHA256SUMS"

if [[ -f "$SUMS_FILE" ]]; then
    DOWNLOADS_TABLE=$'| Artifact | SHA-256 |\n|----------|---------|'
    while IFS= read -r line; do
        CHECKSUM="$(echo "$line" | awk '{print $1}')"
        FILENAME="$(echo "$line" | awk '{print $2}' | sed 's|^\./||')"
        DOWNLOADS_TABLE+=$'\n'"| \`${FILENAME}\` | \`${CHECKSUM}\` |"
    done < "$SUMS_FILE"
else
    warn "SHA256SUMS not found in $DIST_DIR — Downloads table will be placeholder."
    DOWNLOADS_TABLE="_Run release-build.sh first to populate checksums._"
fi

###############################################################################
# 5. Render RELEASE_NOTES.md with AI placeholders
#
# The orchestrating agent MUST:
#   1. Read COMMITS.txt
#   2. Synthesize release-specific highlights and a high-level, user-facing changelog
#   3. Replace both AI placeholder blocks in RELEASE_NOTES.md before publishing
###############################################################################

NOTES_FILE="${DIST_DIR}/RELEASE_NOTES.md"

# For the full changelog link, use the prev tag if available; otherwise use
# the first commit hash so the GitHub compare link resolves correctly.
if [[ -n "$PREV_TAG" ]]; then
    PREV_LABEL="$PREV_TAG"
else
    FIRST_COMMIT="$(git rev-list --max-parents=0 HEAD 2>/dev/null | head -1 || true)"
    PREV_LABEL="${FIRST_COMMIT:-HEAD~1}"
fi

cat > "$NOTES_FILE" << MARKDOWN
# OpenVibely ${VERSION}

OpenVibely is an open-source, self-hosted platform for AI-powered task scheduling and agent-based automation. Run your own agents that plan, execute, and iterate — from a web UI or the macOS desktop app — with full control over your data.

## Highlights

<!-- AI_HIGHLIGHTS_PLACEHOLDER
     The agent orchestrating this release must replace this block with 2–4 concise
     bullets for the biggest user-facing changes that are new in this release.
     Do not reuse static product feature bullets from prior releases.

     Instructions for the agent:
       1. Read dist/${VERSION}/COMMITS.txt — it contains every commit since ${PREV_LABEL}.
       2. Identify the most notable changes that are genuinely new in this release.
       3. Write concise bullets that complement the detailed What's Changed section.
       4. Delete this comment block when done.
-->

## What's Changed

<!-- AI_CHANGELOG_PLACEHOLDER
     The agent orchestrating this release must replace this block with a synthesized,
     high-level summary of user-facing changes. See COMMITS.txt for raw input.

     Instructions for the agent:
       1. Read dist/${VERSION}/COMMITS.txt — it contains every commit since ${PREV_LABEL}.
       2. Identify commits that represent meaningful, user-facing changes:
          features, fixes that affect behavior, notable improvements.
       3. Ignore pure noise: typo fixes, memory updates, internal chore commits,
          test-only changes, minor refactors with no user impact.
       4. Write 3–8 concise bullet points in plain English. Group related changes.
          Do NOT just echo commit subjects — synthesize what actually changed for users.
       5. If there are no meaningful user-facing changes, write a brief honest note.
       6. Delete this comment block when done.
-->

**Full changelog:** [${PREV_LABEL}...v${VERSION}](${REPO_URL}/compare/${PREV_LABEL}...v${VERSION})

## Downloads

${DOWNLOADS_TABLE}

See **Assets** below for the complete file list and [SHA256SUMS](${REPO_URL}/releases/download/v${VERSION}/SHA256SUMS).

## Docker

\`\`\`bash
docker pull openvibely/openvibely:${VERSION}
docker run -d -p 3001:3001 -v openvibely_data:/data openvibely/openvibely:${VERSION}
\`\`\`

Persistent data (database, repos, uploads) is stored in the \`/data\` volume. The image runs as UID/GID \`10001:10001\`, so mounted storage must be writable by that user.

## Known Limitations

- **Linux desktop**: Pick the amd64 or arm64 tarball for your machine. Extract and run \`openvibely-desktop\` directly, or run \`./openvibely-desktop --install-desktop\` to install the executable, application-menu entry, and icons for your user.
- **Windows desktop**: The Windows desktop artifacts are executable zips, not installers. Pick the amd64 or arm64 zip for your machine, then extract and run \`openvibely-desktop.exe\` directly.
- **Docker / VPS storage**: Mount a persistent volume at \`/data\` so your database, repos, and uploads survive container restarts. The Docker image uses \`/data\` as the data root.

---
_OpenVibely v${VERSION} — [Source](${REPO_URL}) · [Docs](https://docs.openvibely.ai)_
MARKDOWN

log "Release notes shell written to: $NOTES_FILE"
echo ""
info "============================================================"
info "Commit data and release notes template ready"
info "============================================================"
info ""
info "  Commits file:  $COMMITS_FILE  ($COMMIT_COUNT commits)"
info "  Notes file:    $NOTES_FILE"
info ""
info "NEXT STEP (agent action required):"
info "  1. Read $COMMITS_FILE"
info "  2. Synthesize release-specific highlights and a high-level, user-facing 'What's Changed' section"
info "  3. Replace the AI_HIGHLIGHTS_PLACEHOLDER and AI_CHANGELOG_PLACEHOLDER blocks in $NOTES_FILE"
info "  4. Then run: release-publish.sh $VERSION $DIST_DIR"
