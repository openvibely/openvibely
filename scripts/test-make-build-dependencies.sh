#!/bin/sh

set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
MAKE=${MAKE:-make}
ORIGINAL_PATH=$PATH
TMP_ROOT=${TMPDIR:-/tmp}
TEST_DIR=$(mktemp -d "$TMP_ROOT/openvibely-make-dependencies.XXXXXX")
INITIAL_STATE="$TEST_DIR/initial"
BASELINE_STATE="$TEST_DIR/baseline"
WRAPPER_DIR="$TEST_DIR/bin"
GO_LOG="$TEST_DIR/go.log"
LAST_LOG="$TEST_DIR/last.log"

TEMPL_SOURCE="web/templates/pages/tasks.templ"
TEMPL_OUTPUT="web/templates/pages/tasks_templ.go"
SWAGGER_SOURCE="internal/handler/system_handler.go"
UPDATE_SCHEMA_SOURCE="internal/update/client.go"
GO_SOURCE="internal/service/task_service.go"
SCHEMA_PROBE="internal/models/issue_848_schema_probe.go"

TEMPL_STAMP="bin/.templ-inputs"
SWAGGER_STAMP="bin/.swagger-inputs"
SERVER_BINARY="bin/openvibely"
DESKTOP_BINARY="bin/openvibely-desktop"

mkdir -p "$WRAPPER_DIR"

state_paths() {
    include_sources=$1
    (
        cd "$ROOT"
        find web/templates -type f -name '*_templ.go' -print
        printf '%s\n' docs/docs.go docs/swagger.json docs/swagger.yaml
        printf '%s\n' "$TEMPL_STAMP" "$SWAGGER_STAMP" "$SERVER_BINARY" "$DESKTOP_BINARY"
        if [ "$include_sources" = "1" ]; then
            printf '%s\n' "$TEMPL_SOURCE" "$SWAGGER_SOURCE" "$UPDATE_SCHEMA_SOURCE" "$GO_SOURCE"
        fi
    ) | LC_ALL=C sort -u
}

snapshot_state() {
    state_dir=$1
    include_sources=$2
    rm -rf "$state_dir"
    mkdir -p "$state_dir/files"
    state_paths "$include_sources" > "$state_dir/paths"
    : > "$state_dir/existing"
    : > "$state_dir/missing"
    while IFS= read -r relative_path; do
        [ -n "$relative_path" ] || continue
        if [ -e "$ROOT/$relative_path" ]; then
            mkdir -p "$state_dir/files/$(dirname "$relative_path")"
            cp -p "$ROOT/$relative_path" "$state_dir/files/$relative_path"
            printf '%s\n' "$relative_path" >> "$state_dir/existing"
        else
            printf '%s\n' "$relative_path" >> "$state_dir/missing"
        fi
    done < "$state_dir/paths"
}

restore_state() {
    state_dir=$1
    if [ -f "$state_dir/missing" ]; then
        while IFS= read -r relative_path; do
            [ -n "$relative_path" ] || continue
            rm -f "$ROOT/$relative_path"
        done < "$state_dir/missing"
    fi
    while IFS= read -r relative_path; do
        [ -n "$relative_path" ] || continue
        mkdir -p "$ROOT/$(dirname "$relative_path")"
        cp -p "$state_dir/files/$relative_path" "$ROOT/$relative_path"
    done < "$state_dir/existing"
}

remove_extra_generated_files() {
    if [ ! -f "$INITIAL_STATE/paths" ]; then
        return
    fi
    (
        cd "$ROOT"
        find web/templates -type f -name '*_templ.go' -print
    ) | while IFS= read -r absolute_path; do
        relative_path=${absolute_path#"$ROOT"/}
        if ! grep -F -x -q "$relative_path" "$INITIAL_STATE/paths"; then
            rm -f "$absolute_path"
        fi
    done
}

restore_sources() {
    for relative_path in "$TEMPL_SOURCE" "$SWAGGER_SOURCE" "$UPDATE_SCHEMA_SOURCE" "$GO_SOURCE"; do
        mkdir -p "$ROOT/$(dirname "$relative_path")"
        cp -p "$INITIAL_STATE/files/$relative_path" "$ROOT/$relative_path"
    done
}

restore_baseline() {
    restore_sources
    restore_state "$BASELINE_STATE"
    rm -f "$SCHEMA_PROBE"
}

cleanup() {
    status=$?
    set +e
    rm -f "$SCHEMA_PROBE"
    remove_extra_generated_files
    restore_state "$INITIAL_STATE"
    rm -rf "$TEST_DIR"
    exit "$status"
}
trap cleanup EXIT INT TERM

snapshot_state "$INITIAL_STATE" 1

REAL_GO=$(command -v go)
cat > "$WRAPPER_DIR/go" <<'EOF'
#!/bin/sh

real_go=${OPENVIBELY_BUILD_TEST_GO:?}
go_log=${OPENVIBELY_BUILD_TEST_LOG:?}

if [ "${1:-}" = "run" ]; then
    case "${2:-}" in
        github.com/a-h/templ/cmd/templ@*) printf '%s\n' templ >> "$go_log" ;;
        github.com/swaggo/swag/cmd/swag@*) printf '%s\n' swagger >> "$go_log" ;;
    esac
elif [ "${1:-}" = "build" ]; then
    printf '%s\n' compiler >> "$go_log"
fi

exec "$real_go" "$@"
EOF
chmod +x "$WRAPPER_DIR/go"

run_make() {
    target=$1
    if ! PATH="$WRAPPER_DIR:$ORIGINAL_PATH" \
        OPENVIBELY_BUILD_TEST_GO="$REAL_GO" \
        OPENVIBELY_BUILD_TEST_LOG="$GO_LOG" \
        "$MAKE" -C "$ROOT" "$target" > "$LAST_LOG" 2>&1; then
        printf '%s\n' "make $target failed:" >&2
        cat "$LAST_LOG" >&2
        return 1
    fi
    return 0
}

run_make_dry() {
    target=$1
    "$MAKE" -C "$ROOT" -n "$target"
}

count_log() {
    value=$1
    awk -v value="$value" '$0 == value { count++ } END { print count + 0 }' "$GO_LOG"
}

expect_counts() {
    expected_templ=$1
    expected_swagger=$2
    expected_compiler=$3
    actual_templ=$(count_log templ)
    actual_swagger=$(count_log swagger)
    actual_compiler=$(count_log compiler)
    if [ "$actual_templ" -ne "$expected_templ" ] || \
        [ "$actual_swagger" -ne "$expected_swagger" ] || \
        [ "$actual_compiler" -ne "$expected_compiler" ]; then
        printf '%s\n' "unexpected subprocess counts: templ=$actual_templ swagger=$actual_swagger compiler=$actual_compiler" >&2
        cat "$LAST_LOG" >&2
        exit 1
    fi
}

reset_log() {
    : > "$GO_LOG"
    : > "$LAST_LOG"
}

hash_file() {
    git -C "$ROOT" hash-object "$1"
}

assert_file_exists() {
    test -f "$ROOT/$1" || {
        printf '%s\n' "expected file to exist: $1" >&2
        exit 1
    }
}

assert_no_delimiter_fields() {
    if grep -Eq 'LeftDelim:|RightDelim:' "$ROOT/docs/docs.go"; then
        printf '%s\n' 'docs/docs.go still contains delimiter fields' >&2
        exit 1
    fi
}

# A clean checkout has tracked generated outputs but no ignored freshness stamps.
# Seed the stamps from that clean, issue-scoped tree instead of regenerating both artifacts.
rm -f "$ROOT/$TEMPL_STAMP" "$ROOT/$SWAGGER_STAMP"
git -C "$ROOT" ls-files --error-unmatch -- \
    "$TEMPL_OUTPUT" docs/docs.go docs/swagger.json docs/swagger.yaml >/dev/null
reset_log
run_make build
expect_counts 0 0 1
assert_file_exists "$TEMPL_STAMP"
assert_file_exists "$SWAGGER_STAMP"
snapshot_state "$BASELINE_STATE" 0

# An unavailable stamp must still regenerate when a relevant source is dirty.
restore_baseline
rm -f "$ROOT/$TEMPL_STAMP"
printf '\n// issue-848 unknown template freshness state\n' >> "$ROOT/$TEMPL_SOURCE"
reset_log
run_make build
expect_counts 1 0 1
restore_baseline

# Malformed fingerprint metadata must also regenerate conservatively.
restore_baseline
stamp_head=$(sed -n '1p' "$ROOT/$TEMPL_STAMP")
stamp_version=$(sed -n '2p' "$ROOT/$TEMPL_STAMP")
stamp_paths=$(sed -n '4p' "$ROOT/$TEMPL_STAMP")
printf '%s\n' "$stamp_head" "$stamp_version" 'not-a-digest' "$stamp_paths" '0' > "$ROOT/$TEMPL_STAMP"
reset_log
run_make build
expect_counts 1 0 1
restore_baseline

# Unchanged server builds must compile without either generator.
restore_baseline
reset_log
run_make build
expect_counts 0 0 1
assert_file_exists "$SERVER_BINARY"

# Desktop and the run wrapper must retain their existing binary graph without generation.
restore_baseline
reset_log
run_make build-desktop
expect_counts 0 0 1
assert_file_exists "$DESKTOP_BINARY"
run_output=$(run_make_dry run)
case "$run_output" in
    *'./bin/openvibely'*) ;;
    *) printf '%s\n' 'make -n run did not retain the server binary path' >&2; exit 1 ;;
esac
case "$run_output" in
    *'go run github.com/a-h/templ/cmd/templ@'*) printf '%s\n' 'make -n run exposed unconditional templ generation' >&2; exit 1 ;;
esac
case "$run_output" in
    *'go run github.com/swaggo/swag/cmd/swag@'*) printf '%s\n' 'make -n run exposed unconditional Swagger generation' >&2; exit 1 ;;
esac

# A Go-only edit outside the Swagger source set must not regenerate Swagger.
restore_baseline
printf '\n// issue-848 non-Swagger Go change\n' >> "$ROOT/$GO_SOURCE"
reset_log
run_make build
expect_counts 0 0 1
restore_baseline

# Explicit generation targets remain forced and real.
restore_baseline
reset_log
run_make templ
expect_counts 1 0 0
restore_baseline
reset_log
run_make swagger
expect_counts 0 1 0
assert_no_delimiter_fields

# A real template edit regenerates template output only and compiles the server.
restore_baseline
sed -i.bak 's#<h2 class="text-2xl font-bold">Tasks</h2>#<h2 class="text-2xl font-bold">Tasks</h2><!-- issue-848 template freshness -->#' "$ROOT/$TEMPL_SOURCE"
rm -f "$ROOT/$TEMPL_SOURCE.bak"
reset_log
templ_before=$(hash_file "$TEMPL_OUTPUT")
run_make build
expect_counts 1 0 1
assert_file_exists "$SERVER_BINARY"
templ_after=$(hash_file "$TEMPL_OUTPUT")
[ "$templ_before" != "$templ_after" ] || {
    printf '%s\n' 'template edit did not update its generated Go output' >&2
    exit 1
}
grep -F -q 'issue-848 template freshness' "$ROOT/$TEMPL_OUTPUT" || {
    printf '%s\n' 'generated template output did not contain the changed template content' >&2
    exit 1
}
restore_baseline

# A real Swagger annotation edit regenerates docs only and preserves delimiter cleanup.
restore_baseline
sed -E -i.bak 's#// @Summary System health and build identity$#// @Summary System health and build identity (issue-848 freshness)#' "$ROOT/$SWAGGER_SOURCE"
rm -f "$ROOT/$SWAGGER_SOURCE.bak"
reset_log
swagger_before=$(hash_file docs/docs.go)
run_make build
expect_counts 0 1 1
swagger_after=$(hash_file docs/docs.go)
[ "$swagger_before" != "$swagger_after" ] || {
    printf '%s\n' 'Swagger annotation edit did not update docs/docs.go' >&2
    exit 1
}
grep -F -q 'issue-848 freshness' "$ROOT/docs/docs.go" || {
    printf '%s\n' 'generated Swagger output did not contain the changed annotation' >&2
    exit 1
}
assert_no_delimiter_fields
restore_baseline

# A nested update-package schema change must also regenerate Swagger documentation,
# even when the prior freshness state is unavailable.
restore_baseline
rm -f "$ROOT/$SWAGGER_STAMP"
sed -E -i.bak 's#json:"image_ref,omitempty"#json:"issue_848_update_schema_probe,omitempty"#' "$ROOT/$UPDATE_SCHEMA_SOURCE"
rm -f "$ROOT/$UPDATE_SCHEMA_SOURCE.bak"
reset_log
update_before=$(hash_file docs/docs.go)
run_make build
expect_counts 0 1 1
update_after=$(hash_file docs/docs.go)
[ "$update_before" != "$update_after" ] || {
    printf '%s\n' 'nested update schema change did not update docs/docs.go' >&2
    exit 1
}
grep -F -q 'issue_848_update_schema_probe' "$ROOT/docs/docs.go" || {
    printf '%s\n' 'generated Swagger output did not contain the nested update schema change' >&2
    exit 1
}
assert_no_delimiter_fields
restore_baseline

# Removing the last annotations from a previously annotated file must regenerate docs,
# even when no prior freshness stamp exists.
restore_baseline
rm -f "$ROOT/$SWAGGER_STAMP"
sed -E -i.bak '/^[[:space:]]*\/\/[[:space:]]+@[[:alpha:]]/d' "$ROOT/$SWAGGER_SOURCE"
rm -f "$ROOT/$SWAGGER_SOURCE.bak"
if grep -Eq '^[[:space:]]*\/\/[[:space:]]+@[[:alpha:]]' "$ROOT/$SWAGGER_SOURCE"; then
    printf '%s\n' 'Swagger annotation removal probe did not remove all annotations' >&2
    exit 1
fi
reset_log
run_make build
expect_counts 0 1 1
if grep -q '/api/system/health' "$ROOT/docs/swagger.json" "$ROOT/docs/swagger.yaml" "$ROOT/docs/docs.go"; then
    printf '%s\n' 'removed Swagger annotations left the health route in generated docs' >&2
    exit 1
fi
assert_no_delimiter_fields
restore_baseline

# Adding and then removing a schema input must be observed through the saved fingerprint.
restore_baseline
cat > "$ROOT/$SCHEMA_PROBE" <<'EOF'
package models

type Issue848SchemaProbe struct {
	Value string `json:"value"`
}
EOF
reset_log
run_make build
expect_counts 0 1 1
rm -f "$ROOT/$SCHEMA_PROBE"
reset_log
run_make build
expect_counts 0 1 1
assert_no_delimiter_fields
restore_baseline

# Missing generated outputs must trigger one complete regeneration and a successful build.
restore_baseline
rm -f "$ROOT/docs/swagger.json"
reset_log
run_make build
expect_counts 0 1 1
assert_file_exists docs/swagger.json
restore_baseline
rm -f "$ROOT/$TEMPL_OUTPUT"
reset_log
run_make build
expect_counts 1 0 1
assert_file_exists "$TEMPL_OUTPUT"

# Force both generators once more and require the generated tree to remain clean.
restore_baseline
run_make templ
run_make swagger
assert_no_delimiter_fields
if ! git -C "$ROOT" diff --exit-code -- docs/docs.go docs/swagger.json docs/swagger.yaml web/templates; then
    printf '%s\n' 'explicit generation left an unexplained generated-file diff' >&2
    exit 1
fi

printf '%s\n' 'make build dependency regression checks passed'
