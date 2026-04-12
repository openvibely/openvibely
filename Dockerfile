# =============================================================================
# Stage 1: Builder — compile Go binary with all generated assets
# =============================================================================
FROM golang:1.24-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

# Install templ and swag for code generation
RUN go install github.com/a-h/templ/cmd/templ@latest \
 && go install github.com/swaggo/swag/cmd/swag@latest

WORKDIR /src

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Copy full source
COPY . .

# Generate templ templates
RUN templ generate

# Generate swagger docs
RUN swag init -g cmd/server/main.go -o docs \
 && sed -i '/LeftDelim:/d' docs/docs.go \
 && sed -i '/RightDelim:/d' docs/docs.go

# Build static binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -o /out/openvibely \
    ./cmd/server

# =============================================================================
# Stage 2: Collect Git and its dynamic library dependencies
# =============================================================================
FROM alpine:3.21 AS git-collector

RUN apk add --no-cache git binutils

# Copy git binary and discover all shared libraries it needs.
# We use a helper script so the final scratch image has a working git.
RUN mkdir -p /git-dist/usr/bin /git-dist/usr/lib /git-dist/etc \
 && cp "$(which git)" /git-dist/usr/bin/git \
 && ldd "$(which git)" \
    | awk '/=>/ {print $3}' \
    | sort -u \
    | while read -r lib; do \
        dir="/git-dist$(dirname "$lib")"; \
        mkdir -p "$dir"; \
        cp "$lib" "$dir/"; \
      done \
 # Also grab the dynamic linker itself
 && INTERP=$(readelf -l "$(which git)" | grep 'program interpreter' | sed 's/.*: \(.*\)]/\1/') \
 && mkdir -p "/git-dist$(dirname "$INTERP")" \
 && cp "$INTERP" "/git-dist$INTERP" \
 # Git exec-path helpers (git-remote-https, etc.)
 && GIT_EXEC_PATH="$(git --exec-path)" \
 && mkdir -p "/git-dist${GIT_EXEC_PATH}" \
 && cp -a "${GIT_EXEC_PATH}/"* "/git-dist${GIT_EXEC_PATH}/" \
 # Resolve and copy libs for every binary in git exec-path
 && find "/git-dist${GIT_EXEC_PATH}" -type f -executable | while read -r bin; do \
      ldd "$bin" 2>/dev/null \
      | awk '/=>/ {print $3}' \
      | sort -u \
      | while read -r lib; do \
          dir="/git-dist$(dirname "$lib")"; \
          mkdir -p "$dir"; \
          cp -n "$lib" "$dir/" 2>/dev/null || true; \
        done; \
    done \
 # Git needs a minimal gitconfig to work
 && printf '[safe]\n\tdirectory = *\n' > /git-dist/etc/gitconfig

# =============================================================================
# Stage 3: Minimal runtime image from scratch
# =============================================================================
FROM scratch

LABEL org.opencontainers.image.title="OpenVibely" \
      org.opencontainers.image.description="AI-powered task scheduling and automation platform" \
      org.opencontainers.image.url="https://github.com/openvibely/openvibely" \
      org.opencontainers.image.source="https://github.com/openvibely/openvibely" \
      org.opencontainers.image.licenses="MIT"

# SSL certificates for HTTPS (LLM API calls, GitHub, etc.)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Timezone data for time.Local / schedule calculations
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Passwd/group so the app can run as non-root
COPY --from=builder /etc/passwd /etc/passwd
COPY --from=builder /etc/group /etc/group

# Git binary + all its shared library dependencies
COPY --from=git-collector /git-dist/ /

# Application binary
COPY --from=builder /out/openvibely /openvibely

# Create a writable /tmp (needed by SQLite WAL, temp files, etc.)
COPY --from=builder /tmp /tmp

ENV PORT=3001 \
    DATABASE_PATH=/data/openvibely.db \
    ENVIRONMENT=production \
    GIT_EXEC_PATH=/usr/libexec/git-core

EXPOSE 3001

VOLUME ["/data"]

ENTRYPOINT ["/openvibely"]
