#!/usr/bin/env bash
set -euo pipefail

SETUP=0
NO_INTERACTIVE=0
INSTALL_TOOLING=1
if [[ "${1:-}" == "--setup" ]]; then
    SETUP=1
elif [[ "${1:-}" == "--no-interactive" ]]; then
    SETUP=1
    NO_INTERACTIVE=1
    INSTALL_TOOLING=0
elif [[ $# -gt 0 ]]; then
    echo "usage: $0 [--setup|--no-interactive]" >&2
    exit 2
fi

missing=0
placeholder=0
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null | tail -n 1 || true)"
if [[ -z "$REPO_ROOT" ]]; then
    REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd -P)"
fi
LOCAL_ENV="${REPO_ROOT}/.release-signing.env"

write_env_template() {
    if [[ -f "$LOCAL_ENV" ]]; then
        echo "local env exists: $LOCAL_ENV"
        return 0
    fi
    cat > "$LOCAL_ENV" <<EOF
# Local release signing configuration.
# Do not commit this file.

OPENVIBELY_RELEASE_KEY_ID='openvibely-release-1'
OPENVIBELY_RELEASE_PUBLIC_KEY='<base64-ed25519-public-key>'

OPENVIBELY_MACOS_SIGN_IDENTITY='Developer ID Application: Your Name or Company (TEAMID)'
OPENVIBELY_MACOS_NOTARY_PROFILE='openvibely-notary'

OPENVIBELY_AZURE_SIGNING_ENDPOINT='<azure-region>.codesigning.azure.net'
OPENVIBELY_AZURE_SIGNING_ACCOUNT='<artifact-signing-account-name>'
OPENVIBELY_AZURE_SIGNING_PROFILE='<certificate-profile-name>'
OPENVIBELY_AZURE_SUBSCRIPTION_ID='<optional-azure-subscription-id>'
OPENVIBELY_WINDOWS_SIGN_COMMAND="${REPO_ROOT}/.openvibely/skills/openvibely_release_workflow/scripts/sign-windows.sh"
OPENVIBELY_WINDOWS_VERIFY_COMMAND="${REPO_ROOT}/.openvibely/skills/openvibely_release_workflow/scripts/verify-windows.sh"

OPENVIBELY_LINUX_DESKTOP_BINARY='<path-to-linux-amd64-openvibely-desktop>'
OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY='<path-to-linux-arm64-openvibely-desktop>'
EOF
    chmod 600 "$LOCAL_ENV"
    echo "created local env template: $LOCAL_ENV"
}

load_release_env_defaults() {
    local env_file="$1"
    local had_key=0 had_pub=0 had_mac_id=0 had_notary=0 had_win_sign=0 had_win_verify=0 had_azure_endpoint=0 had_azure_account=0 had_azure_profile=0 had_azure_sub=0 had_linux_desktop=0 had_linux_arm64_desktop=0
    local saved_key="" saved_pub="" saved_mac_id="" saved_notary="" saved_win_sign="" saved_win_verify="" saved_azure_endpoint="" saved_azure_account="" saved_azure_profile="" saved_azure_sub="" saved_linux_desktop="" saved_linux_arm64_desktop=""

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
    [[ "$had_linux_desktop" == "1" ]] && OPENVIBELY_LINUX_DESKTOP_BINARY="$saved_linux_desktop"
    [[ "$had_linux_arm64_desktop" == "1" ]] && OPENVIBELY_LINUX_ARM64_DESKTOP_BINARY="$saved_linux_arm64_desktop"
    return 0
}

if [[ "$SETUP" == "1" ]]; then
    write_env_template
fi

if [[ "${SKIP_RELEASE_SIGNING_ENV:-0}" != "1" && -f "$LOCAL_ENV" ]]; then
    load_release_env_defaults "$LOCAL_ENV"
    echo "loaded local env: $LOCAL_ENV"
fi

check_env() {
    local name="$1"
    if [[ -z "${!name:-}" ]]; then
        echo "missing env: $name"
        missing=1
    elif [[ "${!name}" == *"<"*">"* || "${!name}" == *"Your Name or Company"* ]]; then
        echo "placeholder env: $name"
        missing=1
        placeholder=1
    else
        echo "ok env: $name"
    fi
}

check_cmd() {
    local name="$1"
    if ! command -v "$name" >/dev/null 2>&1; then
        echo "missing tool: $name"
        missing=1
    else
        echo "ok tool: $name ($(command -v "$name"))"
    fi
}

tool_runs() {
    local tool="$1"
    TOOL="$tool" bash -c '"$TOOL" --help >/dev/null 2>&1' 2>/dev/null
}

check_osslsigncode() {
    if ! command -v osslsigncode >/dev/null 2>&1; then
        if [[ "$INSTALL_TOOLING" == "0" ]]; then
            echo "missing tool: osslsigncode (will be installed during non-dry-run setup)"
        else
            echo "missing tool: osslsigncode"
        fi
        missing=1
        return
    fi
    local tool_path
    tool_path="$(command -v osslsigncode)"
    if tool_runs "$tool_path"; then
        echo "ok tool: osslsigncode ($(command -v osslsigncode))"
    else
        echo "broken tool: osslsigncode ($(command -v osslsigncode))"
        echo "osslsigncode exists but cannot run on this machine."
        missing=1
    fi
}

check_cmd xcrun
if xcrun --find notarytool >/dev/null 2>&1; then
    echo "ok tool: notarytool ($(xcrun --find notarytool))"
else
    echo "missing tool: notarytool"
    missing=1
fi

check_env OPENVIBELY_MACOS_SIGN_IDENTITY
if [[ -z "${OPENVIBELY_MACOS_NOTARY_PROFILE:-}" ]]; then
    if [[ "$SETUP" == "1" ]]; then
        export OPENVIBELY_MACOS_NOTARY_PROFILE="openvibely-notary"
        echo "OPENVIBELY_MACOS_NOTARY_PROFILE unset; using default: $OPENVIBELY_MACOS_NOTARY_PROFILE"
    else
        echo "missing env: OPENVIBELY_MACOS_NOTARY_PROFILE"
        missing=1
    fi
else
    if [[ "$OPENVIBELY_MACOS_NOTARY_PROFILE" == *"<"*">"* ]]; then
        echo "placeholder env: OPENVIBELY_MACOS_NOTARY_PROFILE"
        missing=1
        placeholder=1
    else
        echo "ok env: OPENVIBELY_MACOS_NOTARY_PROFILE"
    fi
fi
check_env OPENVIBELY_RELEASE_KEY_ID
check_env OPENVIBELY_RELEASE_PUBLIC_KEY
check_env OPENVIBELY_WINDOWS_SIGN_COMMAND
check_env OPENVIBELY_WINDOWS_VERIFY_COMMAND
check_env OPENVIBELY_AZURE_SIGNING_ENDPOINT
check_env OPENVIBELY_AZURE_SIGNING_ACCOUNT
check_env OPENVIBELY_AZURE_SIGNING_PROFILE

for hook_var in OPENVIBELY_WINDOWS_SIGN_COMMAND OPENVIBELY_WINDOWS_VERIFY_COMMAND; do
    hook_path="${!hook_var:-}"
    if [[ -n "$hook_path" && "$hook_path" != *"<"*">"* ]]; then
        if [[ -x "$hook_path" ]]; then
            echo "ok executable: $hook_var"
        else
            echo "missing executable: $hook_var=$hook_path"
            missing=1
        fi
    fi
done

if [[ "$INSTALL_TOOLING" == "1" ]]; then
    if ! "${SCRIPT_DIR}/ensure-release-tooling.sh"; then
        missing=1
    fi
fi
export PATH="${REPO_ROOT}/.tools/osslsigncode/bin:${PATH}"
export PATH="${REPO_ROOT}/.tools/jsign/bin:${PATH}"
export PATH="${REPO_ROOT}/.tools/azure-cli/bin:${PATH}"
export PATH="${REPO_ROOT}/.tools/wails3/bin:${PATH}"
check_osslsigncode
if [[ -n "${OPENVIBELY_JAVA_BIN:-}" && -x "$OPENVIBELY_JAVA_BIN" ]]; then
    echo "ok tool: java ($OPENVIBELY_JAVA_BIN)"
elif [[ -x "${REPO_ROOT}/.tools/jre/Contents/Home/bin/java" ]]; then
    echo "ok tool: java (${REPO_ROOT}/.tools/jre/Contents/Home/bin/java)"
elif command -v java >/dev/null 2>&1 && java -version >/dev/null 2>&1; then
    echo "ok tool: java ($(command -v java))"
else
    echo "missing/broken tool: java"
    missing=1
fi
check_cmd jsign
check_cmd wails3
if [[ -z "${AZURE_ACCESS_TOKEN:-}" ]]; then
    check_cmd az
else
    echo "ok env: AZURE_ACCESS_TOKEN"
fi

if [[ -n "${OPENVIBELY_MACOS_SIGN_IDENTITY:-}" && "$OPENVIBELY_MACOS_SIGN_IDENTITY" != *"<"*">"* && "$OPENVIBELY_MACOS_SIGN_IDENTITY" != *"Your Name or Company"* ]]; then
    if security find-identity -v -p codesigning | grep -F "$OPENVIBELY_MACOS_SIGN_IDENTITY" >/dev/null; then
        echo "ok certificate: $OPENVIBELY_MACOS_SIGN_IDENTITY"
    else
        echo "missing certificate in keychain: $OPENVIBELY_MACOS_SIGN_IDENTITY"
        missing=1
    fi
fi

if [[ "$placeholder" == "1" ]]; then
    echo "fill placeholders in: $LOCAL_ENV"
elif [[ -n "${OPENVIBELY_MACOS_NOTARY_PROFILE:-}" && "$OPENVIBELY_MACOS_NOTARY_PROFILE" != *"<"*">"* ]]; then
    if xcrun notarytool history --keychain-profile "$OPENVIBELY_MACOS_NOTARY_PROFILE" >/dev/null 2>&1; then
        echo "ok notary profile: $OPENVIBELY_MACOS_NOTARY_PROFILE"
    elif [[ "$SETUP" == "1" && "$NO_INTERACTIVE" != "1" ]]; then
        echo "notary profile missing or invalid; starting interactive setup..."
        if ! xcrun notarytool store-credentials "$OPENVIBELY_MACOS_NOTARY_PROFILE"; then
            echo "notary profile setup failed"
            missing=1
        fi
        if xcrun notarytool history --keychain-profile "$OPENVIBELY_MACOS_NOTARY_PROFILE" >/dev/null 2>&1; then
            echo "ok notary profile: $OPENVIBELY_MACOS_NOTARY_PROFILE"
        else
            echo "notary profile still failed verification: $OPENVIBELY_MACOS_NOTARY_PROFILE"
            missing=1
        fi
    else
        echo "missing/invalid notary profile: $OPENVIBELY_MACOS_NOTARY_PROFILE"
        echo "run: $0 --setup"
        missing=1
    fi
fi

exit "$missing"
