#!/usr/bin/env bash
set -euo pipefail

PORT="${PORT:-3001}"
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Load .env before resolving runtime defaults so OPENVIBELY_APP_DATA_DIR can
# make local server and desktop share DB/repos/global agents when desired.
if [ -f "$SCRIPT_DIR/.env" ]; then
    echo -e "\033[0;32m[openvibely]\033[0m Loading .env"
    set -a
    source "$SCRIPT_DIR/.env"
    set +a
fi

if [ -n "${OPENVIBELY_APP_DATA_DIR:-}" ]; then
    RUNTIME_DIR="$OPENVIBELY_APP_DATA_DIR"
elif [ -n "${OPENVIBELY_RUNTIME_DIR:-}" ]; then
    OPENVIBELY_APP_DATA_DIR="$OPENVIBELY_RUNTIME_DIR"
    RUNTIME_DIR="$OPENVIBELY_APP_DATA_DIR"
else
    RUNTIME_DIR="$HOME/.openvibely"
fi
DISPLAY_DATABASE_PATH="${DATABASE_PATH:-$RUNTIME_DIR/openvibely.db}"
DISPLAY_PROJECT_REPO_ROOT="${PROJECT_REPO_ROOT:-$RUNTIME_DIR/repos}"
BIN_DIR="$SCRIPT_DIR/bin"
BINARY="$BIN_DIR/openvibely"
LOG_DIR="$SCRIPT_DIR/logs"
LOG_FILE="$LOG_DIR/openvibely.log"
TEMPL_MODULE="github.com/a-h/templ/cmd/templ"
TEMPL_VERSION="${TEMPL_VERSION:-}"
if [ "${ENVIRONMENT+x}" != "x" ]; then
    ENVIRONMENT=development
fi
OPENVIBELY_ENABLE_LOCAL_REPO_PATH=true
OPENVIBELY_LOG_LEVEL="${OPENVIBELY_LOG_LEVEL:-info}"
#AUTH_ENABLED=true
#AUTH_USERNAME=admin
#AUTH_PASSWORD=password
# AUTH_SESSION_SECRET="$(openssl rand -base64 32 | tr '+/' '-_' | tr -d '=\n')"

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
    local templ_path templ_version

    if command -v templ &>/dev/null; then
        templ_path="$(command -v templ)"
        templ_version="$("$templ_path" version 2>/dev/null || true)"
        if [ "$templ_version" = "$TEMPL_VERSION" ]; then
            log "templ found on PATH: $templ_path ($templ_version)"
            return 0
        fi
        warn "templ found on PATH at $templ_path, but version is ${templ_version:-unknown}; expected $TEMPL_VERSION"
    fi

    gobin="$(go env GOBIN 2>/dev/null || true)"
    gopath="$(go env GOPATH 2>/dev/null || true)"

    if [ -n "$gobin" ] && [ -x "$gobin/templ" ]; then
        export PATH="$gobin:$PATH"
        templ_version="$("$gobin/templ" version 2>/dev/null || true)"
        if [ "$templ_version" = "$TEMPL_VERSION" ]; then
            log "templ found in GOBIN and added to PATH: $gobin/templ ($templ_version)"
            return 0
        fi
        warn "templ found in GOBIN at $gobin/templ, but version is ${templ_version:-unknown}; expected $TEMPL_VERSION"
    fi

    if [ -n "$gopath" ] && [ -x "$gopath/bin/templ" ]; then
        export PATH="$gopath/bin:$PATH"
        templ_version="$("$gopath/bin/templ" version 2>/dev/null || true)"
        if [ "$templ_version" = "$TEMPL_VERSION" ]; then
            log "templ found in GOPATH/bin and added to PATH: $gopath/bin/templ ($templ_version)"
            return 0
        fi
        warn "templ found in GOPATH/bin at $gopath/bin/templ, but version is ${templ_version:-unknown}; expected $TEMPL_VERSION"
    fi

    warn "Installing ${TEMPL_MODULE}@${TEMPL_VERSION}..."
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

    templ_path="$(command -v templ)"
    templ_version="$("$templ_path" version 2>/dev/null || true)"
    if [ "$templ_version" != "$TEMPL_VERSION" ]; then
        err "templ installation produced ${templ_version:-unknown}; expected $TEMPL_VERSION"
        exit 1
    fi

    log "templ installed and available at: $templ_path ($templ_version)"
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

if [ -z "$TEMPL_VERSION" ]; then
    TEMPL_VERSION="$(go list -m -f '{{.Version}}' github.com/a-h/templ 2>/dev/null)"
    if [ -z "$TEMPL_VERSION" ]; then
        err "Unable to resolve templ version from go.mod"
        exit 1
    fi
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

# Verify port is free after shutdown
if lsof -ti:"$PORT" &>/dev/null; then
    err "Port $PORT is still in use by another process. Cannot start."
    exit 1
fi

export PORT OPENVIBELY_ENABLE_LOCAL_REPO_PATH OPENVIBELY_LOG_LEVEL AUTH_ENABLED AUTH_USERNAME AUTH_PASSWORD AUTH_SESSION_SECRET
if [ "${ENVIRONMENT+x}" = "x" ] && [ -n "${ENVIRONMENT:-}" ]; then export ENVIRONMENT; fi
if [ "${OPENVIBELY_HOSTED_SSO_ENABLED+x}" = "x" ]; then export OPENVIBELY_HOSTED_SSO_ENABLED; fi
if [ "${OPENVIBELY_HOSTED_CONTROL_URL+x}" = "x" ]; then export OPENVIBELY_HOSTED_CONTROL_URL; fi
if [ "${OPENVIBELY_HOSTED_INSTANCE_ID+x}" = "x" ]; then export OPENVIBELY_HOSTED_INSTANCE_ID; fi
if [ -n "${OPENVIBELY_APP_DATA_DIR:-}" ]; then export OPENVIBELY_APP_DATA_DIR; fi
if [ -n "${DATABASE_PATH:-}" ]; then export DATABASE_PATH; fi
if [ -n "${PROJECT_REPO_ROOT:-}" ]; then export PROJECT_REPO_ROOT; fi

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
if [ -n "${OPENVIBELY_APP_DATA_DIR:-}" ]; then
    mkdir -p "$RUNTIME_DIR"
fi

log "Starting OpenVibely on http://localhost:$PORT"
if [ -n "${APP_BASE_URL:-}" ]; then
    log "App base URL: $APP_BASE_URL (OAuth callbacks use public host)"
else
    log "App base URL: not set (OAuth callbacks use localhost)"
fi
log "Database: $DISPLAY_DATABASE_PATH"
log "Repos: $DISPLAY_PROJECT_REPO_ROOT"
log "Logs: $LOG_FILE"
log "Press Ctrl+C to stop"
echo ""

exec "$BINARY" 2>&1 | tee "$LOG_FILE"
