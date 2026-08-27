#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MAKE=${MAKE:-make}
TMPDIR=${TMPDIR:-/tmp}
BACKUP_DIR=$(mktemp -d "$TMPDIR/openvibely-make-dependencies.XXXXXX")

TEMPL_SOURCE="$ROOT/web/templates/pages/tasks.templ"
SWAGGER_SOURCE="$ROOT/internal/handler/system_handler.go"
GO_SOURCE="$ROOT/internal/service/task_service.go"

cleanup() {
    status=$?
    cp -p "$BACKUP_DIR/tasks.templ" "$TEMPL_SOURCE"
    cp -p "$BACKUP_DIR/system_handler.go" "$SWAGGER_SOURCE"
    cp -p "$BACKUP_DIR/task_service.go" "$GO_SOURCE"
    rm -rf "$BACKUP_DIR"
    exit "$status"
}
trap cleanup EXIT INT TERM

cp -p "$TEMPL_SOURCE" "$BACKUP_DIR/tasks.templ"
cp -p "$SWAGGER_SOURCE" "$BACKUP_DIR/system_handler.go"
cp -p "$GO_SOURCE" "$BACKUP_DIR/task_service.go"

generator_templ='go run github.com/a-h/templ/cmd/templ@'
generator_swagger='go run github.com/swaggo/swag/cmd/swag@'

assert_contains() {
    output=$1
    expected=$2
    case "$output" in
        *"$expected"*) ;;
        *)
            printf '%s\n' "expected make output to contain: $expected" >&2
            printf '%s\n' "$output" >&2
            exit 1
            ;;
    esac
}

assert_not_contains() {
    output=$1
    unexpected=$2
    case "$output" in
        *"$unexpected"*)
            printf '%s\n' "expected make output not to contain: $unexpected" >&2
            printf '%s\n' "$output" >&2
            exit 1
            ;;
        *) ;;
    esac
}

run_make() {
    "$MAKE" -C "$ROOT" -n "$1"
}

for target in build build-desktop run; do
    output=$(run_make "$target")
    assert_not_contains "$output" "$generator_templ"
    assert_not_contains "$output" "$generator_swagger"
done

printf '\n' >> "$TEMPL_SOURCE"
output=$(run_make build)
assert_contains "$output" "$generator_templ"
assert_not_contains "$output" "$generator_swagger"
cp -p "$BACKUP_DIR/tasks.templ" "$TEMPL_SOURCE"

printf '\n' >> "$SWAGGER_SOURCE"
output=$(run_make build)
assert_contains "$output" "$generator_swagger"
assert_not_contains "$output" "$generator_templ"
cp -p "$BACKUP_DIR/system_handler.go" "$SWAGGER_SOURCE"

printf '\n' >> "$GO_SOURCE"
output=$(run_make build)
assert_not_contains "$output" "$generator_templ"
assert_not_contains "$output" "$generator_swagger"

printf '%s\n' 'make build dependency regression checks passed'
