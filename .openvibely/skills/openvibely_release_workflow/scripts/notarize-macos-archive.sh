#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
    echo "usage: $0 <zip-archive>" >&2
    exit 2
fi

archive="$1"

: "${OPENVIBELY_MACOS_NOTARY_PROFILE:?set OPENVIBELY_MACOS_NOTARY_PROFILE to the notarytool profile name}"

if [[ ! -f "$archive" ]]; then
    echo "archive not found: $archive" >&2
    exit 1
fi

xcrun notarytool submit "$archive" --keychain-profile "$OPENVIBELY_MACOS_NOTARY_PROFILE" --wait
