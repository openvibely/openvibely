#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <windows-exe>" >&2
    exit 2
fi

input="$1"
tmp="${input}.signed"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"${SCRIPT_DIR}/ensure-release-tooling.sh"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null | tail -n 1 || true)"
if [[ -z "$REPO_ROOT" ]]; then
    REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd -P)"
fi
export PATH="${REPO_ROOT}/.tools/osslsigncode/bin:${PATH}"
if [[ -d "${REPO_ROOT}/.tools/osslsigncode/lib/ossl-modules" ]]; then
    export OPENSSL_MODULES="${REPO_ROOT}/.tools/osslsigncode/lib/ossl-modules"
fi
OSSL_SIGNCODE="${REPO_ROOT}/.tools/osslsigncode/bin/osslsigncode"

: "${WINDOWS_CERT_P12:?set WINDOWS_CERT_P12 to the .p12/.pfx code-signing certificate path}"
: "${WINDOWS_CERT_PASSWORD:?set WINDOWS_CERT_PASSWORD to the certificate password}"

if [[ ! -f "$input" ]]; then
    echo "input file not found: $input" >&2
    exit 1
fi
if [[ ! -f "$WINDOWS_CERT_P12" ]]; then
    echo "certificate file not found: $WINDOWS_CERT_P12" >&2
    exit 1
fi

pass_file="$(mktemp)"
chmod 600 "$pass_file"
printf '%s\n' "$WINDOWS_CERT_PASSWORD" > "$pass_file"
unset WINDOWS_CERT_PASSWORD
trap 'rm -f "$pass_file" "$tmp"' EXIT

sign_args=(
    sign
    -pkcs12 "$WINDOWS_CERT_P12"
    -readpass "$pass_file"
    -n "OpenVibely"
    -i "https://openvibely.ai"
    -h sha256
    -ts "http://timestamp.digicert.com"
    -in "$input"
    -out "$tmp"
)

rm -f "$tmp"
if [[ -x "$OSSL_SIGNCODE" ]]; then
    "$OSSL_SIGNCODE" "${sign_args[@]}"
elif command -v osslsigncode >/dev/null 2>&1; then
    osslsigncode "${sign_args[@]}"
else
    echo "osslsigncode is unavailable after tooling setup" >&2
    exit 127
fi
mv "$tmp" "$input"
