#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

SWAG_VERSION=${SWAG_VERSION:-$(go list -m -f '{{.Version}}' github.com/swaggo/swag)}
SWAGGER_FORCE=${SWAGGER_FORCE:-0}
STAMP="$ROOT/bin/.swagger-inputs"
LOCK="$STAMP.lock"
CURRENT_HEAD=$(git rev-parse HEAD)
STAMP_HEAD=
STAMP_VERSION=
STAMP_FINGERPRINT=
STAMP_PATHS=
STAMP_UNTRACKED_STATE=
STAMP_VALID=0
ANNOTATION_PATTERN='^[[:space:]]*//[[:space:]]+@[[:alpha:]]'
SWAGGER_OUTPUTS="docs/docs.go docs/swagger.json docs/swagger.yaml"

mkdir -p "$ROOT/bin"

cleanup() {
    status=$?
    rmdir "$LOCK" 2>/dev/null || true
    rm -f "$STAMP.$$"
    exit "$status"
}
trap cleanup EXIT INT TERM

while ! mkdir "$LOCK" 2>/dev/null; do
    sleep 0.1
done

refresh_path_state() {
    ALL_REPOSITORY_PATHS=$(git ls-files --cached --others --exclude-standard)
    SWAGGER_UNTRACKED_STATE=0
    for path in $(git ls-files --others --exclude-standard); do
        case "$path" in
            .swaggo|docs/docs.go|docs/swagger.json|docs/swagger.yaml|cmd/*.go|internal/*.go|pkg/*.go|web/*.go)
                SWAGGER_UNTRACKED_STATE=1
                ;;
        esac
    done
    ALL_GO_SOURCES=$(printf '%s\n' "$ALL_REPOSITORY_PATHS" | awk '$0 ~ /^(cmd|internal|pkg|web)\/.*\.go$/ { print }')
    SWAGGER_PATHS=$( {
        printf '%s\n' "$ALL_GO_SOURCES" | awk 'NF { print "go-source:" $0 }'
        printf '%s\n' "$ALL_REPOSITORY_PATHS" | awk '$0 == ".swaggo" { print "config:.swaggo" }'
    } | git hash-object --stdin )
}

refresh_inputs() {
    refresh_path_state

    HEAD_ANNOTATION_SOURCES=$(git grep -l -E "$ANNOTATION_PATTERN" HEAD -- cmd internal pkg web 2>/dev/null | sed 's#^HEAD:##' | awk '$0 ~ /\.go$/ { print }' || true)
    CURRENT_ANNOTATION_SOURCES=$(find cmd internal pkg web -type d -name '.*' -prune -o -type f -name '*.go' -exec grep -lE "$ANNOTATION_PATTERN" {} + | LC_ALL=C sort -u || true)
    SWAGGER_ANNOTATION_SOURCES=$(printf '%s\n%s\n' "$HEAD_ANNOTATION_SOURCES" "$CURRENT_ANNOTATION_SOURCES" | sed '/^$/d' | LC_ALL=C sort -u)

    SWAGGER_SCHEMA_SOURCES=$(printf '%s\n' "$ALL_GO_SOURCES" | awk '($0 ~ /^internal\/(models|viewmodels)\/.*\.go$/ || $0 ~ /^internal\/update\/.*\.go$/) && $0 !~ /_test\.go$/ { print }')
    # CoordinatorSnapshot reaches nested update-package types through
    # VerifiedRelease and DrainStatus, so complete non-test update files stay
    # in the schema fingerprint as that package evolves.
    SWAGGER_SCHEMA_SOURCES="$SWAGGER_SCHEMA_SOURCES
internal/repository/execution_repo.go"

    SWAGGER_CONFIG_SOURCES="go.mod go.sum"
    if printf '%s\n' "$ALL_REPOSITORY_PATHS" | grep -qx '.swaggo'; then
        SWAGGER_CONFIG_SOURCES="$SWAGGER_CONFIG_SOURCES .swaggo"
    fi
}

read_stamp() {
    STAMP_VALID=0
    if [ -f "$STAMP" ]; then
        set -- $(cat "$STAMP")
        if [ "$#" -eq 5 ]; then
            STAMP_HEAD=${1-}
            STAMP_VERSION=${2-}
            STAMP_FINGERPRINT=${3-}
            STAMP_PATHS=${4-}
            STAMP_UNTRACKED_STATE=${5-}
            STAMP_VALID=1
        fi
    fi
}

valid_digest() {
    digest=$1
    case "$digest" in
        ''|*[!0123456789abcdef]*) return 1 ;;
    esac
    [ "${#digest}" -eq 40 ]
}

is_schema_source() {
    case "$1" in
        internal/models/*.go|internal/viewmodels/*.go|internal/update/*.go)
            case "$1" in
                *_test.go) return 1 ;;
                *) return 0 ;;
            esac
            ;;
        internal/repository/execution_repo.go)
            return 0
            ;;
    esac
    return 1
}

has_annotation() {
    [ -f "$1" ] && grep -Eq "$ANNOTATION_PATTERN" "$1"
}

has_relevant_worktree_changes() {
    changed_paths=$( {
        git diff --name-only HEAD --
        git ls-files --others --exclude-standard
    } )
    for path in $changed_paths; do
        case "$path" in
            go.mod|go.sum|.swaggo|docs/docs.go|docs/swagger.json|docs/swagger.yaml)
                return 0
                ;;
        esac
        if is_schema_source "$path"; then
            return 0
        fi
        case "$path" in
            cmd/*.go|cmd/*/*.go|internal/*.go|internal/*/*.go|pkg/*.go|pkg/*/*.go|web/*.go|web/*/*.go)
                if has_annotation "$path" || git grep -q -E "$ANNOTATION_PATTERN" HEAD -- "$path" 2>/dev/null; then
                    return 0
                fi
                ;;
        esac
    done
    return 1
}

committed_generated_outputs_changed() {
    [ -n "$STAMP_HEAD" ] || return 1
    changed_paths=$(git diff --name-only "$STAMP_HEAD" "$CURRENT_HEAD" -- docs 2>/dev/null) || return 1
    for path in $changed_paths; do
        case "$path" in
            docs/docs.go|docs/swagger.json|docs/swagger.yaml)
                return 0
                ;;
        esac
    done
    return 1
}

append_file() {
    path=$1
    if [ -f "$path" ]; then
        cat "$path"
    else
        printf '%s' '<missing>'
    fi
    printf '\n--end-file--\n'
}

fingerprint() {
    {
        printf 'generator-version:%s\n' "$SWAG_VERSION"
        for source in $SWAGGER_ANNOTATION_SOURCES; do
            printf 'swagger-source:%s\n' "$source"
            append_file "$source"
        done
        for source in $SWAGGER_SCHEMA_SOURCES; do
            printf 'schema:%s\n' "$source"
            append_file "$source"
        done
        for source in $SWAGGER_CONFIG_SOURCES; do
            printf 'configuration:%s\n' "$source"
            append_file "$source"
        done
    } | git hash-object --stdin
}

outputs_are_complete() {
    for output in $SWAGGER_OUTPUTS; do
        test -f "$output" || return 1
    done
    return 0
}

tracked_inputs_are_clean() {
    paths="$SWAGGER_ANNOTATION_SOURCES $SWAGGER_SCHEMA_SOURCES $SWAGGER_CONFIG_SOURCES $SWAGGER_OUTPUTS"
    git ls-files --error-unmatch -- $paths >/dev/null 2>&1 || return 1
    git diff --quiet -- $paths || return 1
    git diff --cached --quiet -- $paths || return 1
    return 0
}

can_seed_stamp() {
    outputs_are_complete || return 1
    # Compare against HEAD before current input discovery so staged deletions
    # cannot disappear from the manifest and preserve stale documentation.
    has_relevant_worktree_changes && return 1
    tracked_inputs_are_clean || return 1
    return 0
}

write_stamp() {
    tmp="$STAMP.$$"
    {
        printf '%s\n' "$CURRENT_HEAD"
        printf '%s\n' "$SWAG_VERSION"
        printf '%s\n' "$CURRENT_FINGERPRINT"
        printf '%s\n' "$SWAGGER_PATHS"
        printf '%s\n' "$SWAGGER_UNTRACKED_STATE"
    } > "$tmp"
    mv "$tmp" "$STAMP"
}

regenerate() {
    go run "github.com/swaggo/swag/cmd/swag@$SWAG_VERSION" init -g cmd/server/main.go -o docs
    sed -i.bak '/LeftDelim:/d' docs/docs.go
    sed -i.bak '/RightDelim:/d' docs/docs.go
    rm -f docs/docs.go.bak

    for output in $SWAGGER_OUTPUTS; do
        test -f "$output" || {
            printf '%s\n' "swag did not generate expected output: $output" >&2
            return 1
        }
    done

    if [ -n "$SWAGGER_OUTPUTS" ]; then
        touch $SWAGGER_OUTPUTS
    fi
    write_stamp
}

regenerate_now() {
    refresh_inputs
    CURRENT_FINGERPRINT=$(fingerprint)
    regenerate
}

read_stamp
refresh_path_state

if [ "$SWAGGER_FORCE" = "1" ]; then
    regenerate_now
elif ! outputs_are_complete; then
    regenerate_now
elif [ ! -f "$STAMP" ]; then
    refresh_inputs
    CURRENT_FINGERPRINT=$(fingerprint)
    if can_seed_stamp; then
        write_stamp
    else
        regenerate
    fi
elif [ "$STAMP_VALID" = "1" ] &&
    [ "$STAMP_HEAD" = "$CURRENT_HEAD" ] &&
    [ "$STAMP_VERSION" = "$SWAG_VERSION" ] &&
    valid_digest "$STAMP_FINGERPRINT" &&
    valid_digest "$STAMP_PATHS"; then
    if [ "$STAMP_PATHS" != "$SWAGGER_PATHS" ]; then
        regenerate_now
    elif has_relevant_worktree_changes; then
        regenerate_now
    else
        for output in $SWAGGER_OUTPUTS; do
            if test "$output" -nt "$STAMP"; then
                regenerate_now
                exit $?
            fi
        done
    fi
else
    refresh_inputs
    CURRENT_FINGERPRINT=$(fingerprint)
    if [ "$STAMP_VERSION" != "$SWAG_VERSION" ]; then
        regenerate
    elif [ "$CURRENT_FINGERPRINT" = "$STAMP_FINGERPRINT" ] && [ "$SWAGGER_PATHS" = "$STAMP_PATHS" ]; then
        if can_seed_stamp; then
            write_stamp
        else
            regenerate
        fi
    elif can_seed_stamp && committed_generated_outputs_changed; then
        write_stamp
    else
        regenerate
    fi
fi
