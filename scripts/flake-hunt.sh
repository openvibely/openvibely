#!/usr/bin/env bash
# Hunts flaky tests by running suites repeatedly under conditions that expose them.
#
# Most flaky tests pass on a fast, idle laptop and fail on a slow, shared CI runner:
# a goroutine is scheduled late, a fixed sleep or timeout expires first, or two pieces
# of work race. Load soaking reproduces that locally: it keeps most CPU cores busy so
# tests compete for CPU, then runs the same tests many times at once. A test that fails
# even occasionally here is timing-sensitive and will eventually fail in CI.
#
# The CPU load comes from `yes > /dev/null`. `yes` is a standard Unix command that prints
# "y" in an endless loop; redirecting it to /dev/null throws the output away, so all it
# does is spin one CPU core at 100%. The script starts LOAD of them in the background and
# always kills them on exit, including Ctrl-C (or stop strays with `pkill -x yes`).
#
# Modes:
#   browser             Browser functional tests (TestBrowserFunctional_*) in
#                       web/templates/components and web/templates/pages, under load.
#                       Each package's test binary is built once, then WORKERS copies
#                       run in parallel, each running every test PASSES times.
#   unit                The CI "all tests" step (go test ./..., browser tests skipped),
#                       run PASSES times back to back under load.
#   race                The CI unit step with -race and -shuffle=on, -count=PASSES, no
#                       extra load. Finds data races and tests that depend on run order
#                       or on state another test left behind. Tests with wall-clock
#                       budgets can fail here only because -race slows code 2-10x.
#   test <pkg> <regex>  One package and -run pattern under load, the same way as
#                       browser. Use it to check that a single fix holds up, e.g.
#                       scripts/flake-hunt.sh test ./internal/handler '^TestFoo$'
#
# Options (environment variables):
#   PASSES   Times each test runs per worker (unit: full suite runs; race: -count).
#            Default 3.
#   WORKERS  Parallel test processes for browser and test modes. Default 3.
#   LOAD     CPU busy-loop processes for browser, unit, and test modes. Each one keeps
#            one CPU core fully busy, so LOAD is roughly the number of cores taken away
#            from the tests. Default: CPU cores minus 4, at least 1 (14 on an 18-core
#            machine), leaving about 4 cores for the tests, like a busy CI runner.
#   OUT      Directory for raw output. Default: a new temporary directory.
#
# Example: PASSES=10 WORKERS=4 scripts/flake-hunt.sh browser
#
# The summary lists failing tests with counts, data races, panics and timeouts, and the
# output directory. A failure seen once is worth investigating: rerun it with test mode
# and read the full output for its error.
set -euo pipefail

mode=${1:-}
case "$mode" in
  browser | unit | race | test) ;;
  *)
    awk 'NR > 1 && /^#/ { sub(/^# ?/, ""); print; next } NR > 1 { exit }' "$0"
    exit 2
    ;;
esac
cd "$(dirname "$0")/.."

cores=$(getconf _NPROCESSORS_ONLN 2>/dev/null || sysctl -n hw.ncpu)
PASSES=${PASSES:-3}
WORKERS=${WORKERS:-3}
LOAD=${LOAD:-$(( cores > 4 ? cores - 4 : 1 ))}
OUT=${OUT:-$(mktemp -d "${TMPDIR:-/tmp}/flake-hunt.XXXXXX")}
mkdir -p "$OUT"

burners=()
stop_load() {
  if ((${#burners[@]})); then kill "${burners[@]}" 2>/dev/null || true; fi
}
trap stop_load EXIT INT TERM
# Busy loops make tests compete for CPU the way they do on a slow, shared CI runner.
start_load() {
  for _ in $(seq "$LOAD"); do yes > /dev/null & burners+=($!); done
}

# run_loaded <label> <package> <run-regex> [extra go test args...]
# Builds the package's test binary once, then runs WORKERS copies in parallel, each PASSES times.
run_loaded() {
  local label=$1 pkg=$2 regex=$3
  shift 3
  local bin="$OUT/$label.test"
  go test -c -o "$bin" "$@" "$pkg"
  local dir
  dir=$(go list -f '{{.Dir}}' "$pkg")
  local pids=()
  for w in $(seq "$WORKERS"); do
    (cd "$dir" && "$bin" -test.run "$regex" -test.count="$PASSES" -test.timeout 3600s > "$OUT/$label-$w.out" 2>&1) &
    pids+=($!)
  done
  wait "${pids[@]}" || true
}

summarize() {
  echo "== failing tests (count) =="
  grep -hE '^\s*--- FAIL' "$OUT"/*.out | sed -E 's/^\s*//; s/ \(.*//' | sort | uniq -c | sort -rn || true
  echo "== failing packages =="
  grep -hE '^FAIL\s' "$OUT"/*.out | sort | uniq -c || true
  echo "== data races =="
  { grep -h -c 'WARNING: DATA RACE' "$OUT"/*.out || true; } | awk '{s+=$1} END {print s+0}'
  echo "== panics / timeouts =="
  grep -hE '^panic:|test timed out' "$OUT"/*.out | sort | uniq -c || true
  echo "full output: $OUT"
}

case "$mode" in
  browser)
    start_load
    run_loaded components ./web/templates/components '^TestBrowserFunctional_'
    run_loaded pages ./web/templates/pages '^TestBrowserFunctional_'
    ;;
  unit)
    start_load
    for pass in $(seq "$PASSES"); do
      OPENVIBELY_SKIP_BROWSER_TESTS=1 go test ./... -count=1 -timeout 1200s > "$OUT/unit-$pass.out" 2>&1 || true
    done
    ;;
  race)
    # The race detector already slows tests 2-10x, so no extra CPU load here; timing-budget
    # failures under -race are expected noise, races and order dependence are the signal.
    OPENVIBELY_SKIP_BROWSER_TESTS=1 go test ./... -race -shuffle=on -count="$PASSES" -timeout 3600s > "$OUT/race.out" 2>&1 || true
    ;;
  test)
    pkg=${2:?usage: $0 test <package> <run-regex>}
    regex=${3:?usage: $0 test <package> <run-regex>}
    start_load
    run_loaded single "$pkg" "$regex"
    ;;
esac

summarize
