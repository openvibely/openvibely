#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

TEMPL_VERSION=${TEMPL_VERSION:-$(go list -m -f '{{.Version}}' github.com/a-h/templ)}
TEMPL_FORCE=${TEMPL_FORCE:-0}
STAMP="$ROOT/bin/.templ-inputs"
LOCK="$STAMP.lock"
CURRENT_HEAD=$(git rev-parse HEAD)
STAMP_HEAD=
STAMP_VERSION=
STAMP_FINGERPRINT=
STAMP_PATHS=
STAMP_UNTRACKED_STATE=
STAMP_VALID=0

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

refresh_inputs() {
    TEMPL_REPOSITORY_PATHS=$(git ls-files --cached --others --exclude-standard)
    TEMPL_UNTRACKED_STATE=0
    for path in $(git ls-files --others --exclude-standard); do
        case "$path" in
            web/templates/*.templ|web/templates/*_templ.go|go.mod|go.sum)
                TEMPL_UNTRACKED_STATE=1
                ;;
        esac
    done
    TEMPL_SOURCES=$(printf '%s\n' "$TEMPL_REPOSITORY_PATHS" | awk '$0 ~ /^web\/templates\/.*\.templ$/ { print }')
    TEMPL_OUTPUTS=""
    TEMPL_STALE_OUTPUTS=""
    for source in $TEMPL_SOURCES; do
        output=${source%.templ}_templ.go
        if [ -f "$source" ]; then
            TEMPL_OUTPUTS="$TEMPL_OUTPUTS $output"
        else
            TEMPL_STALE_OUTPUTS="$TEMPL_STALE_OUTPUTS $output"
        fi
    done
    TEMPL_TRACKED_OUTPUTS=$(printf '%s\n' "$TEMPL_REPOSITORY_PATHS" | awk '$0 ~ /^web\/templates\/.*_templ\.go$/ { print }')
    TEMPL_PATHS=$( {
        printf 'template-source:%s\n' "$TEMPL_SOURCES"
        printf 'template-output:%s\n' "$TEMPL_TRACKED_OUTPUTS"
    } | git hash-object --stdin )
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

has_relevant_worktree_changes() {
    changed_paths=$( {
        git diff --name-only HEAD --
        git ls-files --others --exclude-standard
    } )
    for path in $changed_paths; do
        case "$path" in
            web/templates/*.templ|web/templates/*_templ.go|go.mod|go.sum)
                return 0
                ;;
        esac
    done
    return 1
}

committed_generated_outputs_changed() {
    [ -n "$STAMP_HEAD" ] || return 1
    changed_paths=$(git diff --name-only "$STAMP_HEAD" "$CURRENT_HEAD" -- web/templates 2>/dev/null) || return 1
    for path in $changed_paths; do
        case "$path" in
            web/templates/*_templ.go)
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
        printf 'generator-version:%s\n' "$TEMPL_VERSION"
        for source in $TEMPL_SOURCES; do
            printf 'template:%s\n' "$source"
            append_file "$source"
        done
        for input in go.mod go.sum; do
            printf 'configuration:%s\n' "$input"
            append_file "$input"
        done
    } | git hash-object --stdin
}

outputs_are_complete() {
    for output in $TEMPL_OUTPUTS; do
        test -f "$output" || return 1
    done
    for output in $TEMPL_TRACKED_OUTPUTS; do
        case " $TEMPL_OUTPUTS " in
            *" $output "*) ;;
            *) return 1 ;;
        esac
    done
    return 0
}

tracked_inputs_are_clean() {
    paths="$TEMPL_SOURCES $TEMPL_TRACKED_OUTPUTS go.mod go.sum"
    git ls-files --error-unmatch -- $paths >/dev/null 2>&1 || return 1
    git diff --quiet -- $paths || return 1
    git diff --cached --quiet -- $paths || return 1
    return 0
}

can_seed_stamp() {
    outputs_are_complete || return 1
    tracked_inputs_are_clean || return 1
    return 0
}

write_stamp() {
    tmp="$STAMP.$$"
    {
        printf '%s\n' "$CURRENT_HEAD"
        printf '%s\n' "$TEMPL_VERSION"
        printf '%s\n' "$CURRENT_FINGERPRINT"
        printf '%s\n' "$TEMPL_PATHS"
        printf '%s\n' "$TEMPL_UNTRACKED_STATE"
    } > "$tmp"
    mv "$tmp" "$STAMP"
}

regenerate() {
    go run "github.com/a-h/templ/cmd/templ@$TEMPL_VERSION" generate

    for output in $TEMPL_OUTPUTS; do
        test -f "$output" || {
            printf '%s\n' "templ did not generate expected output: $output" >&2
            return 1
        }
    done

    for output in $TEMPL_STALE_OUTPUTS; do
        rm -f "$output"
    done
    for output in $TEMPL_TRACKED_OUTPUTS; do
        case " $TEMPL_OUTPUTS " in
            *" $output "*) ;;
            *) rm -f "$output" ;;
        esac
    done

    if [ -n "$TEMPL_OUTPUTS" ]; then
        touch $TEMPL_OUTPUTS
    fi
    write_stamp
}

regenerate_now() {
    CURRENT_FINGERPRINT=$(fingerprint)
    regenerate
}

read_stamp
refresh_inputs

if [ "$TEMPL_FORCE" = "1" ]; then
    regenerate_now
elif ! outputs_are_complete; then
    regenerate_now
elif [ ! -f "$STAMP" ]; then
    CURRENT_FINGERPRINT=$(fingerprint)
    if can_seed_stamp; then
        write_stamp
    else
        regenerate
    fi
elif [ "$STAMP_VALID" = "1" ] &&
    [ "$STAMP_HEAD" = "$CURRENT_HEAD" ] &&
    [ "$STAMP_VERSION" = "$TEMPL_VERSION" ] &&
    valid_digest "$STAMP_FINGERPRINT" &&
    valid_digest "$STAMP_PATHS"; then
    if [ "$STAMP_PATHS" != "$TEMPL_PATHS" ]; then
        regenerate_now
    elif has_relevant_worktree_changes; then
        regenerate_now
    else
        for output in $TEMPL_OUTPUTS; do
            if test "$output" -nt "$STAMP"; then
                regenerate_now
                exit $?
            fi
        done
    fi
else
    CURRENT_FINGERPRINT=$(fingerprint)
    if [ "$STAMP_VERSION" != "$TEMPL_VERSION" ]; then
        regenerate
    elif [ "$CURRENT_FINGERPRINT" = "$STAMP_FINGERPRINT" ] && [ "$TEMPL_PATHS" = "$STAMP_PATHS" ]; then
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
