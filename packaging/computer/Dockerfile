FROM golang:1.27.4 AS builder
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

FROM debian:trixie-slim
# Keep the same Node runtime for both supported architectures.
COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/npm
COPY --from=node-runtime /usr/local/LICENSE /usr/share/licenses/openvibely-computer/node/LICENSE
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && \
    ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx
ENV DEBIAN_FRONTEND=noninteractive DISPLAY=:99
# Debian installs the native Chromium package for the image's target architecture.
RUN apt-get update && apt-get install -y --no-install-recommends chromium x11vnc websockify git curl python3 python3-venv jq ripgrep zip unzip xfce4 xfce4-terminal x11-utils wmctrl xvfb xdotool imagemagick dbus-x11 ca-certificates tzdata fonts-dejavu tini util-linux iproute2 iptables slirp4netns xauth && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install --prefix /opt/browser playwright@1.63.0 && rm -rf /var/lib/apt/lists/* /root/.npm
# Preserve the existing volume layout; task accounts are created by the supervisor.
RUN mkdir -p /home/ubuntu && printf '/usr/bin/chromium\n' > /usr/local/lib/openvibely-chromium-path
COPY packaging/computer/open-browser.sh /usr/local/bin/openvibely-browser
RUN chmod +x /usr/local/bin/openvibely-browser && \
    sed -i 's|Exec=/usr/bin/chromium|Exec=/usr/local/bin/openvibely-browser|;s|Exec=chromium|Exec=/usr/local/bin/openvibely-browser|' /usr/share/applications/chromium.desktop && \
    update-alternatives --install /usr/bin/x-www-browser x-www-browser /usr/local/bin/openvibely-browser 200 && \
    mkdir -p /etc/xdg/xfce4 && printf '[Desktop Entry]\nVersion=1.0\nType=X-XFCE-Helper\nName=Chromium\nIcon=chromium\nX-XFCE-Category=WebBrowser\nX-XFCE-Commands=/usr/local/bin/openvibely-browser;\nX-XFCE-CommandsWithParameter=/usr/local/bin/openvibely-browser "%%s";\n' > /usr/share/xfce4/helpers/openvibely-chromium.desktop && \
    printf 'WebBrowser=openvibely-chromium\nTerminalEmulator=xfce4-terminal\n' > /etc/xdg/xfce4/helpers.rc && \
    printf '[Default Applications]\nx-scheme-handler/http=chromium.desktop\nx-scheme-handler/https=chromium.desktop\ntext/html=chromium.desktop\n' > /etc/xdg/mimeapps.list
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
