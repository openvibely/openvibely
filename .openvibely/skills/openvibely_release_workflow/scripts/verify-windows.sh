#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <windows-exe>" >&2
    exit 2
fi

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

if [[ -x "$OSSL_SIGNCODE" ]] || command -v osslsigncode >/dev/null 2>&1; then
    if [[ ! -x "$OSSL_SIGNCODE" ]]; then
        OSSL_SIGNCODE="$(command -v osslsigncode)"
    fi
    set +e
    output="$("$OSSL_SIGNCODE" verify -in "$1" 2>&1)"
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
