#!/usr/bin/env bash
set -euo pipefail

PORT="${PORT:-3001}"
DATABASE_PATH="${DATABASE_PATH:-./openvibely.db}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$SCRIPT_DIR/bin"
BINARY="$BIN_DIR/openvibely"
LOG_DIR="$SCRIPT_DIR/logs"
LOG_FILE="$LOG_DIR/openvibely.log"
TEMPL_MODULE="github.com/a-h/templ/cmd/templ"
TEMPL_VERSION="${TEMPL_VERSION:-v0.3.977}"
ENVIRONMENT="${ENVIRONMENT:-development}"
OPENVIBELY_ENABLE_LOCAL_REPO_PATH=true
OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS=true
#AUTH_ENABLED=true
#AUTH_USERNAME=admin
#AUTH_PASSWORD=password
#AUTH_SESSION_SECRET=secret

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
NC='\033[0m'

log() { echo -e "${GREEN}[openvibely]${NC} $1"; }
warn() { echo -e "${YELLOW}[openvibely]${NC} $1"; }
err() { echo -e "${RED}[openvibely]${NC} $1" >&2; }

ensure_templ() {
    local gopath gobin

    if command -v templ &>/dev/null; then
        log "templ found on PATH: $(command -v templ)"
        return 0
    fi

    gobin="$(go env GOBIN 2>/dev/null || true)"
    gopath="$(go env GOPATH 2>/dev/null || true)"

    if [ -n "$gobin" ] && [ -x "$gobin/templ" ]; then
        export PATH="$gobin:$PATH"
        log "templ found in GOBIN and added to PATH: $gobin/templ"
        return 0
    fi

    if [ -n "$gopath" ] && [ -x "$gopath/bin/templ" ]; then
        export PATH="$gopath/bin:$PATH"
        log "templ found in GOPATH/bin and added to PATH: $gopath/bin/templ"
        return 0
    fi

    warn "templ not found; installing ${TEMPL_MODULE}@${TEMPL_VERSION}..."
    go install "${TEMPL_MODULE}@${TEMPL_VERSION}"

    gobin="$(go env GOBIN 2>/dev/null || true)"
    gopath="$(go env GOPATH 2>/dev/null || true)"

    if [ -n "$gobin" ] && [ -d "$gobin" ]; then
        export PATH="$gobin:$PATH"
    fi
    if [ -n "$gopath" ] && [ -d "$gopath/bin" ]; then
        export PATH="$gopath/bin:$PATH"
    fi

    if ! command -v templ &>/dev/null; then
        err "templ installation succeeded but templ is still not on PATH"
        exit 1
    fi

    log "templ installed and available at: $(command -v templ)"
}

# Kill any existing openvibely server
if lsof -ti:"$PORT" &>/dev/null; then
    warn "Stopping existing server on port $PORT..."
    kill $(lsof -ti:"$PORT") 2>/dev/null || true
    sleep 1
fi

# Check Go is installed
if ! command -v go &>/dev/null; then
    err "Go is not installed. Install it from https://go.dev/dl/"
    exit 1
fi

# Check/install templ and ensure it is usable in this shell
ensure_templ

# Generate templ files
log "Generating templates..."
templ generate

# Build
log "Building..."
mkdir -p "$BIN_DIR"
go build -ldflags="-s -w" -o "$BINARY" ./cmd/server

# Load .env if it exists
if [ -f "$SCRIPT_DIR/.env" ]; then
    log "Loading .env"
    set -a
    source "$SCRIPT_DIR/.env"
    set +a
fi

# Verify port is free after shutdown
if lsof -ti:"$PORT" &>/dev/null; then
    err "Port $PORT is still in use by another process. Cannot start."
    exit 1
fi

export PORT DATABASE_PATH ENVIRONMENT OPENVIBELY_ENABLE_LOCAL_REPO_PATH OPENVIBELY_ENABLE_TASK_CHANGES_MERGE_OPTIONS AUTH_ENABLED AUTH_USERNAME AUTH_PASSWORD AUTH_SESSION_SECRET

# APP_BASE_URL controls hosted OAuth callback URLs.
# Leave unset for local development (uses localhost callback listeners).
# Set to your public URL for hosted callbacks, e.g.:
#   APP_BASE_URL=https://dubee.org
# Optional: force localhost callbacks even on VPS and finish via manual paste:
#   OAUTH_REDIRECT_MODE=localhost_manual
# Other valid values: auto (default), hosted
if [ -n "${APP_BASE_URL:-}" ]; then
    export APP_BASE_URL
fi
if [ -n "${OAUTH_REDIRECT_MODE:-}" ]; then
    export OAUTH_REDIRECT_MODE
fi

mkdir -p "$LOG_DIR"

log "Starting OpenVibely on http://localhost:$PORT"
if [ -n "${APP_BASE_URL:-}" ]; then
    log "App base URL: $APP_BASE_URL (OAuth callbacks use public host)"
else
    log "App base URL: not set (OAuth callbacks use localhost)"
fi
log "Database: $DATABASE_PATH"
log "Logs: $LOG_FILE"
log "Press Ctrl+C to stop"
echo ""

exec "$BINARY" 2>&1 | tee "$LOG_FILE"
