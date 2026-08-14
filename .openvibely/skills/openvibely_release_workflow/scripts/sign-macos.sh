#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <macos-binary-or-app>" >&2
    exit 2
fi

target="$1"

: "${OPENVIBELY_MACOS_SIGN_IDENTITY:?set OPENVIBELY_MACOS_SIGN_IDENTITY to the Developer ID Application identity}"

if [[ ! -e "$target" ]]; then
    echo "target not found: $target" >&2
    exit 1
fi

args=(--force --options runtime --timestamp --sign "$OPENVIBELY_MACOS_SIGN_IDENTITY")
if [[ -d "$target" ]]; then
    args=(--force --deep --options runtime --timestamp --sign "$OPENVIBELY_MACOS_SIGN_IDENTITY")
fi

codesign "${args[@]}" "$target"
if [[ -d "$target" ]]; then
    codesign --verify --deep --strict --verbose=2 "$target"
else
    codesign --verify --strict --verbose=2 "$target"
fi
