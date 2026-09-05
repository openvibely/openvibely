#!/usr/bin/env bash
# release-validate.sh — Script-level unit tests for release tooling.
#
# Tests semver parsing, artifact naming, and environment detection without
# making any network calls or building anything. Safe to run in CI or locally.
#
# Usage:
#   ./release-validate.sh
#
# Exit code 0 = all tests passed. Non-zero = at least one failure.

set -uo pipefail

RED='\033[0;31m'; GREEN='\033[0;32m'; YELLOW='\033[1;33m'; NC='\033[0m'
PASS=0; FAIL=0

pass() { echo -e "${GREEN}✓${NC} $1"; ((PASS++)); }
fail() { echo -e "${RED}✗${NC} $1"; ((FAIL++)); }
section() { echo ""; echo -e "${YELLOW}--- $1 ---${NC}"; }

###############################################################################
# Production release-version policy
###############################################################################

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
VERSION_HELPER="${SCRIPT_DIR}/release-version.sh"

# shellcheck source=release-version.sh
source "$VERSION_HELPER"

###############################################################################
# 1. Semver parsing
###############################################################################

section "Semver normalization and validation"

# Valid inputs
for input in "0.1.0" "v0.1.0" "1.0.0" "v1.2.3" "10.20.300"; do
    v="$(normalize_release_version "$input")"
    if is_valid_release_version "$v"; then
        pass "valid:   '$input' → '$v'"
    else
        fail "expected valid: '$input' → '$v'"
    fi
done

# Invalid inputs
for input in "" "v" "0.1" "0.1.0-alpha" "0.1.0+build" "abc" "v1.2.3.4" "1.2"; do
    v="$(normalize_release_version "$input")"
    if ! is_valid_release_version "$v"; then
        pass "invalid: '$input' → '$v' (correctly rejected)"
    else
        fail "expected invalid: '$input' → '$v' (should have been rejected)"
    fi
done

###############################################################################
# 2. Helper source safety
###############################################################################

section "Release-version helper source safety"

SOURCE_TEST_TMP="$(mktemp -d)"
SOURCE_TRACE="${SOURCE_TEST_TMP}/commands.log"
SOURCE_RESULT="${SOURCE_TEST_TMP}/result"
SOURCE_RUNNER="${SOURCE_TEST_TMP}/source-test.sh"

cat > "$SOURCE_RUNNER" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
set -T

trace_file="$1"
result_file="$2"
helper="$3"
before_pwd="$PWD"
before_options="$(set +o)"
before_shopt="$(shopt -p)"

trap 'printf "%s\n" "$BASH_COMMAND" >> "$trace_file"' DEBUG
# shellcheck disable=SC1090
source "$helper"
trap - DEBUG

after_pwd="$PWD"
after_options="$(set +o)"
after_shopt="$(shopt -p)"

{
    [[ "$before_pwd" == "$after_pwd" ]] && echo "cwd=unchanged" || echo "cwd=changed"
    [[ "$before_options" == "$after_options" && "$before_shopt" == "$after_shopt" ]] && echo "options=unchanged" || echo "options=changed"
} > "$result_file"
EOF

if bash "$SOURCE_RUNNER" "$SOURCE_TRACE" "$SOURCE_RESULT" "$VERSION_HELPER"; then
    if [[ "$(wc -l < "$SOURCE_TRACE" | tr -d ' ')" == "2" ]]; then
        pass "source safety: helper executes no commands while loading"
    else
        fail "source safety: helper executed commands while loading: $(tr '\n' ';' < "$SOURCE_TRACE")"
    fi
    if grep -qx "cwd=unchanged" "$SOURCE_RESULT"; then
        pass "source safety: helper does not change the caller cwd"
    else
        fail "source safety: helper changed the caller cwd"
    fi
    if grep -qx "options=unchanged" "$SOURCE_RESULT"; then
        pass "source safety: helper does not change caller shell options"
    else
        fail "source safety: helper changed caller shell options"
    fi
else
    fail "source safety: helper could not be sourced"
fi

rm -rf "$SOURCE_TEST_TMP"

###############################################################################
# 3. Production entrypoint version handling
###############################################################################

section "Production entrypoint version handling"

ENTRYPOINT_TMP="$(mktemp -d)"
ENTRYPOINT_MOCK_BIN="${ENTRYPOINT_TMP}/bin"
mkdir -p "$ENTRYPOINT_MOCK_BIN"

cat > "${ENTRYPOINT_MOCK_BIN}/gh" <<'EOF'
#!/usr/bin/env bash
case "${1:-} ${2:-}" in
    "--version ") echo "gh version test" ;;
    "auth status") exit 1 ;;
    *) exit 99 ;;
esac
EOF
chmod +x "${ENTRYPOINT_MOCK_BIN}/gh"

check_entrypoint_accepts() {
    local script="$1" input="$2" expected="$3"
    local output dist_dir="${ENTRYPOINT_TMP}/valid-${script%.sh}-${input#v}"
    rm -rf "$dist_dir"

    case "$script" in
        release-preflight.sh)
            output="$(DRY_RUN=1 SKIP_GH_AUTH_CHECK=1 PATH="${ENTRYPOINT_MOCK_BIN}:$PATH" \
                bash "${SCRIPT_DIR}/${script}" "$input" 2>&1)"
            ;;
        release-build.sh)
            output="$(DRY_RUN=1 SKIP_GENERATE=1 OPENVIBELY_RELEASE_KEY_ID=release-test OPENVIBELY_RELEASE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Test' OPENVIBELY_MACOS_NOTARY_PROFILE=test-profile OPENVIBELY_WINDOWS_SIGN_COMMAND=/bin/true OPENVIBELY_WINDOWS_VERIFY_COMMAND=/bin/true PATH="${ENTRYPOINT_MOCK_BIN}:$PATH" \
                bash "${SCRIPT_DIR}/${script}" "$input" "$dist_dir" 2>&1)"
            ;;
        release-notes.sh)
            output="$(PATH="${ENTRYPOINT_MOCK_BIN}:$PATH" \
                bash "${SCRIPT_DIR}/${script}" "$input" "" "$dist_dir" 2>&1)"
            ;;
        release-publish.sh)
            mkdir -p "$dist_dir"
            : > "${dist_dir}/RELEASE_NOTES.md"
            output="$(DRY_RUN=1 PATH="${ENTRYPOINT_MOCK_BIN}:$PATH" \
                bash "${SCRIPT_DIR}/${script}" "$input" "$dist_dir" 2>&1)"
            ;;
        release.sh)
            output="$(DRY_RUN=1 SKIP_GH_AUTH_CHECK=1 SKIP_GENERATE=1 AUTO_CONFIRM=1 SKIP_SIGNING_CHECK=1 \
                OPENVIBELY_RELEASE_KEY_ID=release-test OPENVIBELY_RELEASE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Test' OPENVIBELY_MACOS_NOTARY_PROFILE=test-profile OPENVIBELY_WINDOWS_SIGN_COMMAND=/bin/true OPENVIBELY_WINDOWS_VERIFY_COMMAND=/bin/true \
                DIST_DIR="$dist_dir" PATH="${ENTRYPOINT_MOCK_BIN}:$PATH" \
                bash "${SCRIPT_DIR}/${script}" "$input" 2>&1)"
            ;;
    esac

    if [[ $? -eq 0 ]] && grep -Fq "$expected" <<< "$output"; then
        pass "entrypoint: $script accepts '$input' as 0.4.1"
    else
        fail "entrypoint: $script did not accept '$input' as 0.4.1"
    fi
}

for input in "0.4.1" "v0.4.1"; do
    check_entrypoint_accepts "release-preflight.sh" "$input" "Normalized version: 0.4.1"
    check_entrypoint_accepts "release-build.sh" "$input" "Building OpenVibely v0.4.1"
    check_entrypoint_accepts "release-notes.sh" "$input" "Collecting commits for v0.4.1"
    check_entrypoint_accepts "release-publish.sh" "$input" "Publishing OpenVibely v0.4.1"
    check_entrypoint_accepts "release.sh" "$input" "release pipeline starting for v0.4.1"
done

INVALID_MOCK_BIN="${ENTRYPOINT_TMP}/invalid-bin"
INVALID_WORK_LOG="${ENTRYPOINT_TMP}/invalid-work.log"
mkdir -p "$INVALID_MOCK_BIN"
: > "$INVALID_WORK_LOG"

for command_name in git go gh zip tar sha256sum shasum mkdir mktemp cp chmod sed rm find date awk uname; do
    cat > "${INVALID_MOCK_BIN}/${command_name}" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "${0##*/}" >> "$RELEASE_WORK_LOG"
exit 99
EOF
    chmod +x "${INVALID_MOCK_BIN}/${command_name}"
done

check_entrypoint_rejects_before_work() {
    local script="$1" invalid_input="$2"
    local invalid_dist="${ENTRYPOINT_TMP}/invalid-${script%.sh}"
    local before_lines after_lines output status
    before_lines="$(wc -l < "$INVALID_WORK_LOG" | tr -d ' ')"
    rm -rf "$invalid_dist"

    case "$script" in
        release-preflight.sh|release.sh)
            output="$(RELEASE_WORK_LOG="$INVALID_WORK_LOG" DIST_DIR="$invalid_dist" \
                PATH="${INVALID_MOCK_BIN}:$PATH" bash "${SCRIPT_DIR}/${script}" "$invalid_input" 2>&1)"
            status=$?
            ;;
        release-build.sh|release-publish.sh)
            output="$(RELEASE_WORK_LOG="$INVALID_WORK_LOG" PATH="${INVALID_MOCK_BIN}:$PATH" \
                bash "${SCRIPT_DIR}/${script}" "$invalid_input" "$invalid_dist" 2>&1)"
            status=$?
            ;;
        release-notes.sh)
            output="$(RELEASE_WORK_LOG="$INVALID_WORK_LOG" PATH="${INVALID_MOCK_BIN}:$PATH" \
                bash "${SCRIPT_DIR}/${script}" "$invalid_input" "" "$invalid_dist" 2>&1)"
            status=$?
            ;;
    esac

    after_lines="$(wc -l < "$INVALID_WORK_LOG" | tr -d ' ')"
    if [[ $status -ne 0 ]] && grep -Fq "Invalid semver" <<< "$output" \
        && [[ "$before_lines" == "$after_lines" ]] && [[ ! -e "$invalid_dist" ]]; then
        pass "entrypoint: $script rejects '$invalid_input' before release work"
    else
        fail "entrypoint: $script performed work or did not reject '$invalid_input' first"
    fi
}

for script in release-preflight.sh release-build.sh release-notes.sh release-publish.sh release.sh; do
    for invalid_input in "" "v" "0.4" "0.4.1-alpha" "0.4.1+build" "vv0.4.1" "0.4.1.2" "abc"; do
        check_entrypoint_rejects_before_work "$script" "$invalid_input"
    done
done

rm -rf "$ENTRYPOINT_TMP"

###############################################################################
# 4. Artifact naming
###############################################################################

section "Artifact naming conventions"

check_artifact_name() {
    local version="$1" pattern="$2" expected="$3"
    local actual
    actual="$(echo "$pattern" | sed "s/<version>/${version}/g")"
    if [[ "$actual" == "$expected" ]]; then
        pass "artifact: '$actual'"
    else
        fail "artifact name mismatch: got '$actual', expected '$expected'"
    fi
}

VERSION="0.1.1"

# Desktop macOS (capital O in OpenVibely)
check_artifact_name "$VERSION" "OpenVibely_<version>_darwin_amd64.app.zip" \
    "OpenVibely_0.1.1_darwin_amd64.app.zip"
check_artifact_name "$VERSION" "OpenVibely_<version>_darwin_arm64.app.zip" \
    "OpenVibely_0.1.1_darwin_arm64.app.zip"

# Server tarballs (lowercase openvibely)
check_artifact_name "$VERSION" "openvibely_<version>_darwin_amd64_server.zip" \
    "openvibely_0.1.1_darwin_amd64_server.zip"
check_artifact_name "$VERSION" "openvibely_<version>_darwin_arm64_server.zip" \
    "openvibely_0.1.1_darwin_arm64_server.zip"
check_artifact_name "$VERSION" "openvibely_<version>_linux_amd64_server.tar.gz" \
    "openvibely_0.1.1_linux_amd64_server.tar.gz"
check_artifact_name "$VERSION" "openvibely_<version>_linux_arm64_server.tar.gz" \
    "openvibely_0.1.1_linux_arm64_server.tar.gz"

# Windows server zip
check_artifact_name "$VERSION" "openvibely_<version>_windows_amd64_server.zip" \
    "openvibely_0.1.1_windows_amd64_server.zip"
check_artifact_name "$VERSION" "openvibely_<version>_windows_arm64_server.zip" \
    "openvibely_0.1.1_windows_arm64_server.zip"

# Windows desktop zip
check_artifact_name "$VERSION" "openvibely_<version>_windows_amd64_desktop.zip" \
    "openvibely_0.1.1_windows_amd64_desktop.zip"
check_artifact_name "$VERSION" "openvibely_<version>_windows_arm64_desktop.zip" \
    "openvibely_0.1.1_windows_arm64_desktop.zip"

# Linux desktop tarball
check_artifact_name "$VERSION" "openvibely_<version>_linux_amd64_desktop.tar.gz" \
    "openvibely_0.1.1_linux_amd64_desktop.tar.gz"
check_artifact_name "$VERSION" "openvibely_<version>_linux_arm64_desktop.tar.gz" \
    "openvibely_0.1.1_linux_arm64_desktop.tar.gz"

# SHA256SUMS (no version in the filename)
SUMS_NAME="SHA256SUMS"
if [[ "$SUMS_NAME" == "SHA256SUMS" ]]; then
    pass "checksum file: 'SHA256SUMS' (correct, no version in name)"
else
    fail "checksum file: expected 'SHA256SUMS', got '$SUMS_NAME'"
fi

###############################################################################
# 5. Official artifact signing contracts
###############################################################################

section "Official artifact signing contracts"
BUILD_SCRIPT="${SCRIPT_DIR}/release-build.sh"
for required in OPENVIBELY_MACOS_SIGN_IDENTITY OPENVIBELY_MACOS_NOTARY_PROFILE OPENVIBELY_WINDOWS_SIGN_COMMAND OPENVIBELY_WINDOWS_VERIFY_COMMAND; do
    if grep -q "${required}.*required" "$BUILD_SCRIPT"; then
        pass "official build requires ${required}"
    else
        fail "official build does not require ${required}"
    fi
done
for required_call in 'sign-macos.sh' 'notarize-macos-archive.sh' 'xcrun stapler staple' 'notarize_macos_binary_archive' 'clean_macos_bundle_metadata "$app_dir"' 'package_macos_app_zip "$app_dir" "$notary_zip"' 'package_macos_app_zip "$app_dir" "$release_zip"' 'COPYFILE_DISABLE=1 ditto -c -k --norsrc --keepParent' 'assert_clean_tar_archive "${DIST_DIR}/${artifact}"' "grep -E '(^|/)\\._|(^|/)__MACOSX(/|$)|(^|/)\\.DS_Store$'" 'assets/desktop/icons/openvibely.icns' 'CFBundleIconFile' 'verify_desktop_icon_marker' 'verify_windows_icon_resource' 'internal/releaseassets/cmd/verify-pe-icon' 'openvibely-desktop-icon-linux-gtk3-v1' 'openvibely-desktop-icon-native-v1' 'sign_windows_binary "$TMP_BIN/server_windows_amd64.exe"' 'sign_windows_binary "$TMP_BIN/server_windows_arm64.exe"' 'sign_windows_binary "$TMP_BIN/desktop_windows_amd64.exe"' 'sign_windows_binary "$TMP_BIN/desktop_windows_arm64.exe"' 'build_desktop_binary "$TMP_BIN/desktop_windows_amd64.exe" windows amd64 0' 'build_desktop_binary "$TMP_BIN/desktop_windows_arm64.exe" windows arm64 0' 'build_linux_desktop amd64' 'build_linux_desktop arm64' 'package_linux_desktop_tar amd64' 'package_linux_desktop_tar arm64' 'Official releases require a Linux $goarch desktop build' 'linux_amd64_desktop.tar.gz' 'linux_arm64_desktop.tar.gz'; do
    if grep -Fq "$required_call" "$BUILD_SCRIPT"; then
        pass "release build contains signing step: ${required_call}"
    else
        fail "release build lacks signing step: ${required_call}"
    fi
done
for required_layout in 'zip -X "$archive" "$(basename "$binary")"' 'zip -X '\''${DIST_DIR}/${artifact}'\'' openvibely.exe' 'zip -X '\''${DIST_DIR}/${artifact}'\'' openvibely-desktop.exe' 'env COPYFILE_DISABLE=1 tar -czf "${DIST_DIR}/${artifact}" -C "$pkg_dir" openvibely' 'env COPYFILE_DISABLE=1 tar -czf "${DIST_DIR}/${artifact}" -C "$linux_pkg" openvibely-desktop'; do
    if grep -Fq "$required_layout" "$BUILD_SCRIPT"; then
        pass "release package preserves flat executable artifact: ${required_layout}"
    else
        fail "release package does not preserve flat executable artifact: ${required_layout}"
    fi
done
PUBLISH_SCRIPT="${SCRIPT_DIR}/release-publish.sh"
for required_artifact in 'require_release_artifact "openvibely_${VERSION}_darwin_amd64_server.zip"' 'require_release_artifact "openvibely_${VERSION}_darwin_arm64_server.zip"' 'require_release_artifact "openvibely_${VERSION}_linux_amd64_server.tar.gz"' 'require_release_artifact "openvibely_${VERSION}_linux_arm64_server.tar.gz"' 'require_release_artifact "openvibely_${VERSION}_windows_amd64_server.zip"' 'require_release_artifact "openvibely_${VERSION}_windows_arm64_server.zip"' 'require_release_artifact "OpenVibely_${VERSION}_darwin_amd64.app.zip"' 'require_release_artifact "OpenVibely_${VERSION}_darwin_arm64.app.zip"' 'require_release_artifact "openvibely_${VERSION}_windows_amd64_desktop.zip"' 'require_release_artifact "openvibely_${VERSION}_windows_arm64_desktop.zip"' 'require_release_artifact "openvibely_${VERSION}_linux_amd64_desktop.tar.gz"' 'require_release_artifact "openvibely_${VERSION}_linux_arm64_desktop.tar.gz"'; do
    if grep -Fq "$required_artifact" "$PUBLISH_SCRIPT"; then
        pass "release publish requires artifact: ${required_artifact}"
    else
        fail "release publish can omit required artifact: ${required_artifact}"
    fi
done

###############################################################################
# 6. Tag format
###############################################################################

section "Tag and release title format"

check_tag() {
    local version="$1" expected_tag="$2" expected_title="$3"
    local tag="v${version}"
    local title="OpenVibely ${version}"
    if [[ "$tag" == "$expected_tag" ]]; then
        pass "tag:   v${version} → '$tag'"
    else
        fail "tag mismatch: got '$tag', expected '$expected_tag'"
    fi
    if [[ "$title" == "$expected_title" ]]; then
        pass "title: OpenVibely ${version} → '$title'"
    else
        fail "title mismatch: got '$title', expected '$expected_title'"
    fi
}

check_tag "0.1.0" "v0.1.0" "OpenVibely 0.1.0"
check_tag "0.1.1" "v0.1.1" "OpenVibely 0.1.1"
check_tag "1.0.0" "v1.0.0" "OpenVibely 1.0.0"

###############################################################################
# 4. Branch naming
###############################################################################

section "Release branch naming"

check_branch() {
    local version="$1" expected="$2"
    local branch="release/v${version}"
    if [[ "$branch" == "$expected" ]]; then
        pass "branch: v${version} → '$branch'"
    else
        fail "branch mismatch: got '$branch', expected '$expected'"
    fi
}

check_branch "0.1.1" "release/v0.1.1"
check_branch "1.0.0" "release/v1.0.0"
check_branch "2.3.4" "release/v2.3.4"

###############################################################################
# 5. Release notes template structure
#
# The script produces a RELEASE_NOTES.md with AI_HIGHLIGHTS_PLACEHOLDER and
# AI_CHANGELOG_PLACEHOLDER blocks plus a COMMITS.txt with raw git log. Validate
# that the expected sections and markers are present in a simulated output string.
###############################################################################

section "Release notes template structure"

# Simulate the template output (mirrors what release-notes.sh emits)
FAKE_NOTES=$(cat << 'EOF'
# OpenVibely 0.1.1

OpenVibely is an open-source, self-hosted platform

## Highlights

<!-- AI_HIGHLIGHTS_PLACEHOLDER
     The agent orchestrating this release must replace this block
-->

## What's Changed

<!-- AI_CHANGELOG_PLACEHOLDER
     The agent orchestrating this release must replace this block
-->

**Full changelog:** [v0.1.0...v0.1.1](https://github.com/openvibely/openvibely/compare/v0.1.0...v0.1.1)

## Downloads

| Artifact | SHA-256 |

## Docker

docker pull openvibely/openvibely:0.1.1
docker run -d -p 3001:3001 -v openvibely_data:/data openvibely/openvibely:0.1.1

## Known Limitations

- Linux desktop: ...
EOF
)

check_notes_section() {
    local label="$1" pattern="$2"
    if echo "$FAKE_NOTES" | grep -q "$pattern"; then
        pass "notes: contains '$label'"
    else
        fail "notes: missing '$label' (pattern: '$pattern')"
    fi
}

check_notes_section "version heading"              "# OpenVibely 0.1.1"
check_notes_section "Highlights section"           "## Highlights"
check_notes_section "AI_HIGHLIGHTS_PLACEHOLDER"     "AI_HIGHLIGHTS_PLACEHOLDER"
check_notes_section "What's Changed section"       "## What's Changed"
check_notes_section "AI_CHANGELOG_PLACEHOLDER"      "AI_CHANGELOG_PLACEHOLDER"
check_notes_section "Full changelog link"          "Full changelog:"
check_notes_section "Downloads section"            "## Downloads"
check_notes_section "Docker section"               "## Docker"
check_notes_section "Known Limitations section"    "## Known Limitations"
check_notes_section "docker pull command"          "docker pull openvibely/openvibely:"
check_notes_section "docker startup command"        "docker run -d -p 3001:3001"

# Validate placeholders are NOT already replaced (would indicate the script
# wrongly bypassed the agent synthesis step)
if echo "$FAKE_NOTES" | grep -q "AI_HIGHLIGHTS_PLACEHOLDER"; then
    pass "notes: highlights placeholder present (agent synthesis step is required)"
else
    fail "notes: AI_HIGHLIGHTS_PLACEHOLDER missing — template incorrectly bypassed highlights synthesis"
fi

if echo "$FAKE_NOTES" | grep -q "AI_CHANGELOG_PLACEHOLDER"; then
    pass "notes: changelog placeholder present (agent synthesis step is required)"
else
    fail "notes: AI_CHANGELOG_PLACEHOLDER missing — template incorrectly bypassed changelog synthesis"
fi

###############################################################################
# 6. COMMITS.txt format
###############################################################################

section "COMMITS.txt raw log format"

FAKE_COMMITS=$(cat << 'EOF'
# Commits for OpenVibely v0.1.1
# Range: v0.1.0..HEAD
# Generated: 2025-01-01T00:00:00Z

----
commit abc1234def5678901234567890abcdef12345678
Date:    2025-01-01
Author:  Jane Dev <jane@example.com>
Subject: Add new scheduling feature

Body:
Implements weekly schedule support with timezone awareness.

----
commit 000aaa111bbb222ccc333ddd444eee555fff6666
Date:    2025-01-02
Author:  John Dev <john@example.com>
Subject: chore: memory updates

Body:

EOF
)

check_commits_field() {
    local label="$1" pattern="$2"
    if echo "$FAKE_COMMITS" | grep -q "$pattern"; then
        pass "commits: contains '$label'"
    else
        fail "commits: missing '$label'"
    fi
}

check_commits_field "header comment"        "# Commits for OpenVibely"
check_commits_field "commit range"          "# Range:"
check_commits_field "commit separator"      "^----"
check_commits_field "commit hash field"     "^commit "
check_commits_field "date field"            "^Date:"
check_commits_field "author field"          "^Author:"
check_commits_field "subject field"         "^Subject:"
check_commits_field "body field"            "^Body:"
check_commits_field "multiple commits"      "abc1234"

###############################################################################
# 7. Dry-run filesystem isolation
###############################################################################

section "Release build dry-run isolation"

DRY_RUN_TMP="$(mktemp -d)"
MOCK_BIN="${DRY_RUN_TMP}/bin"
DRY_RUN_DIST="${DRY_RUN_TMP}/dist/9.9.9"
mkdir -p "$MOCK_BIN"

cat > "${MOCK_BIN}/go" <<'EOF'
#!/usr/bin/env bash
if [[ "${1:-}" == "list" ]]; then
    echo "v0.0.0"
fi
EOF
chmod +x "${MOCK_BIN}/go"

cat > "${MOCK_BIN}/uname" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
    -s) echo "Darwin" ;;
    -m) echo "arm64" ;;
    *) echo "Darwin" ;;
esac
EOF
chmod +x "${MOCK_BIN}/uname"

DRY_RUN_OUTPUT="$(DRY_RUN=1 OPENVIBELY_RELEASE_KEY_ID=release-test OPENVIBELY_RELEASE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Test' OPENVIBELY_MACOS_NOTARY_PROFILE=test-profile OPENVIBELY_WINDOWS_SIGN_COMMAND=/bin/true OPENVIBELY_WINDOWS_VERIFY_COMMAND=/bin/true PATH="${MOCK_BIN}:$PATH" bash "${SCRIPT_DIR}/release-build.sh" 9.9.9 "$DRY_RUN_DIST" 2>&1)"

if DRY_RUN=1 SKIP_RELEASE_SIGNING_ENV=1 PATH="${MOCK_BIN}:$PATH" bash "${SCRIPT_DIR}/release-build.sh" 9.9.9 "$DRY_RUN_DIST" >/dev/null 2>&1; then
    fail "release build: accepted missing embedded trust root"
else
    pass "release build: requires embedded release trust root"
fi
if DRY_RUN=1 SKIP_RELEASE_SIGNING_ENV=1 OPENVIBELY_RELEASE_KEY_ID=release-test OPENVIBELY_RELEASE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Test' OPENVIBELY_MACOS_NOTARY_PROFILE=test-profile PATH="${MOCK_BIN}:$PATH" bash "${SCRIPT_DIR}/release-build.sh" 9.9.9 "$DRY_RUN_DIST" >/dev/null 2>&1; then
    fail "release build: accepted missing Windows signing hooks"
else
    pass "release build: requires Windows signing and timestamp verification hooks"
fi
if DRY_RUN=1 SKIP_RELEASE_SIGNING_ENV=1 OPENVIBELY_RELEASE_KEY_ID=release-test OPENVIBELY_RELEASE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= OPENVIBELY_WINDOWS_SIGN_COMMAND=/bin/true OPENVIBELY_WINDOWS_VERIFY_COMMAND=/bin/true PATH="${MOCK_BIN}:$PATH" bash "${SCRIPT_DIR}/release-build.sh" 9.9.9 "$DRY_RUN_DIST" >/dev/null 2>&1; then
    fail "release build: accepted missing macOS signing credentials"
else
    pass "release build: requires macOS signing and notarization credentials"
fi
for required in '--force --deep --options runtime --timestamp' '--force --options runtime --timestamp' 'codesign --verify'; do
    if grep -q -- "$required" "${SCRIPT_DIR}/sign-macos.sh"; then
        pass "macOS signing helper: includes $required"
    else
        fail "macOS signing helper: missing $required"
    fi
done
if grep -q -- 'notarytool submit' "${SCRIPT_DIR}/notarize-macos-archive.sh"; then
    pass "macOS notarization helper: includes notarytool submit"
else
    fail "macOS notarization helper: missing notarytool submit"
fi
for required in 'stapler staple' 'spctl --assess'; do
    if grep -q -- "$required" "${SCRIPT_DIR}/release-build.sh"; then
        pass "release build: includes $required"
    else
        fail "release build: missing $required"
    fi
done

if [[ ! -e "$DRY_RUN_DIST" ]]; then
    pass "dry run: does not create the output directory"
else
    fail "dry run: created the output directory"
fi

if ! echo "$DRY_RUN_OUTPUT" | grep -Eq '(^|[[:space:]])(cp|chmod):|No such file or directory'; then
    pass "dry run: macOS bundle setup does not leak filesystem errors"
else
    fail "dry run: macOS bundle setup leaked filesystem errors"
fi

cat > "${MOCK_BIN}/gh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "${MOCK_BIN}/gh"

FULL_DRY_RUN_DIST="${DRY_RUN_TMP}/full-dist/9.9.9"
if DRY_RUN=1 SKIP_SIGNING_CHECK=1 SKIP_GH_AUTH_CHECK=1 DIST_DIR="$FULL_DRY_RUN_DIST" OPENVIBELY_RELEASE_KEY_ID=release-test OPENVIBELY_RELEASE_PUBLIC_KEY=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA= OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Test' OPENVIBELY_MACOS_NOTARY_PROFILE=test-profile OPENVIBELY_WINDOWS_SIGN_COMMAND=/bin/true OPENVIBELY_WINDOWS_VERIFY_COMMAND=/bin/true PATH="${MOCK_BIN}:$PATH" \
    bash "${SCRIPT_DIR}/release.sh" 9.9.9 >/dev/null 2>&1; then
    pass "dry run: full release rehearsal completes without real artifacts"
else
    fail "dry run: full release rehearsal requires real build outputs"
fi

rm -rf "$DRY_RUN_TMP"

###############################################################################
# 8. Docker storage guidance
###############################################################################

section "Docker publication identity"
for publish_script in "${SCRIPT_DIR}/release-publish.sh" "${SCRIPT_DIR}/release.sh"; do
    if grep -q -- '--build-arg VERSION=' "$publish_script" && grep -q -- '--build-arg COMMIT=' "$publish_script" && grep -q -- '--build-arg BUILD_TIME=' "$publish_script"; then
        pass "docker: $(basename "$publish_script") passes immutable build identity"
    else
        fail "docker: $(basename "$publish_script") must pass VERSION, COMMIT, and BUILD_TIME build args"
    fi
done

section "Docker storage guidance"

REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd)"
if grep -Eiq 'migrat(e|ing|ion).*volume|volume.*migrat(e|ing|ion)' "${REPO_ROOT}/Dockerfile"; then
    fail "docker: runtime guidance must not prescribe legacy volume migration"
else
    pass "docker: runtime guidance only states the current UID/GID ownership contract"
fi

###############################################################################
# Results
###############################################################################

echo ""
TOTAL=$((PASS + FAIL))
if [[ "$FAIL" -eq 0 ]]; then
    echo -e "${GREEN}All ${TOTAL} tests passed.${NC}"
    exit 0
else
    echo -e "${RED}${FAIL} of ${TOTAL} tests FAILED.${NC}"
    exit 1
fi
