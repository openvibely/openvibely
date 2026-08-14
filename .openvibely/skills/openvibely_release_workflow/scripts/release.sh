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

load_release_env_defaults() {
    local env_file="$1"
    local had_key=0 had_pub=0 had_mac_id=0 had_notary=0 had_win_sign=0 had_win_verify=0 had_win_p12=0 had_win_pass=0 had_win_desktop=0 had_linux_desktop=0
    local saved_key="" saved_pub="" saved_mac_id="" saved_notary="" saved_win_sign="" saved_win_verify="" saved_win_p12="" saved_win_pass="" saved_win_desktop="" saved_linux_desktop=""

    [[ ${OPENVIBELY_RELEASE_KEY_ID+x} ]] && { had_key=1; saved_key="$OPENVIBELY_RELEASE_KEY_ID"; }
    [[ ${OPENVIBELY_RELEASE_PUBLIC_KEY+x} ]] && { had_pub=1; saved_pub="$OPENVIBELY_RELEASE_PUBLIC_KEY"; }
    [[ ${OPENVIBELY_MACOS_SIGN_IDENTITY+x} ]] && { had_mac_id=1; saved_mac_id="$OPENVIBELY_MACOS_SIGN_IDENTITY"; }
    [[ ${OPENVIBELY_MACOS_NOTARY_PROFILE+x} ]] && { had_notary=1; saved_notary="$OPENVIBELY_MACOS_NOTARY_PROFILE"; }
    [[ ${OPENVIBELY_WINDOWS_SIGN_COMMAND+x} ]] && { had_win_sign=1; saved_win_sign="$OPENVIBELY_WINDOWS_SIGN_COMMAND"; }
    [[ ${OPENVIBELY_WINDOWS_VERIFY_COMMAND+x} ]] && { had_win_verify=1; saved_win_verify="$OPENVIBELY_WINDOWS_VERIFY_COMMAND"; }
    [[ ${WINDOWS_CERT_P12+x} ]] && { had_win_p12=1; saved_win_p12="$WINDOWS_CERT_P12"; }
    [[ ${WINDOWS_CERT_PASSWORD+x} ]] && { had_win_pass=1; saved_win_pass="$WINDOWS_CERT_PASSWORD"; }
    [[ ${OPENVIBELY_WINDOWS_DESKTOP_BINARY+x} ]] && { had_win_desktop=1; saved_win_desktop="$OPENVIBELY_WINDOWS_DESKTOP_BINARY"; }
    [[ ${OPENVIBELY_LINUX_DESKTOP_BINARY+x} ]] && { had_linux_desktop=1; saved_linux_desktop="$OPENVIBELY_LINUX_DESKTOP_BINARY"; }

    # shellcheck source=/dev/null
    source "$env_file"

    [[ "$had_key" == "1" ]] && OPENVIBELY_RELEASE_KEY_ID="$saved_key"
    [[ "$had_pub" == "1" ]] && OPENVIBELY_RELEASE_PUBLIC_KEY="$saved_pub"
    [[ "$had_mac_id" == "1" ]] && OPENVIBELY_MACOS_SIGN_IDENTITY="$saved_mac_id"
    [[ "$had_notary" == "1" ]] && OPENVIBELY_MACOS_NOTARY_PROFILE="$saved_notary"
    [[ "$had_win_sign" == "1" ]] && OPENVIBELY_WINDOWS_SIGN_COMMAND="$saved_win_sign"
    [[ "$had_win_verify" == "1" ]] && OPENVIBELY_WINDOWS_VERIFY_COMMAND="$saved_win_verify"
    [[ "$had_win_p12" == "1" ]] && WINDOWS_CERT_P12="$saved_win_p12"
    [[ "$had_win_pass" == "1" ]] && WINDOWS_CERT_PASSWORD="$saved_win_pass"
    [[ "$had_win_desktop" == "1" ]] && OPENVIBELY_WINDOWS_DESKTOP_BINARY="$saved_win_desktop"
    [[ "$had_linux_desktop" == "1" ]] && OPENVIBELY_LINUX_DESKTOP_BINARY="$saved_linux_desktop"
    return 0
}

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
SIGNING_CHECK="${SCRIPT_DIR}/check-release-signing.sh"

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
# 0b. Signing setup
###############################################################################

if [[ "${SKIP_SIGNING_CHECK:-0}" == "1" ]]; then
    warn "SKIP_SIGNING_CHECK=1 — skipping signing readiness checks."
elif [[ -x "$SIGNING_CHECK" ]]; then
    log "=== Step 0/6: Signing setup/readiness ==="
    if [[ "$DRY_RUN" == "1" ]]; then
        "$SIGNING_CHECK" --no-interactive
    else
        "$SIGNING_CHECK" --setup
    fi
    if [[ "${SKIP_RELEASE_SIGNING_ENV:-0}" != "1" && -f "${REPO_ROOT}/.release-signing.env" ]]; then
        load_release_env_defaults "${REPO_ROOT}/.release-signing.env"
        export -n WINDOWS_CERT_PASSWORD 2>/dev/null || true
    fi
else
    warn "Signing readiness script not found: $SIGNING_CHECK"
fi
export -n WINDOWS_CERT_PASSWORD 2>/dev/null || true

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
warn "REMINDER: Publish the OpenVibely Docker image manually if applicable:"
info "  docker buildx build --platform linux/amd64,linux/arm64 -f Dockerfile \\"
info "      --build-arg VERSION=${VERSION} --build-arg COMMIT=$(git rev-parse HEAD) \\"
info "      --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ) \\"
info "      --build-arg RELEASE_TRUST_ID=\${OPENVIBELY_RELEASE_KEY_ID:?set-release-key-id} \\"
info "      --build-arg RELEASE_TRUST_VALUE=\${OPENVIBELY_RELEASE_PUBLIC_KEY:?set-release-public-key} \\"
info "      -t openvibely/openvibely:${VERSION} -t openvibely/openvibely:latest --push ."
