#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"

TEMPL_VERSION=${TEMPL_VERSION:-$(go list -m -f '{{.Version}}' github.com/a-h/templ)}
SWAG_VERSION=${SWAG_VERSION:-$(go list -m -f '{{.Version}}' github.com/swaggo/swag)}
TEMPL_STAMP="$ROOT/bin/.templ-inputs"
SWAGGER_STAMP="$ROOT/bin/.swagger-inputs"
CURRENT_HEAD=$(git rev-parse HEAD)
ANNOTATION_PATTERN='^[[:space:]]*//[[:space:]]+@[[:alpha:]]'

mkdir -p "$ROOT/bin"

read_stamp() {
    stamp=$1
    STAMP_HEAD=
    STAMP_VERSION=
    STAMP_FINGERPRINT=
    STAMP_PATHS=
    STAMP_UNTRACKED_STATE=
    STAMP_VALID=0
    if [ -f "$stamp" ]; then
        set -- $(cat "$stamp")
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

has_annotation() {
    [ -f "$1" ] && grep -Eq "$ANNOTATION_PATTERN" "$1"
}

is_swagger_schema_source() {
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

has_relevant_tracked_changes() {
    changed_paths=$(git diff --name-only HEAD --)
    for path in $changed_paths; do
        case "$path" in
            web/templates/*.templ|web/templates/*_templ.go|go.mod|go.sum|.swaggo|docs/docs.go|docs/swagger.json|docs/swagger.yaml)
                return 0
                ;;
        esac
        if is_swagger_schema_source "$path"; then
            return 0
        fi
        case "$path" in
            cmd/*.go|internal/*.go|pkg/*.go|web/*.go)
                if has_annotation "$path" || git grep -q -E "$ANNOTATION_PATTERN" HEAD -- "$path" 2>/dev/null; then
                    return 0
                fi
                ;;
        esac
    done
    return 1
}

has_relevant_untracked_changes() {
    for path in $(git ls-files --others --exclude-standard); do
        case "$path" in
            web/templates/*.templ|web/templates/*_templ.go|go.mod|go.sum|.swaggo|docs/docs.go|docs/swagger.json|docs/swagger.yaml|cmd/*.go|internal/*.go|pkg/*.go|web/*.go)
                return 0
                ;;
        esac
    done
    return 1
}

templ_outputs_are_complete() {
    TEMPL_TRACKED_OUTPUTS=
    for path in $(git ls-files 'web/templates/*_templ.go'); do
        case "$path" in
            *_templ.go)
                TEMPL_TRACKED_OUTPUTS="$TEMPL_TRACKED_OUTPUTS $path"
                test -f "$path" || return 1
                ;;
        esac
    done
    return 0
}

templ_outputs_are_not_newer_than_stamp() {
    for output in $TEMPL_TRACKED_OUTPUTS; do
        if test "$output" -nt "$1"; then
            return 1
        fi
    done
    return 0
}

swagger_outputs_are_not_newer_than_stamp() {
    for output in docs/docs.go docs/swagger.json docs/swagger.yaml; do
        if test "$output" -nt "$1"; then
            return 1
        fi
    done
    return 0
}

run_conservative_checks() {
    TEMPL_VERSION="$TEMPL_VERSION" ./scripts/ensure-templ-generated.sh
    SWAG_VERSION="$SWAG_VERSION" ./scripts/ensure-swagger-generated.sh
}

read_stamp "$TEMPL_STAMP"
TEMPL_STAMP_HEAD=$STAMP_HEAD
TEMPL_STAMP_VERSION=$STAMP_VERSION
TEMPL_STAMP_FINGERPRINT=$STAMP_FINGERPRINT
TEMPL_STAMP_PATHS=$STAMP_PATHS
TEMPL_STAMP_UNTRACKED_STATE=$STAMP_UNTRACKED_STATE
TEMPL_STAMP_VALID=$STAMP_VALID
read_stamp "$SWAGGER_STAMP"
SWAGGER_STAMP_HEAD=$STAMP_HEAD
SWAGGER_STAMP_VERSION=$STAMP_VERSION
SWAGGER_STAMP_FINGERPRINT=$STAMP_FINGERPRINT
SWAGGER_STAMP_PATHS=$STAMP_PATHS
SWAGGER_STAMP_UNTRACKED_STATE=$STAMP_UNTRACKED_STATE
SWAGGER_STAMP_VALID=$STAMP_VALID
if [ -f "$TEMPL_STAMP" ] && \
    [ "$TEMPL_STAMP_VALID" = "1" ] && \
    [ "$TEMPL_STAMP_HEAD" = "$CURRENT_HEAD" ] && \
    [ "$TEMPL_STAMP_VERSION" = "$TEMPL_VERSION" ] && \
    valid_digest "$TEMPL_STAMP_FINGERPRINT" && \
    valid_digest "$TEMPL_STAMP_PATHS" && \
    [ "$TEMPL_STAMP_UNTRACKED_STATE" = "0" ] && \
    [ -f "$SWAGGER_STAMP" ] && \
    [ "$SWAGGER_STAMP_VALID" = "1" ] && \
    [ "$SWAGGER_STAMP_HEAD" = "$CURRENT_HEAD" ] && \
    [ "$SWAGGER_STAMP_VERSION" = "$SWAG_VERSION" ] && \
    valid_digest "$SWAGGER_STAMP_FINGERPRINT" && \
    valid_digest "$SWAGGER_STAMP_PATHS" && \
    [ "$SWAGGER_STAMP_UNTRACKED_STATE" = "0" ]; then
    if ! has_relevant_untracked_changes && \
        templ_outputs_are_complete && \
        test -f docs/docs.go && test -f docs/swagger.json && test -f docs/swagger.yaml && \
        templ_outputs_are_not_newer_than_stamp "$TEMPL_STAMP" && \
        swagger_outputs_are_not_newer_than_stamp "$SWAGGER_STAMP" && \
        ! has_relevant_tracked_changes; then
        exit 0
    fi
fi

run_conservative_checks
