# =============================================================================
# Stage 1: Builder — compile Go binary with all generated assets
# =============================================================================
FROM golang:1.26.1-alpine AS builder

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
# Stage 2: Collect Git/CLI tools and dynamic library dependencies
# =============================================================================
FROM alpine:3.21 AS git-collector

RUN apk add --no-cache git bash grep busybox binutils coreutils findutils sed gawk util-linux tzdata ca-certificates

# Copy git + shell/tool binaries and discover all shared libraries they need.
# This keeps the scratch runtime usable for model bash-style tool calls.
RUN mkdir -p /git-dist/usr/bin /git-dist/bin /git-dist/usr/lib /git-dist/etc \
 && for bin in git bash sh grep sed awk find xargs ls cat cp mv rm mkdir pwd head tail wc sort uniq cut tr env printenv dirname basename; do \
      path="$(command -v "$bin" 2>/dev/null || true)"; \
      [ -n "$path" ] || continue; \
      mkdir -p "/git-dist$(dirname "$path")"; \
      cp "$path" "/git-dist$path"; \
    done \
 # Ensure canonical shell paths exist for exec and debugging
 && bash_path="$(command -v bash 2>/dev/null || true)" \
 && [ -n "$bash_path" ] && cp -f "$bash_path" /git-dist/bin/bash || true \
 && sh_path="$(command -v sh 2>/dev/null || true)" \
 && [ -n "$sh_path" ] && cp -f "$sh_path" /git-dist/bin/sh || true \
 # Git exec-path helpers (git-remote-https, etc.)
 && GIT_EXEC_PATH="$(git --exec-path)" \
 && mkdir -p "/git-dist${GIT_EXEC_PATH}" \
 && cp -a "${GIT_EXEC_PATH}/"* "/git-dist${GIT_EXEC_PATH}/" \
 # Git templates (avoids: warning: templates not found in /usr/share/git-core/templates)
 && if [ -d /usr/share/git-core/templates ]; then \
      mkdir -p /git-dist/usr/share/git-core/templates; \
      cp -a /usr/share/git-core/templates/. /git-dist/usr/share/git-core/templates/; \
    fi \
 # Resolve and copy libs + interpreter for all shipped executables
 && find /git-dist/bin /git-dist/usr/bin "/git-dist${GIT_EXEC_PATH}" -type f -executable 2>/dev/null | while read -r bin; do \
      ldd "$bin" 2>/dev/null \
      | awk '/=>/ {print $3}' \
      | sort -u \
      | while read -r lib; do \
          [ -f "$lib" ] || continue; \
          dir="/git-dist$(dirname "$lib")"; \
          mkdir -p "$dir"; \
          cp -n "$lib" "$dir/" 2>/dev/null || true; \
        done; \
      interp="$(readelf -l "$bin" 2>/dev/null | grep 'program interpreter' | sed 's/.*: \(.*\)]/\1/' || true)"; \
      if [ -n "$interp" ] && [ -f "$interp" ]; then \
        mkdir -p "/git-dist$(dirname "$interp")"; \
        cp -n "$interp" "/git-dist$interp" 2>/dev/null || true; \
      fi; \
    done \
 # Minimal gitconfig for safe-directory behavior
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
# Some git builds expect this exact path
COPY --from=builder /etc/ssl/cert.pem /etc/ssl/cert.pem

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
    PROJECT_REPO_ROOT=/data/repos \
    ENVIRONMENT=production \
    GIT_EXEC_PATH=/usr/libexec/git-core \
    PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

EXPOSE 3001

VOLUME ["/data"]

WORKDIR /data

ENTRYPOINT ["/openvibely"]
