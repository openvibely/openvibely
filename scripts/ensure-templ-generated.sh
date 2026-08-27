#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

TEMPL_VERSION=${TEMPL_VERSION:-$(go list -m -f '{{.Version}}' github.com/a-h/templ)}
TEMPL_FORCE=${TEMPL_FORCE:-0}
STAMP="$ROOT/bin/.templ-inputs"
LOCK="$STAMP.lock"
SCRIPT_INPUT="scripts/ensure-templ-generated.sh"

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
    TEMPL_SOURCES=$(git ls-files --cached --others --exclude-standard | awk '$0 ~ /^web\/templates\/.*\.templ$/ { print }' | LC_ALL=C sort -u)
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
    TEMPL_TRACKED_OUTPUTS=$(git ls-files --cached --others --exclude-standard | awk '$0 ~ /^web\/templates\/.*_templ\.go$/ { print }' | LC_ALL=C sort -u)
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
        for input in go.mod go.sum Makefile "$SCRIPT_INPUT"; do
            printf 'configuration:%s\n' "$input"
            append_file "$input"
        done
    } | git hash-object --stdin
}

all_outputs_exist() {
    for output in $TEMPL_OUTPUTS; do
        test -f "$output" || return 1
    done
    return 0
}

write_stamp() {
    tmp="$STAMP.$$"
    printf '%s\n' "$CURRENT_FINGERPRINT" > "$tmp"
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

    touch $TEMPL_OUTPUTS
    write_stamp
}

refresh_inputs
CURRENT_FINGERPRINT=$(fingerprint)
PREVIOUS_FINGERPRINT=$(cat "$STAMP" 2>/dev/null || true)

if [ "$TEMPL_FORCE" = "1" ]; then
    regenerate
elif ! all_outputs_exist; then
    regenerate
elif [ -f "$STAMP" ] && [ "$PREVIOUS_FINGERPRINT" = "$CURRENT_FINGERPRINT" ]; then
    for output in $TEMPL_OUTPUTS; do
        if test "$output" -nt "$STAMP"; then
            regenerate
            exit $?
        fi
    done
else
    regenerate
fi
