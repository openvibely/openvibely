#!/usr/bin/env bash
# release-publish.sh — Create the GitHub release and upload artifacts.
#
# Usage:
#   ./release-publish.sh <version> [dist_dir]
#   ./release-publish.sh 0.1.1
#   ./release-publish.sh 0.1.1 ./dist/0.1.1
#
# Prerequisites (fail if missing):
#   - GitHub CLI (gh) authenticated with push access
#   - dist_dir containing built artifacts and RELEASE_NOTES.md
#   - No pre-existing GitHub release for v<version>
#
# Steps performed:
#   1. Validate inputs and authentication
#   2. Create release branch release/v<version> from current HEAD
#   3. Tag v<version> on HEAD (or tip of release branch)
#   4. Push branch and tag to upstream/origin
#   5. Create GitHub release with gh, uploading all artifacts
#   6. Print Docker tagging/push instructions (manual step)
#
# Environment variables:
#   DRY_RUN=1          Print what would happen without pushing or creating the release.
#   REMOTE=<name>      Git remote name (default: upstream, falls back to origin).
#   SKIP_BRANCH=1      Skip release branch creation (e.g. if already on one).
#   DRAFT=1            Create the GitHub release as a draft for review before publishing.

set -euo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[publish]${NC} $*"; }
warn() { echo -e "${YELLOW}[publish]${NC} $*"; }
err()  { echo -e "${RED}[publish]${NC} $*" >&2; }
info() { echo -e "${CYAN}[publish]${NC} $*"; }
fail() { err "$*"; exit 1; }

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=release-version.sh
source "${SCRIPT_DIR}/release-version.sh"

run() {
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} $*"
    else
        "$@"
    fi
}

###############################################################################
# 1. Arguments and validation
###############################################################################

if [[ $# -lt 1 ]]; then
    fail "Usage: $0 <version> [dist_dir]"
fi

RAW_VERSION="$1"
VERSION="$(normalize_release_version "$RAW_VERSION")"
TAG="v${VERSION}"

if ! is_valid_release_version "$VERSION"; then
    fail "Invalid semver: '$RAW_VERSION'."
fi

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || fail "Not in a git repository.")"
DIST_DIR="${2:-${DIST_DIR:-${REPO_ROOT}/dist/${VERSION}}}"

log "Publishing OpenVibely ${TAG}"
log "Repo root:  $REPO_ROOT"
log "Dist dir:   $DIST_DIR"

# Verify dist dir exists and has expected content
[[ -d "$DIST_DIR" ]] || fail "Dist dir not found: $DIST_DIR. Run release-build.sh first."

NOTES_FILE="${DIST_DIR}/RELEASE_NOTES.md"
[[ -f "$NOTES_FILE" ]] || fail "RELEASE_NOTES.md not found in $DIST_DIR. Run release-notes.sh first."

SUMS_FILE="${DIST_DIR}/SHA256SUMS"
if [[ ! -f "$SUMS_FILE" ]]; then
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        warn "SHA256SUMS not present because this is a non-writing dry run; using the planned path."
    else
        fail "SHA256SUMS not found in $DIST_DIR. Run release-build.sh first."
    fi
fi

###############################################################################
# 2. GitHub CLI authentication
###############################################################################

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    if command -v gh &>/dev/null; then
        log "GitHub CLI present for dry-run release rehearsal."
    else
        warn "GitHub CLI not found; continuing non-publishing dry run."
    fi
else
    command -v gh &>/dev/null || fail "GitHub CLI (gh) not found. Install: https://cli.github.com"
    gh auth status &>/dev/null || fail "GitHub CLI not authenticated. Run: gh auth login"
    log "GitHub auth: OK"
fi

# Check tag does not exist remotely when authenticated. Local collision is checked below.
REMOTE_RELEASE=""
if command -v gh &>/dev/null && gh auth status &>/dev/null 2>&1; then
    REMOTE_RELEASE="$(gh api "repos/{owner}/{repo}/releases/tags/${TAG}" --jq '.tag_name' 2>/dev/null || true)"
fi
if [[ "$REMOTE_RELEASE" == "$TAG" ]]; then
    fail "GitHub release '$TAG' already exists. Delete it first or use a different version."
fi
log "GitHub tag '$TAG' not yet used."

###############################################################################
# 3. Determine git remote
###############################################################################

cd "$REPO_ROOT"

if git remote | grep -qx 'upstream'; then
    REMOTE="${REMOTE:-upstream}"
elif git remote | grep -qx 'origin'; then
    REMOTE="${REMOTE:-origin}"
else
    fail "No git remote named 'upstream' or 'origin' found. Cannot push."
fi

log "Using remote: $REMOTE"

###############################################################################
# 4. Create release branch
###############################################################################

RELEASE_BRANCH="release/${TAG}"

if [[ "${SKIP_BRANCH:-0}" != "1" ]]; then
    CURRENT_BRANCH="$(git branch --show-current)"
    log "Creating release branch: $RELEASE_BRANCH (from $CURRENT_BRANCH)"
    run git checkout -b "$RELEASE_BRANCH"
    run git push "$REMOTE" "$RELEASE_BRANCH"
    log "Release branch pushed: $RELEASE_BRANCH"
else
    log "Skipping branch creation (SKIP_BRANCH=1). Using current HEAD."
fi

###############################################################################
# 5. Tag
###############################################################################

if git rev-parse "$TAG" &>/dev/null; then
    fail "Local tag '$TAG' already exists. Delete with: git tag -d $TAG"
fi

log "Creating annotated tag: $TAG"
run git tag -a "$TAG" -m "OpenVibely ${VERSION}"
run git push "$REMOTE" "$TAG"
log "Tag '$TAG' pushed."

###############################################################################
# 6. Collect artifacts
###############################################################################

ARTIFACTS=()
while IFS= read -r file; do
    ARTIFACTS+=("$file")
done < <(find "$DIST_DIR" -maxdepth 1 \( -name "*.zip" -o -name "*.tar.gz" \) | sort)

if [[ ${#ARTIFACTS[@]} -eq 0 && "${DRY_RUN:-0}" == "1" ]]; then
    ARTIFACTS+=(
        "${DIST_DIR}/openvibely_${VERSION}_darwin_amd64_server.tar.gz"
        "${DIST_DIR}/openvibely_${VERSION}_darwin_arm64_server.tar.gz"
        "${DIST_DIR}/openvibely_${VERSION}_linux_amd64_server.tar.gz"
        "${DIST_DIR}/openvibely_${VERSION}_linux_arm64_server.tar.gz"
        "${DIST_DIR}/openvibely_${VERSION}_windows_amd64_server.zip"
    )
    if [[ "$(uname -s)" == "Darwin" ]]; then
        ARTIFACTS+=(
            "${DIST_DIR}/OpenVibely_${VERSION}_darwin_amd64.app.zip"
            "${DIST_DIR}/OpenVibely_${VERSION}_darwin_arm64.app.zip"
        )
    fi
    if command -v x86_64-w64-mingw32-gcc &>/dev/null; then
        ARTIFACTS+=("${DIST_DIR}/openvibely_${VERSION}_windows_amd64_desktop-cli.zip")
    fi
fi

ARTIFACTS+=("$SUMS_FILE")

if [[ ${#ARTIFACTS[@]} -eq 0 ]]; then
    fail "No artifacts found in $DIST_DIR. Run release-build.sh first."
fi

log "Artifacts to upload (${#ARTIFACTS[@]}):"
for f in "${ARTIFACTS[@]}"; do
    info "  $(basename "$f")"
done

###############################################################################
# 7. Create GitHub release
###############################################################################

RELEASE_TITLE="OpenVibely ${VERSION}"
DRAFT_FLAG=""
[[ "${DRAFT:-0}" == "1" ]] && DRAFT_FLAG="--draft"

log "Creating GitHub release: '$RELEASE_TITLE'..."

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo "[DRY-RUN] gh release create '$TAG' \\"
    echo "    --title '$RELEASE_TITLE' \\"
    echo "    --notes-file '$NOTES_FILE' \\"
    [[ -n "$DRAFT_FLAG" ]] && echo "    $DRAFT_FLAG \\"
    for f in "${ARTIFACTS[@]}"; do echo "    '$f' \\"; done
    echo ""
else
    gh release create "$TAG" \
        --title "$RELEASE_TITLE" \
        --notes-file "$NOTES_FILE" \
        ${DRAFT_FLAG} \
        "${ARTIFACTS[@]}"
    log "GitHub release created: $(gh release view "$TAG" --json url --jq '.url')"
fi

###############################################################################
# 8. Docker image — manual / report step
###############################################################################

echo ""
warn "=============================="
warn "DOCKER IMAGE — Manual Step"
warn "=============================="
warn "Docker image tagging and publishing requires a Docker Hub account with"
warn "write access to openvibely/openvibely."
warn ""
warn "To publish the Docker image, run on your Docker-authenticated host:"
warn ""
info "  docker buildx build --platform linux/amd64,linux/arm64 \\"
info "      -t openvibely/openvibely:${VERSION} \\"
info "      -t openvibely/openvibely:latest \\"
info "      --push ."
warn ""
warn "If Docker Hub publishing is not set up yet, add this step to the CI/CD"
warn "workflow or run it manually after the GitHub release is live."
warn "=============================="

###############################################################################
# 9. Summary
###############################################################################

echo ""
info "=============================="
info "Release published!"
info "=============================="
info "  Version:    $VERSION"
info "  Tag:        $TAG"
info "  Branch:     ${RELEASE_BRANCH:-current HEAD}"
info "  Notes:      $NOTES_FILE"
info ""
if [[ "${DRY_RUN:-0}" != "1" ]]; then
    gh release view "$TAG" --json url,assets --jq '"  URL: " + .url + "\n  Assets: " + (.assets | length | tostring)' || true
fi
info ""
info "Done. Review the release on GitHub and publish Docker image if applicable."
