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
#   openvibely_<version>_darwin_amd64_server.zip
#   openvibely_<version>_darwin_arm64_server.zip
#   openvibely_<version>_linux_amd64_server.tar.gz
#   openvibely_<version>_linux_arm64_server.tar.gz
#   openvibely_<version>_windows_amd64_server.zip
#   openvibely_<version>_windows_amd64_desktop-cli.zip
#   openvibely_<version>_linux_amd64_desktop.tar.gz
#   SHA256SUMS
#
# Environment variables:
#   DRY_RUN=1          Print commands without executing build steps.
#   SKIP_GENERATE=1    Skip templ generate + swagger (if already done).
#   DIST_DIR=<path>                       Override distribution output directory.
#   OPENVIBELY_MACOS_SIGN_IDENTITY      Developer ID Application identity.
#   OPENVIBELY_MACOS_NOTARY_PROFILE     notarytool keychain profile.
#   OPENVIBELY_WINDOWS_SIGN_COMMAND     Executable invoked with each Windows binary path.
#   OPENVIBELY_WINDOWS_VERIFY_COMMAND   Executable that fails unless that binary is Authenticode signed and timestamped.
#
# Known limitations (matches v0.1.0):
#   - Linux desktop requires native GTK/WebKit development dependencies or an
#     explicit OPENVIBELY_LINUX_DESKTOP_BINARY produced by the Linux release job.
#   - Windows desktop requires mingw-w64 or OPENVIBELY_WINDOWS_DESKTOP_BINARY.
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

RELEASE_KEY_ID="${OPENVIBELY_RELEASE_KEY_ID:-}"
RELEASE_PUBLIC_KEY="${OPENVIBELY_RELEASE_PUBLIC_KEY:-}"
[[ -n "$RELEASE_KEY_ID" ]] || fail "OPENVIBELY_RELEASE_KEY_ID is required for official release artifacts."
[[ "$RELEASE_KEY_ID" =~ ^[A-Za-z0-9._-]{1,128}$ ]] || fail "OPENVIBELY_RELEASE_KEY_ID must be a canonical key identifier."
[[ -n "$RELEASE_PUBLIC_KEY" ]] || fail "OPENVIBELY_RELEASE_PUBLIC_KEY is required for official release artifacts."
[[ "$RELEASE_PUBLIC_KEY" =~ ^[A-Za-z0-9+/]{43}=$ ]] || fail "OPENVIBELY_RELEASE_PUBLIC_KEY must be a canonical base64-encoded 32-byte Ed25519 public key."
HOST_OS="$(uname -s)"
WINDOWS_SIGN_COMMAND="${OPENVIBELY_WINDOWS_SIGN_COMMAND:-}"
WINDOWS_VERIFY_COMMAND="${OPENVIBELY_WINDOWS_VERIFY_COMMAND:-}"
WINDOWS_DESKTOP_BINARY="${OPENVIBELY_WINDOWS_DESKTOP_BINARY:-}"
LINUX_DESKTOP_BINARY="${OPENVIBELY_LINUX_DESKTOP_BINARY:-}"
[[ -n "$WINDOWS_SIGN_COMMAND" ]] || fail "OPENVIBELY_WINDOWS_SIGN_COMMAND is required for official Windows release artifacts."
[[ "${DRY_RUN:-0}" == "1" || -x "$WINDOWS_SIGN_COMMAND" ]] || fail "OPENVIBELY_WINDOWS_SIGN_COMMAND must name an executable signing hook."
[[ -n "$WINDOWS_VERIFY_COMMAND" ]] || fail "OPENVIBELY_WINDOWS_VERIFY_COMMAND is required for official Windows release validation."
[[ "${DRY_RUN:-0}" == "1" || -x "$WINDOWS_VERIFY_COMMAND" ]] || fail "OPENVIBELY_WINDOWS_VERIFY_COMMAND must name an executable verification hook."
if [[ "$HOST_OS" == "Darwin" ]]; then
    MACOS_SIGN_IDENTITY="${OPENVIBELY_MACOS_SIGN_IDENTITY:-}"
    MACOS_NOTARY_PROFILE="${OPENVIBELY_MACOS_NOTARY_PROFILE:-}"
    [[ -n "$MACOS_SIGN_IDENTITY" ]] || fail "OPENVIBELY_MACOS_SIGN_IDENTITY is required for macOS release artifacts."
    [[ -n "$MACOS_NOTARY_PROFILE" ]] || fail "OPENVIBELY_MACOS_NOTARY_PROFILE is required for macOS notarization."
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

BUILD_COMMIT="$(git rev-parse HEAD)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

build_binary() {
    local output="$1" pkg="$2" goos="$3" goarch="$4"
    local cgo="${5:-0}"
    local cc="${6:-}"
    local artifact="binary"
    [[ "$pkg" == "./cmd/desktop" ]] && artifact="desktop"
    local ldflags="-s -w -X github.com/openvibely/openvibely/internal/buildinfo.Version=${VERSION} -X github.com/openvibely/openvibely/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/openvibely/openvibely/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/openvibely/openvibely/internal/buildinfo.Artifact=${artifact} -X github.com/openvibely/openvibely/internal/buildinfo.ReleaseKeyID=${RELEASE_KEY_ID} -X github.com/openvibely/openvibely/internal/buildinfo.ReleasePublicKey=${RELEASE_PUBLIC_KEY}"

    log "Building $goos/$goarch → $output (CGO_ENABLED=${cgo})"
    local cmd=(env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="$cgo")
    [[ -n "$cc" ]] && cmd+=(CC="$cc")
    cmd+=(go build -ldflags="$ldflags" -o "$output" "$pkg")
    run "${cmd[@]}"
}

sign_windows_binary() {
    local binary="$1"
    log "Authenticode signing and timestamping $(basename "$binary")..."
    run "$WINDOWS_SIGN_COMMAND" "$binary"
    run "$WINDOWS_VERIFY_COMMAND" "$binary"
}

notarize_macos_binary_archive() {
    local binary="$1" archive="$2"
    log "Developer ID signing $(basename "$binary")..."
    run codesign --force --options runtime --timestamp --sign "$MACOS_SIGN_IDENTITY" "$binary"
    run codesign --verify --strict --verbose=2 "$binary"
    run ditto -c -k --keepParent "$binary" "$archive"
    run xcrun notarytool submit "$archive" --keychain-profile "$MACOS_NOTARY_PROFILE" --wait
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
sign_windows_binary "$TMP_BIN/server_windows_amd64.exe"

###############################################################################
# 5. macOS desktop app bundles
###############################################################################

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
        echo -e "${YELLOW}[DRY-RUN]${NC} Would sign and notarize ${app_name}, then staple and verify it"
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

    log "Signing and notarizing ${app_name}..."
    run codesign --force --deep --options runtime --timestamp --sign "$MACOS_SIGN_IDENTITY" "$app_dir"
    local notary_zip="${TMP_BIN}/OpenVibely_${goarch}_notary.zip"
    run ditto -c -k --keepParent "$app_dir" "$notary_zip"
    run xcrun notarytool submit "$notary_zip" --keychain-profile "$MACOS_NOTARY_PROFILE" --wait
    run xcrun stapler staple "$app_dir"
    run codesign --verify --deep --strict --verbose=2 "$app_dir"
    run spctl --assess --type execute --verbose=2 "$app_dir"

    log "Packaging $zip_name..."
    run ditto -c -k --keepParent "$app_dir" "${DIST_DIR}/${zip_name}"
    log "Created: $zip_name"
}

if [[ "$HOST_OS" == "Darwin" ]]; then
    # Build for both architectures
    # macOS SDK supports CGO cross-compile between amd64 ↔ arm64
    build_macos_app amd64
    build_macos_app arm64
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
elif [[ -n "$WINDOWS_DESKTOP_BINARY" ]]; then
    [[ "${DRY_RUN:-0}" == "1" || -f "$WINDOWS_DESKTOP_BINARY" ]] || fail "OPENVIBELY_WINDOWS_DESKTOP_BINARY does not exist."
    run cp "$WINDOWS_DESKTOP_BINARY" "$TMP_BIN/desktop_windows_amd64.exe"
elif [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo -e "${YELLOW}[DRY-RUN]${NC} Would require mingw-w64 or OPENVIBELY_WINDOWS_DESKTOP_BINARY"
else
    fail "Official releases require a Windows desktop build: install mingw-w64 or set OPENVIBELY_WINDOWS_DESKTOP_BINARY."
fi
sign_windows_binary "$TMP_BIN/desktop_windows_amd64.exe"

if [[ "$HOST_OS" == "Linux" ]]; then
    log "Building Linux desktop (amd64)..."
    build_binary "$TMP_BIN/desktop_linux_amd64" ./cmd/desktop linux amd64 1
elif [[ -n "$LINUX_DESKTOP_BINARY" ]]; then
    [[ "${DRY_RUN:-0}" == "1" || -f "$LINUX_DESKTOP_BINARY" ]] || fail "OPENVIBELY_LINUX_DESKTOP_BINARY does not exist."
    run cp "$LINUX_DESKTOP_BINARY" "$TMP_BIN/desktop_linux_amd64"
elif [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo -e "${YELLOW}[DRY-RUN]${NC} Would require a native Linux build or OPENVIBELY_LINUX_DESKTOP_BINARY"
else
    fail "Official releases require a Linux desktop build; run on Linux or set OPENVIBELY_LINUX_DESKTOP_BINARY."
fi

###############################################################################
# 7. Package server tarballs and zips
###############################################################################

package_macos_server_zip() {
    local goarch="$1"
    local src_bin="$TMP_BIN/server_darwin_${goarch}"
    local artifact="openvibely_${VERSION}_darwin_${goarch}_server.zip"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would Developer ID sign and notarize $artifact"
        return
    fi
    [[ "$HOST_OS" == "Darwin" ]] || fail "Official macOS server archives must be built, signed, and notarized on macOS."
    local pkg_dir="${TMP_BIN}/pkg_darwin_${goarch}"
    mkdir -p "$pkg_dir"
    cp "$src_bin" "$pkg_dir/openvibely"
    chmod +x "$pkg_dir/openvibely"
    notarize_macos_binary_archive "$pkg_dir/openvibely" "${DIST_DIR}/${artifact}"
    log "Created signed and notarized: $artifact"
}

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

package_macos_server_zip amd64
package_macos_server_zip arm64
package_server_tar linux  amd64
package_server_tar linux  arm64
package_server_zip windows amd64

win_desktop_artifact="openvibely_${VERSION}_windows_amd64_desktop-cli.zip"
linux_desktop_artifact="openvibely_${VERSION}_linux_amd64_desktop.tar.gz"
if [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo -e "${YELLOW}[DRY-RUN]${NC} Would package $win_desktop_artifact"
    echo -e "${YELLOW}[DRY-RUN]${NC} Would package $linux_desktop_artifact"
else
    log "Packaging $win_desktop_artifact..."
    local_pkg="${TMP_BIN}/pkg_win_desktop"
    mkdir -p "$local_pkg"
    cp "$TMP_BIN/desktop_windows_amd64.exe" "$local_pkg/openvibely-desktop.exe"
    bash -c "cd '${local_pkg}' && zip '${DIST_DIR}/${win_desktop_artifact}' openvibely-desktop.exe"
    log "Created: $win_desktop_artifact"

    log "Packaging $linux_desktop_artifact..."
    linux_pkg="${TMP_BIN}/pkg_linux_desktop"
    mkdir -p "$linux_pkg"
    cp "$TMP_BIN/desktop_linux_amd64" "$linux_pkg/openvibely-desktop"
    chmod +x "$linux_pkg/openvibely-desktop"
    tar -czf "${DIST_DIR}/${linux_desktop_artifact}" -C "$linux_pkg" openvibely-desktop
    log "Created: $linux_desktop_artifact"
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
