# =============================================================================
# Stage 1: Builder — compile Go binary with all generated assets
# =============================================================================
FROM golang:1.26.5-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /src

# Cache module downloads
COPY go.mod go.sum ./
RUN go mod download

# Install templ and swag using the versions declared in go.mod
RUN go install github.com/a-h/templ/cmd/templ@$(go list -m -f '{{.Version}}' github.com/a-h/templ) \
 && go install github.com/swaggo/swag/cmd/swag@$(go list -m -f '{{.Version}}' github.com/swaggo/swag)

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
# Stage 2: Coding/agent runtime — OpenVibely + common toolchains
# =============================================================================
FROM fedora:44

LABEL org.opencontainers.image.title="OpenVibely" \
      org.opencontainers.image.description="AI coding agent platform with common language toolchains and build utilities" \
      org.opencontainers.image.url="https://github.com/openvibely/openvibely" \
      org.opencontainers.image.source="https://github.com/openvibely/openvibely" \
      org.opencontainers.image.licenses="MIT"

RUN dnf install -y --setopt=install_weak_deps=False \
      bash \
      binutils \
      ca-certificates \
      cargo \
      coreutils \
      curl \
      diffutils \
      file \
      findutils \
      gawk \
      gcc \
      gcc-c++ \
      git \
      golang \
      grep \
      gzip \
      java-25-openjdk-devel \
      jq \
      make \
      nodejs \
      npm \
      openssh-clients \
      patch \
      pkgconf-pkg-config \
      procps-ng \
      python3 \
      python3-devel \
      python3-pip \
      ruby \
      ruby-devel \
      rust \
      ripgrep \
      sed \
      shadow-utils \
      tar \
      tzdata \
      unzip \
      util-linux \
      wget \
      which \
      xz \
      zip \
 && dnf clean all \
 && rm -rf /var/cache/dnf

ARG COREPACK_VERSION=0.34.0
ARG TYPESCRIPT_VERSION=5.9.3
RUN npm install --global \
      "corepack@${COREPACK_VERSION}" \
      "typescript@${TYPESCRIPT_VERSION}" \
 && rm -rf /root/.npm

RUN useradd -m -u 10001 -s /bin/bash openvibely \
 && mkdir -p \
      /data \
      /tmp/openvibely-runtime \
      /home/openvibely/go \
 && chown -R openvibely:openvibely \
      /data \
      /tmp/openvibely-runtime \
      /home/openvibely \
 && chmod 700 /tmp/openvibely-runtime \
 && printf '[safe]\n\tdirectory = *\n' > /etc/gitconfig

RUN printf '%s\n' \
      '#!/usr/bin/env bash' \
      'set -euo pipefail' \
      '' \
      'if [ ! -w /data ]; then' \
      '  echo "error: /data must be writable by UID/GID 10001:10001; prepare bind-mount ownership or migrate the existing volume" >&2' \
      '  exit 1' \
      'fi' \
      '' \
      'exec "$@"' \
      > /usr/local/bin/openvibely-entrypoint \
 && chmod +x /usr/local/bin/openvibely-entrypoint

# Application binary
COPY --from=builder /out/openvibely /openvibely

ENV PORT=3001 \
    OPENVIBELY_APP_DATA_DIR=/data \
    DATABASE_PATH=/data/openvibely.db \
    PROJECT_REPO_ROOT=/data/repos \
    ENVIRONMENT=production \
    GIT_EXEC_PATH=/usr/libexec/git-core \
    HOME=/home/openvibely \
    GOPATH=/home/openvibely/go \
    XDG_RUNTIME_DIR=/tmp/openvibely-runtime \
    PATH=/home/openvibely/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

EXPOSE 3001

VOLUME ["/data"]

WORKDIR /data

USER 10001:10001

ENTRYPOINT ["/usr/local/bin/openvibely-entrypoint"]
CMD ["/openvibely"]
