#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <windows-exe>" >&2
    exit 2
fi

input="$1"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
"${SCRIPT_DIR}/ensure-release-tooling.sh"
REPO_ROOT="$(git -C "$SCRIPT_DIR" rev-parse --show-toplevel 2>/dev/null | tail -n 1 || true)"
if [[ -z "$REPO_ROOT" ]]; then
    REPO_ROOT="$(cd "${SCRIPT_DIR}/../../../.." && pwd -P)"
fi
export PATH="${REPO_ROOT}/.tools/jsign/bin:${PATH}"
export PATH="${REPO_ROOT}/.tools/azure-cli/bin:${PATH}"

: "${OPENVIBELY_AZURE_SIGNING_ENDPOINT:?set OPENVIBELY_AZURE_SIGNING_ENDPOINT to the Azure Artifact Signing endpoint, for example eus.codesigning.azure.net}"
: "${OPENVIBELY_AZURE_SIGNING_ACCOUNT:?set OPENVIBELY_AZURE_SIGNING_ACCOUNT to the Azure Artifact Signing account name}"
: "${OPENVIBELY_AZURE_SIGNING_PROFILE:?set OPENVIBELY_AZURE_SIGNING_PROFILE to the Azure Artifact Signing certificate profile name}"

if [[ ! -f "$input" ]]; then
    echo "input file not found: $input" >&2
    exit 1
fi

java_bin="${OPENVIBELY_JAVA_BIN:-}"
if [[ -z "$java_bin" && -x "${REPO_ROOT}/.tools/jre/Contents/Home/bin/java" ]]; then
    java_bin="${REPO_ROOT}/.tools/jre/Contents/Home/bin/java"
fi
if [[ -z "$java_bin" ]] && command -v java >/dev/null 2>&1; then
    java_bin="$(command -v java)"
fi
if [[ -z "$java_bin" ]]; then
    echo "java is required for jsign" >&2
    exit 127
fi
if ! "$java_bin" -version >/dev/null 2>&1; then
    echo "java is installed but no runnable Java runtime is available" >&2
    exit 127
fi
export OPENVIBELY_JAVA_BIN="$java_bin"
if [[ -x "${REPO_ROOT}/.tools/python/bin/python3" && -z "${AZ_PYTHON:-}" ]]; then
    export AZ_PYTHON="${REPO_ROOT}/.tools/python/bin/python3"
fi
if ! command -v jsign >/dev/null 2>&1; then
    echo "jsign is unavailable after tooling setup" >&2
    exit 127
fi

endpoint="${OPENVIBELY_AZURE_SIGNING_ENDPOINT#https://}"
endpoint="${endpoint#http://}"
endpoint="${endpoint%/}"
alias_name="${OPENVIBELY_AZURE_SIGNING_ACCOUNT}/${OPENVIBELY_AZURE_SIGNING_PROFILE}"

token="${AZURE_ACCESS_TOKEN:-}"
if [[ -z "$token" ]]; then
    if ! command -v az >/dev/null 2>&1; then
        echo "az is required to fetch an Azure Artifact Signing access token unless AZURE_ACCESS_TOKEN is already set; run ensure-release-tooling.sh or install Azure CLI" >&2
        exit 127
    fi
    if [[ -n "${OPENVIBELY_AZURE_SUBSCRIPTION_ID:-}" ]]; then
        az account set --subscription "$OPENVIBELY_AZURE_SUBSCRIPTION_ID" >/dev/null
    fi
    token="$(az account get-access-token \
        --resource https://codesigning.azure.net \
        --query accessToken \
        --output tsv)"
fi

if [[ -z "$token" ]]; then
    echo "could not obtain Azure Artifact Signing access token" >&2
    exit 1
fi

jsign \
    --storetype TRUSTEDSIGNING \
    --keystore "$endpoint" \
    --storepass "$token" \
    --alias "$alias_name" \
    --tsaurl "http://timestamp.acs.microsoft.com/" \
    --tsmode RFC3161 \
    --replace \
    "$input"
