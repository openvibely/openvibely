#!/usr/bin/env bash
# release.sh — Full OpenVibely release orchestrator.
#
# Runs the complete release pipeline end-to-end:
#   1. release-preflight.sh  — validate environment
#   2. release-build.sh      — build all artifacts
#   3. release-notes.sh      — collect COMMITS.txt, render RELEASE_NOTES.md shell
#   4. (agent)               — read COMMITS.txt, synthesize changelog, fill placeholder
#   5. review prompt         — confirm RELEASE_NOTES.md before publishing
#   6. release-publish.sh    — create GitHub release and upload artifacts
#
# Usage:
#   ./release.sh <version>
#   ./release.sh 0.1.1
#   DRY_RUN=1 ./release.sh 0.1.1
#
# Environment variables (all passed through to sub-scripts):
#   DRY_RUN=1          Print commands without executing any destructive steps.
#   DRAFT=1            Create GitHub release as draft (review before publishing).
#   SKIP_GENERATE=1    Skip templ generate + swagger (if already generated).
#   SKIP_BRANCH=1      Skip release branch creation.
#   SKIP_GH_AUTH_CHECK=1  Skip GitHub auth check in preflight.
#   AUTO_CONFIRM=1     Skip the RELEASE_NOTES.md review pause (for CI).

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[release]${NC} $*"; }
warn() { echo -e "${YELLOW}[release]${NC} $*"; }
err()  { echo -e "${RED}[release]${NC} $*" >&2; }
info() { echo -e "${CYAN}[release]${NC} $*"; }
fail() { err "$*"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=release-version.sh
source "${SCRIPT_DIR}/release-version.sh"

###############################################################################
# 0. Arguments
###############################################################################

if [[ $# -lt 1 ]]; then
    fail "Usage: $0 <version>  (e.g. 0.1.1 or v0.1.1)"
fi

RAW_VERSION="$1"
VERSION="$(normalize_release_version "$RAW_VERSION")"

if ! is_valid_release_version "$VERSION"; then
    fail "Invalid semver: '$RAW_VERSION'. Expected X.Y.Z or vX.Y.Z."
fi

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || fail "Not in a git repository.")"
DIST_DIR="${DIST_DIR:-${REPO_ROOT}/dist/${VERSION}}"

export DRY_RUN="${DRY_RUN:-0}"
export DRAFT="${DRAFT:-0}"
export SKIP_GENERATE="${SKIP_GENERATE:-0}"
export SKIP_BRANCH="${SKIP_BRANCH:-0}"
export SKIP_GH_AUTH_CHECK="${SKIP_GH_AUTH_CHECK:-0}"
export DIST_DIR

log "OpenVibely release pipeline starting for v${VERSION}"
[[ "$DRY_RUN" == "1" ]] && warn "DRY_RUN=1 — no destructive operations will be performed."
[[ "$DRAFT" == "1" ]]   && warn "DRAFT=1 — GitHub release will be created as a draft."

###############################################################################
# 1. Preflight
###############################################################################

log "=== Step 1/6: Preflight checks ==="
bash "$SCRIPT_DIR/release-preflight.sh" "$VERSION"

# Read previous tag for notes generation
PREV_TAG="$(git tag --list 'v*' --sort=-version:refname | head -1 || true)"

###############################################################################
# 2. Build artifacts
###############################################################################

log "=== Step 2/6: Building release artifacts ==="
bash "$SCRIPT_DIR/release-build.sh" "$VERSION" "$DIST_DIR"

###############################################################################
# 3. Generate release notes
###############################################################################

log "=== Step 3/6: Generating release notes ==="
bash "$SCRIPT_DIR/release-notes.sh" "$VERSION" "${PREV_TAG:-}" "$DIST_DIR"

###############################################################################
# 4. AI synthesis step
###############################################################################

log "=== Step 4/6: Synthesize changelog (agent action required) ==="
NOTES_FILE="${DIST_DIR}/RELEASE_NOTES.md"
COMMITS_FILE="${DIST_DIR}/COMMITS.txt"

if [[ "${AUTO_CONFIRM:-0}" == "1" ]]; then
    warn "AUTO_CONFIRM=1 — skipping AI synthesis pause."
elif [[ "${DRY_RUN}" == "1" ]]; then
    warn "DRY_RUN=1 — skipping AI synthesis pause."
else
    echo ""
    info "================================================================"
    info "ACTION REQUIRED: Synthesize the release changelog"
    info "================================================================"
    info ""
    info "  1. Read:  $COMMITS_FILE"
    info "  2. Write a high-level, user-facing 'What's Changed' section"
    info "     (3–8 plain English bullets; omit noise/internals)"
    info "  3. Replace the AI_CHANGELOG_PLACEHOLDER block in:"
    info "     $NOTES_FILE"
    info ""
    warn "Do NOT press ENTER until the placeholder has been replaced."
    echo ""
    read -rp "Press ENTER once the changelog has been written: "
fi

###############################################################################
# 5. Review pause
###############################################################################

log "=== Step 5/6: Review RELEASE_NOTES.md ==="

if [[ "${AUTO_CONFIRM:-0}" == "1" ]]; then
    warn "AUTO_CONFIRM=1 — skipping review pause."
elif [[ "${DRY_RUN}" == "1" ]]; then
    warn "DRY_RUN=1 — skipping review pause."
else
    echo ""
    warn "Review the final RELEASE_NOTES.md before publishing:"
    info "  $NOTES_FILE"
    echo ""
    # Fail if the placeholder was not replaced
    if grep -q "AI_CHANGELOG_PLACEHOLDER" "$NOTES_FILE" 2>/dev/null; then
        echo ""
        err "AI_CHANGELOG_PLACEHOLDER is still present in RELEASE_NOTES.md."
        err "Replace it with synthesized changelog content before publishing."
        exit 1
    fi
    read -rp "Press ENTER to publish, or Ctrl+C to abort: "
fi

###############################################################################
# 6. Publish
###############################################################################

log "=== Step 6/6: Creating GitHub release ==="
bash "$SCRIPT_DIR/release-publish.sh" "$VERSION" "$DIST_DIR"

###############################################################################
# Done
###############################################################################

echo ""
info "=============================="
info "Release v${VERSION} complete!"
info "=============================="
info "Artifacts: $DIST_DIR"
info "Tag:       v${VERSION}"
echo ""
warn "REMINDER: Publish Docker image manually if applicable:"
info "  docker buildx build --platform linux/amd64,linux/arm64 \\"
info "      -t openvibely/openvibely:${VERSION} -t openvibely/openvibely:latest --push ."
