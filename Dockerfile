# =============================================================================
# Stage 1: Builder — compile Go binary with all generated assets
# =============================================================================
FROM golang:1.26.6-alpine AS builder

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

# Build static binary with immutable container identity.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=1970-01-01T00:00:00Z
ARG RELEASE_TRUST_ID=
ARG RELEASE_TRUST_VALUE=
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X github.com/openvibely/openvibely/internal/buildinfo.Version=${VERSION} -X github.com/openvibely/openvibely/internal/buildinfo.Commit=${COMMIT} -X github.com/openvibely/openvibely/internal/buildinfo.BuildTime=${BUILD_TIME} -X github.com/openvibely/openvibely/internal/buildinfo.Artifact=container -X github.com/openvibely/openvibely/internal/buildinfo.ReleaseKeyID=${RELEASE_TRUST_ID} -X github.com/openvibely/openvibely/internal/buildinfo.ReleasePublicKey=${RELEASE_TRUST_VALUE}" \
    -o /out/openvibely \
    ./cmd/server

# =============================================================================
# Stage 2: Coding/agent runtime — OpenVibely + common toolchains
# =============================================================================
FROM fedora:44

ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_TIME=1970-01-01T00:00:00Z
LABEL org.opencontainers.image.title="OpenVibely" \
      org.opencontainers.image.description="AI coding agent platform with common language toolchains and build utilities" \
      org.opencontainers.image.url="https://github.com/openvibely/openvibely" \
      org.opencontainers.image.source="https://github.com/openvibely/openvibely" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.created="${BUILD_TIME}" \
      org.opencontainers.image.licenses="MIT"

# Apply all pending Fedora security errata before installing toolchains so the
# base layer's pre-installed packages (glibc, openssl, ...) are current too.
RUN dnf -y upgrade --refresh --setopt=install_weak_deps=False \
 && dnf install -y --setopt=install_weak_deps=False \
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

# Remove privilege-escalation surface: the runtime always runs as UID/GID
# 10001:10001 and never needs sudo/su or any setuid/setgid helper.
RUN dnf remove -y --setopt=protected_packages= sudo \
 && dnf clean all \
 && rm -rf /var/cache/dnf \
 && find / -xdev -perm /6000 -type f -exec chmod a-s {} +

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
      '  echo "error: /data must be writable by UID/GID 10001:10001; prepare bind-mount ownership before starting the container" >&2' \
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
    OPENVIBELY_UPDATE_MODE=docker-manual \
    ENVIRONMENT=production \
    GIT_EXEC_PATH=/usr/libexec/git-core \
    HOME=/home/openvibely \
    GOPATH=/home/openvibely/go \
    XDG_RUNTIME_DIR=/tmp/openvibely-runtime \
    PATH=/home/openvibely/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

EXPOSE 3001

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["/openvibely", "healthcheck"]

VOLUME ["/data"]

WORKDIR /data

USER 10001:10001

ENTRYPOINT ["/usr/local/bin/openvibely-entrypoint"]
CMD ["/openvibely"]
