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
#   openvibely_<version>_windows_arm64_server.zip
#   openvibely_<version>_windows_amd64_desktop.zip
#   openvibely_<version>_windows_arm64_desktop.zip
#   openvibely_<version>_linux_amd64_desktop.tar.gz
#   openvibely_<version>_linux_arm64_desktop.tar.gz
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
#   OPENVIBELY_WAILS3                  Optional wails3 executable path/command.
#
# Known limitations (matches v0.1.0):
#   - Linux desktop requires native GTK/WebKit development dependencies,
#     explicit prebuilt artifacts, or Wails Taskfile Docker images for both
#     amd64 and arm64.
#   - Windows desktop normally cross-compiles for amd64 and arm64 with
#     CGO_ENABLED=0; set OPENVIBELY_WINDOWS_DESKTOP_BINARY to provide a
#     prebuilt/signed amd64 candidate.
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

wails3_command() {
    local local_wails3="${REPO_ROOT:-$(git rev-parse --show-toplevel 2>/dev/null || true)}/.tools/wails3/bin/wails3"
    if [[ -n "${OPENVIBELY_WAILS3:-}" ]]; then
        printf '%s\n' "$OPENVIBELY_WAILS3"
    elif command -v wails3 &>/dev/null; then
        command -v wails3
    elif [[ -x "$local_wails3" ]]; then
        printf '%s\n' "$local_wails3"
    else
        return 1
    fi
}

has_wails3() {
    wails3_command >/dev/null 2>&1
}

run_wails3() {
    local wails3
    if wails3="$(wails3_command)"; then
        run "$wails3" "$@"
    else
        fail "wails3 is required for desktop release builds. Install it with: go install github.com/wailsapp/wails/v3/cmd/wails3@$(go list -m -f '{{.Version}}' github.com/wailsapp/wails/v3 2>/dev/null || echo latest)"
    fi
}

load_release_env_defaults() {
    local env_file="$1"
    local had_key=0 had_pub=0 had_mac_id=0 had_notary=0 had_win_sign=0 had_win_verify=0 had_azure_endpoint=0 had_azure_account=0 had_azure_profile=0 had_azure_sub=0 had_win_desktop=0 had_linux_desktop=0 had_linux_arm64_desktop=0
    local saved_key="" saved_pub="" saved_mac_id="" saved_notary="" saved_win_sign="" saved_win_verify="" saved_azure_endpoint="" saved_azure_account="" saved_azure_profile="" saved_azure_sub="" saved_win_desktop="" saved_linux_desktop="" saved_linux_arm64_desktop=""

    [[ ${OPENVIBELY_RELEASE_KEY_ID+x} ]] && { had_key=1; saved_key="$OPENVIBELY_RELEASE_KEY_ID"; }
    [[ ${OPENVIBELY_RELEASE_PUBLIC_KEY+x} ]] && { had_pub=1; saved_pub="$OPENVIBELY_RELEASE_PUBLIC_KEY"; }
    [[ ${OPENVIBELY_MACOS_SIGN_IDENTITY+x} ]] && { had_mac_id=1; saved_mac_id="$OPENVIBELY_MACOS_SIGN_IDENTITY"; }
    [[ ${OPENVIBELY_MACOS_NOTARY_PROFILE+x} ]] && { had_notary=1; saved_notary="$OPENVIBELY_MACOS_NOTARY_PROFILE"; }
    [[ ${OPENVIBELY_WINDOWS_SIGN_COMMAND+x} ]] && { had_win_sign=1; saved_win_sign="$OPENVIBELY_WINDOWS_SIGN_COMMAND"; }
    [[ ${OPENVIBELY_WINDOWS_VERIFY_COMMAND+x} ]] && { had_win_verify=1; saved_win_verify="$OPENVIBELY_WINDOWS_VERIFY_COMMAND"; }
    [[ ${OPENVIBELY_AZURE_SIGNING_ENDPOINT+x} ]] && { had_azure_endpoint=1; saved_azure_endpoint="$OPENVIBELY_AZURE_SIGNING_ENDPOINT"; }
    [[ ${OPENVIBELY_AZURE_SIGNING_ACCOUNT+x} ]] && { had_azure_account=1; saved_azure_account="$OPENVIBELY_AZURE_SIGNING_ACCOUNT"; }
    [[ ${OPENVIBELY_AZURE_SIGNING_PROFILE+x} ]] && { had_azure_profile=1; saved_azure_profile="$OPENVIBELY_AZURE_SIGNING_PROFILE"; }
    [[ ${OPENVIBELY_AZURE_SUBSCRIPTION_ID+x} ]] && { had_azure_sub=1; saved_azure_sub="$OPENVIBELY_AZURE_SUBSCRIPTION_ID"; }
    [[ ${OPENVIBELY_WINDOWS_DESKTOP_BINARY+x} ]] && { had_win_desktop=1; saved_win_desktop="$OPENVIBELY_WINDOWS_DESKTOP_BINARY"; }
    [[ ${OPENVIBELY_LINUX_DESKTOP_BINARY+x} ]] && { had_linux_desktop=1; saved_linux_desktop="$OPENVIBELY_LINUX_DESKTOP_BINARY"; }
    [[ ${OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY+x} ]] && { had_linux_arm64_desktop=1; saved_linux_arm64_desktop="$OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY"; }

    # shellcheck source=/dev/null
    source "$env_file"

    [[ "$had_key" == "1" ]] && OPENVIBELY_RELEASE_KEY_ID="$saved_key"
    [[ "$had_pub" == "1" ]] && OPENVIBELY_RELEASE_PUBLIC_KEY="$saved_pub"
    [[ "$had_mac_id" == "1" ]] && OPENVIBELY_MACOS_SIGN_IDENTITY="$saved_mac_id"
    [[ "$had_notary" == "1" ]] && OPENVIBELY_MACOS_NOTARY_PROFILE="$saved_notary"
    [[ "$had_win_sign" == "1" ]] && OPENVIBELY_WINDOWS_SIGN_COMMAND="$saved_win_sign"
    [[ "$had_win_verify" == "1" ]] && OPENVIBELY_WINDOWS_VERIFY_COMMAND="$saved_win_verify"
    [[ "$had_azure_endpoint" == "1" ]] && OPENVIBELY_AZURE_SIGNING_ENDPOINT="$saved_azure_endpoint"
    [[ "$had_azure_account" == "1" ]] && OPENVIBELY_AZURE_SIGNING_ACCOUNT="$saved_azure_account"
    [[ "$had_azure_profile" == "1" ]] && OPENVIBELY_AZURE_SIGNING_PROFILE="$saved_azure_profile"
    [[ "$had_azure_sub" == "1" ]] && OPENVIBELY_AZURE_SUBSCRIPTION_ID="$saved_azure_sub"
    [[ "$had_win_desktop" == "1" ]] && OPENVIBELY_WINDOWS_DESKTOP_BINARY="$saved_win_desktop"
    [[ "$had_linux_desktop" == "1" ]] && OPENVIBELY_LINUX_DESKTOP_BINARY="$saved_linux_desktop"
    [[ "$had_linux_arm64_desktop" == "1" ]] && OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY="$saved_linux_arm64_desktop"
    return 0
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

REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || true)"
if [[ "${SKIP_RELEASE_SIGNING_ENV:-0}" != "1" && -n "$REPO_ROOT" && -f "${REPO_ROOT}/.release-signing.env" ]]; then
    load_release_env_defaults "${REPO_ROOT}/.release-signing.env"
fi
if [[ "${SKIP_SIGNING_CHECK:-0}" != "1" && "${DRY_RUN:-0}" != "1" && -x "${SCRIPT_DIR}/check-release-signing.sh" ]]; then
    "${SCRIPT_DIR}/check-release-signing.sh"
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
LINUX_ARM64_DESKTOP_BINARY="${OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY:-}"
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
    local ldflags="-s -w -X github.com/openvibely/openvibely/internal/buildinfo.Version=${VERSION} -X github.com/openvibely/openvibely/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/openvibely/openvibely/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/openvibely/openvibely/internal/buildinfo.Artifact=binary -X github.com/openvibely/openvibely/internal/buildinfo.ReleaseKeyID=${RELEASE_KEY_ID} -X github.com/openvibely/openvibely/internal/buildinfo.ReleasePublicKey=${RELEASE_PUBLIC_KEY}"

    log "Building $goos/$goarch → $output (CGO_ENABLED=${cgo})"
    local cmd=(env GOOS="$goos" GOARCH="$goarch" CGO_ENABLED="$cgo")
    cmd+=(go build -ldflags="$ldflags" -o "$output" "$pkg")
    run "${cmd[@]}"
}

desktop_ldflags() {
    local goos="$1"
    local ldflags="-s -w -X github.com/openvibely/openvibely/internal/buildinfo.Version=${VERSION} -X github.com/openvibely/openvibely/internal/buildinfo.Commit=${BUILD_COMMIT} -X github.com/openvibely/openvibely/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/openvibely/openvibely/internal/buildinfo.Artifact=desktop -X github.com/openvibely/openvibely/internal/buildinfo.ReleaseKeyID=${RELEASE_KEY_ID} -X github.com/openvibely/openvibely/internal/buildinfo.ReleasePublicKey=${RELEASE_PUBLIC_KEY}"
    if [[ "$goos" == "windows" ]]; then
        ldflags="${ldflags} -H windowsgui"
    fi
    printf '%s' "$ldflags"
}

build_desktop_binary() {
    local output="$1" goos="$2" goarch="$3"
    local cgo="${4:-}"
    local cross_image="${5:-}"
    local ldflags
    ldflags="$(desktop_ldflags "$goos")"

    log "Building desktop $goos/$goarch with wails3 → $output"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        if [[ -n "$cross_image" ]]; then
            echo -e "${YELLOW}[DRY-RUN]${NC} OPENVIBELY_DESKTOP_LDFLAGS=\"$ldflags\" CROSS_IMAGE=\"$cross_image\" wails3 build GOOS=$goos GOARCH=$goarch OUTPUT=$output${cgo:+ CGO_ENABLED=$cgo}"
        else
            echo -e "${YELLOW}[DRY-RUN]${NC} OPENVIBELY_DESKTOP_LDFLAGS=\"$ldflags\" wails3 build GOOS=$goos GOARCH=$goarch OUTPUT=$output${cgo:+ CGO_ENABLED=$cgo}"
        fi
        return
    fi

    if [[ -n "$cross_image" && -n "$cgo" ]]; then
        OPENVIBELY_DESKTOP_LDFLAGS="$ldflags" CROSS_IMAGE="$cross_image" run_wails3 build "GOOS=$goos" "GOARCH=$goarch" "OUTPUT=$output" "CGO_ENABLED=$cgo"
    elif [[ -n "$cross_image" ]]; then
        OPENVIBELY_DESKTOP_LDFLAGS="$ldflags" CROSS_IMAGE="$cross_image" run_wails3 build "GOOS=$goos" "GOARCH=$goarch" "OUTPUT=$output"
    elif [[ -n "$cgo" ]]; then
        OPENVIBELY_DESKTOP_LDFLAGS="$ldflags" run_wails3 build "GOOS=$goos" "GOARCH=$goarch" "OUTPUT=$output" "CGO_ENABLED=$cgo"
    else
        OPENVIBELY_DESKTOP_LDFLAGS="$ldflags" run_wails3 build "GOOS=$goos" "GOARCH=$goarch" "OUTPUT=$output"
    fi
}

host_goarch() {
    case "$(uname -m)" in
        x86_64|amd64) printf 'amd64\n' ;;
        arm64|aarch64) printf 'arm64\n' ;;
        *) printf '%s\n' "$(uname -m)" ;;
    esac
}

wails_cross_image_for_arch() {
    local goarch="$1"
    local upper_arch var_name
    upper_arch="$(printf '%s' "$goarch" | tr '[:lower:]' '[:upper:]')"
    var_name="OPENVIBELY_WAILS_CROSS_IMAGE_${upper_arch}"
    if [[ -n "${!var_name:-}" ]]; then
        printf '%s\n' "${!var_name}"
    else
        printf 'wails-cross-%s\n' "$goarch"
    fi
}

ensure_wails_cross_image() {
    local goarch="$1"
    local image="$2"
    local image_arch

    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would ensure Wails Docker image $image for linux/$goarch"
        return
    fi
    image_arch="$(docker image inspect "$image" --format '{{.Architecture}}' 2>/dev/null || true)"
    if [[ "$image_arch" == "$goarch" ]]; then
        return
    fi
    log "Preparing Wails Docker image $image for linux/$goarch..."
    CROSS_IMAGE="$image" CROSS_PLATFORM="linux/${goarch}" run_wails3 task setup:docker
}

sign_windows_binary() {
    local binary="$1"
    log "Authenticode signing and timestamping $(basename "$binary")..."
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} $WINDOWS_SIGN_COMMAND $binary"
        echo -e "${YELLOW}[DRY-RUN]${NC} $WINDOWS_VERIFY_COMMAND $binary"
    else
        OPENVIBELY_AZURE_SIGNING_ENDPOINT="${OPENVIBELY_AZURE_SIGNING_ENDPOINT:-}" \
            OPENVIBELY_AZURE_SIGNING_ACCOUNT="${OPENVIBELY_AZURE_SIGNING_ACCOUNT:-}" \
            OPENVIBELY_AZURE_SIGNING_PROFILE="${OPENVIBELY_AZURE_SIGNING_PROFILE:-}" \
            OPENVIBELY_AZURE_SUBSCRIPTION_ID="${OPENVIBELY_AZURE_SUBSCRIPTION_ID:-}" \
            AZURE_ACCESS_TOKEN="${AZURE_ACCESS_TOKEN:-}" \
            "$WINDOWS_SIGN_COMMAND" "$binary"
        "$WINDOWS_VERIFY_COMMAND" "$binary"
    fi
}

notarize_macos_binary_archive() {
    local binary="$1" archive="$2"
    log "Developer ID signing $(basename "$binary")..."
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} $SCRIPT_DIR/sign-macos.sh $binary"
    else
        env OPENVIBELY_MACOS_SIGN_IDENTITY="$MACOS_SIGN_IDENTITY" "$SCRIPT_DIR/sign-macos.sh" "$binary"
    fi
    run ditto -c -k --keepParent "$binary" "$archive"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} $SCRIPT_DIR/notarize-macos-archive.sh $archive"
    else
        env OPENVIBELY_MACOS_NOTARY_PROFILE="$MACOS_NOTARY_PROFILE" "$SCRIPT_DIR/notarize-macos-archive.sh" "$archive"
    fi
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

# windows/arm64 server
build_binary "$TMP_BIN/server_windows_arm64.exe" ./cmd/server windows arm64
sign_windows_binary "$TMP_BIN/server_windows_arm64.exe"

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
    build_desktop_binary "$TMP_BIN/${bin_name}" darwin "$goarch" 1

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
    env OPENVIBELY_MACOS_SIGN_IDENTITY="$MACOS_SIGN_IDENTITY" "$SCRIPT_DIR/sign-macos.sh" "$app_dir"
    local notary_zip="${TMP_BIN}/OpenVibely_${goarch}_notary.zip"
    run ditto -c -k --keepParent "$app_dir" "$notary_zip"
    env OPENVIBELY_MACOS_NOTARY_PROFILE="$MACOS_NOTARY_PROFILE" "$SCRIPT_DIR/notarize-macos-archive.sh" "$notary_zip"
    run xcrun stapler staple "$app_dir"
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
# 6. Windows desktop
###############################################################################

if [[ -n "$WINDOWS_DESKTOP_BINARY" ]]; then
    [[ "${DRY_RUN:-0}" == "1" || -f "$WINDOWS_DESKTOP_BINARY" ]] || fail "OPENVIBELY_WINDOWS_DESKTOP_BINARY does not exist."
    run cp "$WINDOWS_DESKTOP_BINARY" "$TMP_BIN/desktop_windows_amd64.exe"
else
    build_desktop_binary "$TMP_BIN/desktop_windows_amd64.exe" windows amd64 0
fi
build_desktop_binary "$TMP_BIN/desktop_windows_arm64.exe" windows arm64 0

if [[ "${DRY_RUN:-0}" != "1" && ! -f "$TMP_BIN/desktop_windows_amd64.exe" ]]; then
    fail "Official releases require a Windows amd64 desktop build."
fi
if [[ "${DRY_RUN:-0}" != "1" && ! -f "$TMP_BIN/desktop_windows_arm64.exe" ]]; then
    fail "Official releases require a Windows arm64 desktop build."
elif [[ "${DRY_RUN:-0}" == "1" ]]; then
    echo -e "${YELLOW}[DRY-RUN]${NC} Would build Windows desktop artifacts with Go cross-compilation"
fi
sign_windows_binary "$TMP_BIN/desktop_windows_amd64.exe"
sign_windows_binary "$TMP_BIN/desktop_windows_arm64.exe"

build_linux_desktop() {
    local goarch="$1"
    local output="$TMP_BIN/desktop_linux_${goarch}"
    local prebuilt=""
    local prebuilt_var=""
    local cross_image

    case "$goarch" in
        amd64) prebuilt="$LINUX_DESKTOP_BINARY"; prebuilt_var="OPENVIBELY_LINUX_DESKTOP_BINARY" ;;
        arm64) prebuilt="$LINUX_ARM64_DESKTOP_BINARY"; prebuilt_var="OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY" ;;
    esac

    if [[ "$HOST_OS" == "Linux" && "$(host_goarch)" == "$goarch" ]]; then
        log "Building Linux desktop ($goarch natively)..."
        build_desktop_binary "$output" linux "$goarch" 1
    elif [[ -n "$prebuilt" ]]; then
        [[ "${DRY_RUN:-0}" == "1" || -f "$prebuilt" ]] || fail "${prebuilt_var} does not exist."
        run cp "$prebuilt" "$output"
    elif command -v docker &>/dev/null && has_wails3; then
        cross_image="$(wails_cross_image_for_arch "$goarch")"
        ensure_wails_cross_image "$goarch" "$cross_image"
        log "Building Linux desktop ($goarch with Wails Docker path)..."
        build_desktop_binary "$output" linux "$goarch" 1 "$cross_image"
    elif [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would require native Linux, Wails Docker support, or ${prebuilt_var}"
    else
        fail "Official releases require a Linux $goarch desktop build; run on matching Linux, install wails3+Docker, or set ${prebuilt_var}."
    fi
}

build_linux_desktop amd64
build_linux_desktop arm64

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
package_server_zip windows arm64

package_windows_desktop_zip() {
    local goarch="$1"
    local artifact="openvibely_${VERSION}_windows_${goarch}_desktop.zip"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would package $artifact"
        return
    fi

    log "Packaging $artifact..."
    local local_pkg="${TMP_BIN}/pkg_win_desktop_${goarch}"
    mkdir -p "$local_pkg"
    cp "$TMP_BIN/desktop_windows_${goarch}.exe" "$local_pkg/openvibely-desktop.exe"
    bash -c "cd '${local_pkg}' && zip '${DIST_DIR}/${artifact}' openvibely-desktop.exe"
    log "Created: $artifact"
}

package_linux_desktop_tar() {
    local goarch="$1"
    local artifact="openvibely_${VERSION}_linux_${goarch}_desktop.tar.gz"
    if [[ "${DRY_RUN:-0}" == "1" ]]; then
        echo -e "${YELLOW}[DRY-RUN]${NC} Would package $artifact"
        return
    fi

    log "Packaging $artifact..."
    local linux_pkg="${TMP_BIN}/pkg_linux_desktop_${goarch}"
    mkdir -p "$linux_pkg"
    cp "$TMP_BIN/desktop_linux_${goarch}" "$linux_pkg/openvibely-desktop"
    chmod +x "$linux_pkg/openvibely-desktop"
    tar -czf "${DIST_DIR}/${artifact}" -C "$linux_pkg" openvibely-desktop
    log "Created: $artifact"
}

if [[ "${DRY_RUN:-0}" == "1" ]]; then
    package_windows_desktop_zip amd64
    package_windows_desktop_zip arm64
    package_linux_desktop_tar amd64
    package_linux_desktop_tar arm64
else
    package_windows_desktop_zip amd64
    package_windows_desktop_zip arm64
    package_linux_desktop_tar amd64
    package_linux_desktop_tar arm64
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
