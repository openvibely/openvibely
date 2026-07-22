#!/usr/bin/env bash
# release-preflight.sh — Validate environment before starting an OpenVibely release.
#
# Usage:
#   ./release-preflight.sh <version>
#   ./release-preflight.sh v0.1.1
#   ./release-preflight.sh 0.1.1
#
# Outputs normalised version, previous tag, and list of unreleased commits.
# Exits non-zero on any preflight failure.
#
# Environment variables:
#   DRY_RUN=1             Print what would happen without running destructive steps.
#   SKIP_GH_AUTH_CHECK=1  Skip GitHub CLI auth check (for air-gapped environments).

set -euo pipefail

###############################################################################
# Helpers
###############################################################################

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[preflight]${NC} $*"; }
warn() { echo -e "${YELLOW}[preflight]${NC} $*"; }
err()  { echo -e "${RED}[preflight]${NC} $*" >&2; }
info() { echo -e "${CYAN}[preflight]${NC} $*"; }
fail() { err "$*"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=release-version.sh
source "${SCRIPT_DIR}/release-version.sh"

###############################################################################
# 1. Input: parse and normalize semver
###############################################################################

if [[ $# -lt 1 ]]; then
    fail "Usage: $0 <version>  (e.g. 0.1.1 or v0.1.1)"
fi

RAW_VERSION="$1"
VERSION="$(normalize_release_version "$RAW_VERSION")"

# Validate semver: MAJOR.MINOR.PATCH (no pre-release/build suffixes for release)
if ! is_valid_release_version "$VERSION"; then
    fail "Invalid semver release version: '$RAW_VERSION'. Expected X.Y.Z or vX.Y.Z."
fi

TAG="v${VERSION}"
log "Normalized version: ${VERSION}  Tag: ${TAG}"

###############################################################################
# 2. Required tools
###############################################################################

MISSING_TOOLS=()
for tool in go git zip tar; do
    command -v "$tool" &>/dev/null || MISSING_TOOLS+=("$tool")
done

# sha256sum (Linux) or shasum -a 256 (macOS)
if ! command -v sha256sum &>/dev/null && ! command -v shasum &>/dev/null; then
    MISSING_TOOLS+=("sha256sum or shasum")
fi

if [[ ${#MISSING_TOOLS[@]} -gt 0 ]]; then
    fail "Missing required tools: ${MISSING_TOOLS[*]}"
fi

log "Required tools present: go, git, zip, tar, sha(sum)"

# GitHub CLI (needed for release creation — warn, not fail, for preflight)
if ! command -v gh &>/dev/null; then
    warn "GitHub CLI (gh) not found — release publishing will fail. Install: https://cli.github.com"
else
    log "GitHub CLI present: $(gh --version | head -1)"
fi

###############################################################################
# 3. GitHub auth check
###############################################################################

if [[ "${SKIP_GH_AUTH_CHECK:-0}" != "1" ]] && command -v gh &>/dev/null; then
    if ! gh auth status &>/dev/null; then
        fail "GitHub CLI not authenticated. Run: gh auth login"
    fi
    log "GitHub auth: OK ($(gh auth status 2>&1 | grep 'Logged in' || echo 'authenticated'))"

    # Check write permission on the repo
    REPO_PERMISSION="$(gh api repos/{owner}/{repo} --jq '.permissions.push' 2>/dev/null || echo 'unknown')"
    if [[ "$REPO_PERMISSION" == "false" ]]; then
        fail "GitHub account lacks push permission on this repo. Cannot create a release."
    fi
    log "GitHub repo write permission: ${REPO_PERMISSION}"
fi

###############################################################################
# 4. Git worktree state
###############################################################################

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || fail "Not in a git repository.")"
log "Repo root: $REPO_ROOT"

CURRENT_BRANCH="$(git branch --show-current)"
log "Current branch: $CURRENT_BRANCH"

# Check for dirty worktree (uncommitted changes)
GIT_STATUS="$(git status --porcelain)"
if [[ -n "$GIT_STATUS" ]]; then
    warn "Worktree has uncommitted changes:"
    git status --short
    warn "Commits and tags will be made from the current dirty state unless you commit first."
    warn "Set DRY_RUN=1 to inspect without making changes."
fi

###############################################################################
# 5. Tag collision check
###############################################################################

if git rev-parse "$TAG" &>/dev/null; then
    fail "Tag '$TAG' already exists locally. Delete it first or choose a different version."
fi

if command -v gh &>/dev/null && gh auth status &>/dev/null 2>&1; then
    REMOTE_TAG="$(gh api "repos/{owner}/{repo}/releases/tags/${TAG}" --jq '.tag_name' 2>/dev/null || true)"
    if [[ "$REMOTE_TAG" == "$TAG" ]]; then
        fail "GitHub release '$TAG' already exists. Choose a different version or delete it first."
    fi
fi

log "Tag '$TAG' not yet used — safe to proceed."

###############################################################################
# 6. Determine previous release tag
###############################################################################

PREV_TAG="$(git tag --list 'v*' --sort=-version:refname | head -1 || true)"
if [[ -z "$PREV_TAG" ]]; then
    warn "No previous release tags found. Changelog will cover all commits."
    PREV_TAG=""
else
    log "Previous release tag: $PREV_TAG"
fi

###############################################################################
# 7. List unreleased commits
###############################################################################

if [[ -n "$PREV_TAG" ]]; then
    COMMIT_RANGE="${PREV_TAG}..HEAD"
else
    COMMIT_RANGE="HEAD"
fi

COMMIT_COUNT="$(git log "$COMMIT_RANGE" --oneline | wc -l | tr -d ' ')"
log "Commits since ${PREV_TAG:-beginning}: ${COMMIT_COUNT}"

if [[ "$COMMIT_COUNT" -eq 0 ]]; then
    warn "No commits found since $PREV_TAG. This may be a re-release or tag already points to HEAD."
else
    info "--- Unreleased commits ---"
    git log "$COMMIT_RANGE" --oneline --no-decorate
    info "--- End of commits ---"
fi

###############################################################################
# 8. macOS desktop build capability
###############################################################################

HOST_OS="$(uname -s)"
HOST_ARCH="$(uname -m)"

if [[ "$HOST_OS" == "Darwin" ]]; then
    log "Build host: macOS ($HOST_ARCH) — macOS desktop app bundles supported."
else
    warn "Build host: $HOST_OS — macOS desktop .app bundles CANNOT be built. Linux/Windows server artifacts only."
fi

# Windows CGO cross-compiler check
if command -v x86_64-w64-mingw32-gcc &>/dev/null; then
    log "mingw-w64 cross-compiler present — Windows desktop-cli build supported."
else
    warn "mingw-w64 not found (x86_64-w64-mingw32-gcc). Windows desktop-cli build will be skipped."
    warn "Install on macOS with: brew install mingw-w64"
fi

# Docker check
if command -v docker &>/dev/null; then
    log "Docker present: $(docker --version)"
else
    warn "Docker not found — Docker image publishing step will be skipped."
fi

###############################################################################
# 9. Summary
###############################################################################

echo ""
info "=============================="
info "Preflight PASSED"
info "=============================="
info "  Version:       $VERSION"
info "  Tag:           $TAG"
info "  Prev tag:      ${PREV_TAG:-(none)}"
info "  Commits:       $COMMIT_COUNT"
info "  Host:          $HOST_OS / $HOST_ARCH"
info "  Branch:        $CURRENT_BRANCH"
echo ""
info "Proceed with:"
info "  ./release-build.sh $VERSION [dist_dir]"
info "  ./release-notes.sh $VERSION ${PREV_TAG:-(none)} [dist_dir]"
info "  ./release-publish.sh $VERSION [dist_dir]"

# Export for callers that source this script
export RELEASE_VERSION="$VERSION"
export RELEASE_TAG="$TAG"
export RELEASE_PREV_TAG="$PREV_TAG"
export RELEASE_COMMIT_COUNT="$COMMIT_COUNT"
