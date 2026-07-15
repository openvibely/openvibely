#!/usr/bin/env bash
# release-build.sh — Build all OpenVibely release artifacts for a given version.
#
# Usage:
#   ./release-build.sh <version> [dist_dir]
#   ./release-build.sh 0.1.1
#   ./release-build.sh 0.1.1 /tmp/openvibely-dist
#
# Produces in dist_dir (default: ./dist/<version>):
#   OpenVibely_<version>_darwin_amd64.app.zip
#   OpenVibely_<version>_darwin_arm64.app.zip
#   openvibely_<version>_darwin_amd64_server.tar.gz
#   openvibely_<version>_darwin_arm64_server.tar.gz
#   openvibely_<version>_linux_amd64_server.tar.gz
#   openvibely_<version>_linux_arm64_server.tar.gz
#   openvibely_<version>_windows_amd64_server.zip
#   openvibely_<version>_windows_amd64_desktop-cli.zip  (requires mingw-w64)
#   SHA256SUMS
#
# Environment variables:
#   DRY_RUN=1          Print commands without executing build steps.
#   SKIP_GENERATE=1    Skip templ generate + swagger (if already done).
#   DIST_DIR=<path>    Override distribution output directory.
#
# Known limitations (matches v0.1.0):
#   - Linux desktop (GTK/WebKit) artifacts are not built from macOS; install
#     Linux desktop deps and build natively on Linux if needed.
#   - Windows desktop-cli requires mingw-w64 (brew install mingw-w64 on macOS).
#   - Docker image publishing is a separate step (release-publish.sh).

set -euo pipefail

###############################################################################
# Helpers
###############################################################################

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; CYAN='\033[0;36m'; NC='\033[0m'
log()  { echo -e "${GREEN}[build]${NC} $*"; }
warn() { echo -e "${YELLOW}[build]${NC} $*"; }
err()  { echo -e "${RED}[build]${NC} $*" >&2; }
info() { echo -e "${CYAN}[build]${NC} $*"; }
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
# 1. Arguments
###############################################################################

if [[ $# -lt 1 ]]; then
    fail "Usage: $0 <version> [dist_dir]  (e.g. 0.1.1)"
fi

RAW_VERSION="$1"
VERSION="$(normalize_release_version "$RAW_VERSION")"

if ! is_valid_release_version "$VERSION"; then
    fail "Invalid semver: '$RAW_VERSION'. Expected X.Y.Z or vX.Y.Z."
fi

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || fail "Not in a git repository.")"
DIST_DIR="${2:-${DIST_DIR:-${REPO_ROOT}/dist/${VERSION}}}"

log "Building OpenVibely v${VERSION}"
log "Repo root:  $REPO_ROOT"
log "Output dir: $DIST_DIR"

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo -e "${YELLOW}[DRY-RUN]${NC} Would create output directory: $DIST_DIR"
else
    mkdir -p "$DIST_DIR"
fi
cd "$REPO_ROOT"

###############################################################################
# 2. Code generation (templ + swagger)
###############################################################################

if [[ "${SKIP_GENERATE:-0}" != "1" ]]; then
    log "Running templ generate..."
    TEMPL_VERSION="$(go list -m -f '{{.Version}}' github.com/a-h/templ 2>/dev/null)"
    run go run "github.com/a-h/templ/cmd/templ@${TEMPL_VERSION}" generate

    log "Running swag init..."
    SWAG_VERSION="$(go list -m -f '{{.Version}}' github.com/swaggo/swag 2>/dev/null)"
    run go run "github.com/swaggo/swag/cmd/swag@${SWAG_VERSION}" init \
        -g cmd/server/main.go -o docs
    if [[ "${DRY_RUN:-0}" != "1" ]]; then
        sed -i.bak '/LeftDelim:/d' docs/docs.go && \
        sed -i.bak '/RightDelim:/d' docs/docs.go && \
        rm -f docs/docs.go.bak
    fi
else
    log "Skipping code generation (SKIP_GENERATE=1)."
fi

###############################################################################
# 3. Helper: build binary
###############################################################################

LDFLAGS="-s -w -X main.Version=${VERSION}"

build_binary() {
    local output="$1" pkg="$2" goos="$3" goarch="$4"
    local cgo="${5:-0}"
    local cc="${6:-}"

    log "Building $goos/$goarch → $output (CGO_ENABLED=${cgo})"
    local cmd=(env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="$cgo")
    [[ -n "$cc" ]] && cmd+=(CC="$cc")
    cmd+=(go build -ldflags="$LDFLAGS" -o "$output" "$pkg")
    run "${cmd[@]}"
}

###############################################################################
# 4. Server binaries (CGO_ENABLED=0 — cross-compile for all platforms)
###############################################################################

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    TMP_BIN="${TMPDIR:-/tmp}/openvibely-release-dry-run"
else
    TMP_BIN="$(mktemp -d)"
    trap 'rm -rf "$TMP_BIN"' EXIT
fi

log "Building server binaries (all platforms)..."

# darwin/amd64 server
build_binary "$TMP_BIN/server_darwin_amd64" ./cmd/server darwin amd64

# darwin/arm64 server
build_binary "$TMP_BIN/server_darwin_arm64" ./cmd/server darwin arm64

# linux/amd64 server
build_binary "$TMP_BIN/server_linux_amd64" ./cmd/server linux amd64

# linux/arm64 server
build_binary "$TMP_BIN/server_linux_arm64" ./cmd/server linux arm64

# windows/amd64 server
build_binary "$TMP_BIN/server_windows_amd64.exe" ./cmd/server windows amd64

###############################################################################
# 5. macOS desktop app bundles
###############################################################################

HOST_OS="$(uname -s)"
HOST_ARCH="$(uname -m)"

build_macos_app() {
    local goarch="$1"
    local bin_name="openvibely-desktop-${goarch}"
    local app_name="OpenVibely.app"
    local staging_dir="${TMP_BIN}/staging_${goarch}"
    local app_dir="${staging_dir}/${app_name}"
    local bundle="${app_dir}/Contents"

    log "Building macOS desktop app ($goarch)..."
    build_binary "$TMP_BIN/${bin_name}" ./cmd/desktop darwin "$goarch" 1

    local zip_name="OpenVibely_${VERSION}_darwin_${goarch}.app.zip"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would assemble ${app_name}/Contents/MacOS/OpenVibely"
        echo -e "${YELLOW}[DRY-RUN]${NC} Would package ${DIST_DIR}/${zip_name} with ${app_name} as the archive root"
        return
    fi

    mkdir -p "${bundle}/MacOS" "${bundle}/Resources"
    cp "$TMP_BIN/${bin_name}" "${bundle}/MacOS/OpenVibely"
    chmod +x "${bundle}/MacOS/OpenVibely"

    cat > "${bundle}/Info.plist" << PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>OpenVibely</string>
  <key>CFBundleDisplayName</key><string>OpenVibely</string>
  <key>CFBundleIdentifier</key><string>com.openvibely.desktop</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleExecutable</key><string>OpenVibely</string>
  <key>LSMinimumSystemVersion</key><string>12.0</string>
</dict>
</plist>
PLIST

    log "Packaging $zip_name..."
    run bash -c "cd '${staging_dir}' && zip -r '${DIST_DIR}/${zip_name}' '${app_name}' -x '*.DS_Store'"
    log "Created: $zip_name"
}

if [[ "$HOST_OS" == "Darwin" ]]; then
    # Build for both architectures
    # macOS SDK supports CGO cross-compile between amd64 ↔ arm64
    build_macos_app amd64 || warn "darwin/amd64 desktop build failed — skipping"
    build_macos_app arm64 || warn "darwin/arm64 desktop build failed — skipping"
else
    warn "Skipping macOS desktop app bundles — build host is $HOST_OS, not macOS."
    warn "To build macOS desktop artifacts, run this script on a macOS machine."
fi

###############################################################################
# 6. Windows desktop-cli (requires mingw-w64 cross-compiler)
###############################################################################

if command -v x86_64-w64-mingw32-gcc &>/dev/null; then
    log "Building Windows desktop-cli (amd64 with mingw-w64)..."
    build_binary "$TMP_BIN/desktop_windows_amd64.exe" ./cmd/desktop windows amd64 1 x86_64-w64-mingw32-gcc
    WINDOWS_DESKTOP_OK=1
else
    warn "mingw-w64 not found — skipping Windows desktop-cli build."
    warn "Install with: brew install mingw-w64  (macOS) or apt install mingw-w64  (Linux)"
    WINDOWS_DESKTOP_OK=0
fi

###############################################################################
# 7. Package server tarballs and zips
###############################################################################

package_server_tar() {
    local goos="$1" goarch="$2"
    local src_bin="$TMP_BIN/server_${goos}_${goarch}"
    local artifact="openvibely_${VERSION}_${goos}_${goarch}_server.tar.gz"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would package $artifact"
        return
    fi
    [[ -f "$src_bin" ]] || { warn "Binary missing: $src_bin — skipping $artifact"; return; }

    log "Packaging $artifact..."
    local pkg_dir="${TMP_BIN}/pkg_${goos}_${goarch}"
    mkdir -p "$pkg_dir"
    cp "$src_bin" "$pkg_dir/openvibely"
    tar -czf "${DIST_DIR}/${artifact}" -C "$pkg_dir" openvibely
    log "Created: $artifact"
}

package_server_zip() {
    local goos="$1" goarch="$2"
    local src_bin="$TMP_BIN/server_${goos}_${goarch}.exe"
    local artifact="openvibely_${VERSION}_${goos}_${goarch}_server.zip"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would package $artifact"
        return
    fi
    [[ -f "$src_bin" ]] || { warn "Binary missing: $src_bin — skipping $artifact"; return; }

    log "Packaging $artifact..."
    local pkg_dir="${TMP_BIN}/pkg_${goos}_${goarch}_server"
    mkdir -p "$pkg_dir"
    cp "$src_bin" "$pkg_dir/openvibely.exe"
    bash -c "cd '${pkg_dir}' && zip '${DIST_DIR}/${artifact}' openvibely.exe"
    log "Created: $artifact"
}

package_server_tar darwin amd64
package_server_tar darwin arm64
package_server_tar linux  amd64
package_server_tar linux  arm64
package_server_zip windows amd64

if [[ "$WINDOWS_DESKTOP_OK" == "1" ]]; then
    win_desktop_artifact="openvibely_${VERSION}_windows_amd64_desktop-cli.zip"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would package $win_desktop_artifact"
    else
        log "Packaging $win_desktop_artifact..."
        local_pkg="${TMP_BIN}/pkg_win_desktop"
        mkdir -p "$local_pkg"
        cp "$TMP_BIN/desktop_windows_amd64.exe" "$local_pkg/openvibely-desktop.exe"
        bash -c "cd '${local_pkg}' && zip '${DIST_DIR}/${win_desktop_artifact}' openvibely-desktop.exe"
        log "Created: $win_desktop_artifact"
    fi
fi

###############################################################################
# 8. SHA256SUMS
###############################################################################

log "Generating SHA256SUMS..."
if [[ "${DRY_RUN:-0}" != "1" ]]; then
    (
        cd "$DIST_DIR"
        # Prefer sha256sum (Linux); fall back to shasum -a 256 (macOS)
        if command -v sha256sum &>/dev/null; then
            sha256sum ./*.zip ./*.tar.gz 2>/dev/null | sort > SHA256SUMS
        else
            shasum -a 256 ./*.zip ./*.tar.gz 2>/dev/null | sort > SHA256SUMS
        fi
    )
    log "SHA256SUMS written."
else
    echo "[DRY-RUN] Would generate SHA256SUMS in $DIST_DIR"
fi

###############################################################################
# 9. Summary
###############################################################################

echo ""
info "=============================="
info "Build complete: $DIST_DIR"
info "=============================="
if [[ "${DRY_RUN:-0}" != "1" ]]; then
    ls -lh "$DIST_DIR" 2>/dev/null || true
fi
info ""
info "Next step: generate release notes"
info "  ./release-notes.sh $VERSION <prev_tag> $DIST_DIR"
