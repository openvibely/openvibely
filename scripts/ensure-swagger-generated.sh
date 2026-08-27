#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

SWAG_VERSION=${SWAG_VERSION:-$(go list -m -f '{{.Version}}' github.com/swaggo/swag)}
SWAGGER_FORCE=${SWAGGER_FORCE:-0}
STAMP="$ROOT/bin/.swagger-inputs"
LOCK="$STAMP.lock"
SCRIPT_INPUT="scripts/ensure-swagger-generated.sh"
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

refresh_inputs() {
    ALL_GO_SOURCES=$(git ls-files --cached --others --exclude-standard | awk '$0 ~ /^(cmd|internal|pkg|web)\/.*\.go$/ { print }' | LC_ALL=C sort -u)

    HEAD_ANNOTATION_SOURCES=$(git grep -l -E "$ANNOTATION_PATTERN" HEAD -- cmd internal pkg web 2>/dev/null | sed 's#^HEAD:##' | awk '$0 ~ /\.go$/ { print }' || true)
    CURRENT_ANNOTATION_SOURCES=$(find cmd internal pkg web -type d -name '.*' -prune -o -type f -name '*.go' -exec grep -lE "$ANNOTATION_PATTERN" {} + | LC_ALL=C sort -u || true)
    SWAGGER_ANNOTATION_SOURCES=$(printf '%s\n%s\n' "$HEAD_ANNOTATION_SOURCES" "$CURRENT_ANNOTATION_SOURCES" | sed '/^$/d' | LC_ALL=C sort -u)

    SWAGGER_SCHEMA_SOURCES=$(printf '%s\n' "$ALL_GO_SOURCES" | awk '$0 ~ /^internal\/(models|viewmodels)\/[[:alnum:]_.-]+\.go$/ && $0 !~ /_test\.go$/ { print }')
    SWAGGER_SCHEMA_SOURCES="$SWAGGER_SCHEMA_SOURCES
internal/repository/execution_repo.go
internal/update/coordinator.go"

    SWAGGER_CONFIG_SOURCES="go.mod go.sum Makefile $SCRIPT_INPUT"
    if [ -f .swaggo ] || git ls-files --cached --others --exclude-standard | grep -qx '.swaggo'; then
        SWAGGER_CONFIG_SOURCES="$SWAGGER_CONFIG_SOURCES .swaggo"
    fi
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

all_outputs_exist() {
    for output in $SWAGGER_OUTPUTS; do
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

    touch $SWAGGER_OUTPUTS
    write_stamp
}

refresh_inputs
CURRENT_FINGERPRINT=$(fingerprint)
PREVIOUS_FINGERPRINT=$(cat "$STAMP" 2>/dev/null || true)

if [ "$SWAGGER_FORCE" = "1" ]; then
    regenerate
elif ! all_outputs_exist; then
    regenerate
elif [ -f "$STAMP" ] && [ "$PREVIOUS_FINGERPRINT" = "$CURRENT_FINGERPRINT" ]; then
    for output in $SWAGGER_OUTPUTS; do
        if test "$output" -nt "$STAMP"; then
            regenerate
            exit $?
        fi
    done
else
    regenerate
fi
