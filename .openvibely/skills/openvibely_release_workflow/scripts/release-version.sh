#!/usr/bin/env bash
# Side-effect-free normalization and validation policy for release versions.

normalize_release_version() {
    local raw="${1:-}"
    printf '%s\n' "${raw#v}"
}

is_valid_release_version() {
    local version="${1:-}"
    [[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]
}
