FROM golang:1.27.1 AS builder
WORKDIR /src
COPY cmd/computer/ ./
RUN go mod init openvibely.local/computer && go get github.com/coder/websocket@v1.8.15 && CGO_ENABLED=0 go build -o /computer .
RUN mkdir -p /licenses/coder-websocket /licenses/go && \
    cp /go/pkg/mod/github.com/coder/websocket@v1.8.15/LICENSE.txt /licenses/coder-websocket/ && \
    cp /usr/local/go/LICENSE /usr/local/go/PATENTS /licenses/go/ && \
    cp go.mod go.sum /licenses/ && go version > /licenses/go/version.txt

FROM ubuntu:24.04
ENV DEBIAN_FRONTEND=noninteractive DISPLAY=:99 PLAYWRIGHT_BROWSERS_PATH=/opt/browsers
RUN apt-get update && apt-get install -y --no-install-recommends x11vnc websockify git curl python3 python3-venv jq ripgrep zip unzip xfce4 xfce4-terminal x11-utils wmctrl xvfb xdotool imagemagick dbus-x11 nodejs npm ca-certificates tzdata fonts-dejavu tini util-linux iproute2 iptables slirp4netns xauth && npm install --prefix /opt/browser playwright@1.58.2 && /opt/browser/node_modules/.bin/playwright install --with-deps --no-shell chromium && chmod -R a+rX /opt/browsers && rm -rf /opt/browsers/ffmpeg-* /var/lib/apt/lists/* /root/.npm
# Only the visible Chromium executable is used; recordings and headless shell are not shipped.
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
