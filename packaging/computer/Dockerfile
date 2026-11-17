FROM golang:1.27.1 AS builder
WORKDIR /src
COPY cmd/computer/ ./cmd/computer/
COPY pkg/agenttools/ ./pkg/agenttools/
COPY pkg/executionenv/ ./pkg/executionenv/
COPY pkg/gitidentity/ ./pkg/gitidentity/
COPY pkg/toolschema/ ./pkg/toolschema/
RUN go mod init github.com/openvibely/openvibely && go get github.com/coder/websocket@v1.8.15 github.com/santhosh-tekuri/jsonschema/v5@v5.3.1 && CGO_ENABLED=0 go build -o /computer ./cmd/computer
RUN mkdir -p /licenses/coder-websocket /licenses/go /licenses/jsonschema && \
    cp /go/pkg/mod/github.com/coder/websocket@v1.8.15/LICENSE.txt /licenses/coder-websocket/ && \
    cp /go/pkg/mod/github.com/santhosh-tekuri/jsonschema/v5@v5.3.1/LICENSE /licenses/jsonschema/ && \
    cp /usr/local/go/LICENSE /usr/local/go/PATENTS /licenses/go/ && \
    cp go.mod go.sum /licenses/ && go version > /licenses/go/version.txt

FROM node:22-bookworm-slim AS node-runtime

FROM ubuntu:24.04
# Use a supported Node runtime on both amd64 and arm64; Ubuntu 24.04 ships Node 18.
COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
COPY --from=node-runtime /usr/local/LICENSE /usr/share/licenses/openvibely-computer/node/LICENSE
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && \
    ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ENV DEBIAN_FRONTEND=noninteractive DISPLAY=:99 PLAYWRIGHT_BROWSERS_PATH=/opt/browsers
RUN apt-get update && apt-get install -y --no-install-recommends x11vnc websockify git curl python3 python3-venv jq ripgrep zip unzip xfce4 xfce4-terminal x11-utils wmctrl xvfb xdotool imagemagick dbus-x11 ca-certificates tzdata fonts-dejavu tini util-linux iproute2 iptables slirp4netns xauth && npm install --prefix /opt/browser playwright@1.63.0 && /opt/browser/node_modules/.bin/playwright install --with-deps --no-shell chromium && chmod -R a+rX /opt/browsers && rm -rf /opt/browsers/ffmpeg-* /var/lib/apt/lists/* /root/.npm
# Playwright 1.63 supplies Chrome for Testing with H.264/AAC on Linux arm64 too.
# Only the visible browser executable is used; recordings and headless shell are not shipped.
# Resolve the architecture-specific executable once, rather than loading Playwright
# for every task desktop startup.
RUN node -e 'const fs = require("fs"); const path = require("/opt/browser/node_modules/playwright").chromium.executablePath(); fs.accessSync(path, fs.constants.X_OK); fs.writeFileSync("/usr/local/lib/openvibely-chromium-path", path + "\n", {mode: 0o644});'
# Verify real H.264 video and AAC audio playback before shipping either architecture.
COPY packaging/computer/media_test.cjs /tmp/computer-media-test/media_test.cjs
COPY packaging/computer/testdata/h264-aac.mp4 /tmp/computer-media-test/testdata/h264-aac.mp4
RUN node --test /tmp/computer-media-test/media_test.cjs && rm -rf /tmp/computer-media-test
COPY --from=builder /licenses/ /usr/share/licenses/openvibely-computer/
COPY packaging/computer/licenses.cjs /tmp/openvibely-licenses.cjs
RUN node /tmp/openvibely-licenses.cjs && rm /tmp/openvibely-licenses.cjs
COPY packaging/computer/SOURCE-OFFER.txt /usr/share/licenses/openvibely-computer/SOURCE-OFFER.txt
# The trusted supervisor creates task namespaces and drops all capabilities in
# desktop applications and shells. Only it runs as root.
ENV HOME=/home/ubuntu
COPY --from=builder /computer /usr/local/bin/openvibely-computer
COPY packaging/computer/browser.cjs /usr/local/lib/openvibely-browser.cjs
COPY packaging/computer/start.sh /usr/local/bin/start-computer
COPY packaging/computer/isolate.sh /usr/local/bin/start-isolated-desktop
RUN chmod +x /usr/local/bin/start-computer /usr/local/bin/start-isolated-desktop
USER root
WORKDIR /home/ubuntu
EXPOSE 8090
ENTRYPOINT ["/usr/bin/tini","--","/usr/local/bin/start-computer"]
