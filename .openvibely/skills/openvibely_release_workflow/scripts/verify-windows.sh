#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <windows-exe>" >&2
    exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SKIP_AZURE_SIGNING_TOOLING=1 "${SCRIPT_DIR}/ensure-release-tooling.sh"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null | tail -n 1 || true)"
if [[ -z "$REPO_ROOT" ]]; then
    REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd -P)"
fi
export PATH="${REPO_ROOT}/.tools/osslsigncode/bin:${PATH}"
if [[ -d "${REPO_ROOT}/.tools/osslsigncode/lib/ossl-modules" ]]; then
    export OPENSSL_MODULES="${REPO_ROOT}/.tools/osslsigncode/lib/ossl-modules"
fi
OSSL_SIGNCODE="${REPO_ROOT}/.tools/osslsigncode/bin/osslsigncode"

ensure_microsoft_trusted_signing_bundle() {
    local bundle="${OPENVIBELY_WINDOWS_VERIFY_CA_BUNDLE:-${REPO_ROOT}/.tools/microsoft-trusted-signing-ca.pem}"
    if [[ -f "$bundle" ]]; then
        printf '%s\n' "$bundle"
        return 0
    fi

    command -v curl >/dev/null 2>&1 || return 1
    command -v openssl >/dev/null 2>&1 || return 1

    local tmpdir
    tmpdir="$(mktemp -d)"
    trap 'rm -rf "$tmpdir"' RETURN

    download_cert() {
        local name="$1" url="$2" expected_sha1="$3"
        local der="${tmpdir}/${name}.crt"
        local pem="${tmpdir}/${name}.pem"
        local actual_sha1

        curl -fsSL "$url" -o "$der"
        actual_sha1="$(openssl x509 -inform DER -in "$der" -noout -fingerprint -sha1 | awk -F= '{print $2}' | tr -d ':' | tr '[:upper:]' '[:lower:]')"
        if [[ "$actual_sha1" != "$expected_sha1" ]]; then
            echo "certificate fingerprint mismatch for $name: expected $expected_sha1, got $actual_sha1" >&2
            return 1
        fi
        openssl x509 -inform DER -in "$der" -out "$pem"
        cat "$pem"
    }

    mkdir -p "$(dirname "$bundle")"
    {
        download_cert \
            "microsoft-identity-verification-root-ca-2020" \
            "https://www.microsoft.com/pkiops/certs/microsoft%20identity%20verification%20root%20certificate%20authority%202020.crt" \
            "f40042e2e5f7e8ef8189fed15519aece42c3bfa2"
        download_cert \
            "microsoft-public-rsa-timestamping-ca-2020" \
            "https://www.microsoft.com/pkiops/certs/Microsoft%20Public%20RSA%20Timestamping%20CA%202020.crt" \
            "27f0abac2877ba255f62b389b43ff539a0fb598e"
    } > "${bundle}.tmp"
    mv "${bundle}.tmp" "$bundle"
    printf '%s\n' "$bundle"
}

if [[ -x "$OSSL_SIGNCODE" ]] || command -v osslsigncode >/dev/null 2>&1; then
    if [[ ! -x "$OSSL_SIGNCODE" ]]; then
        OSSL_SIGNCODE="$(command -v osslsigncode)"
    fi
    verify_args=(verify -in "$1")
    if ca_bundle="$(ensure_microsoft_trusted_signing_bundle)"; then
        verify_args+=(-CAfile "$ca_bundle" -TSA-CAfile "$ca_bundle")
    else
        echo "warning: Microsoft Trusted Signing CA bundle unavailable; using osslsigncode default trust store" >&2
    fi
    set +e
    output="$("$OSSL_SIGNCODE" "${verify_args[@]}" 2>&1)"
    status=$?
    set -e
    printf '%s\n' "$output"
    if [[ $status -ne 0 ]] || grep -Eq '(^|[[:space:]])Failed($|[[:space:]])' <<< "$output" || ! grep -Eq 'Succeeded|Number of verified signatures:[[:space:]]*[1-9]' <<< "$output"; then
        exit 1
    fi
    if ! grep -Eiq 'timestamp|time stamp|time-stamp' <<< "$output" || grep -Eiq 'not timestamped|no timestamp|without timestamp' <<< "$output"; then
        echo "missing Authenticode timestamp" >&2
        exit 1
    fi
else
    echo "osslsigncode is unavailable after tooling setup" >&2
    exit 127
fi
