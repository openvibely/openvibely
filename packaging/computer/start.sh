#!/bin/bash
set -euo pipefail
desktop_base=${OPENVIBELY_DESKTOP_PORT:-0}
profile="$HOME/.browser"
if [ "$desktop_base" -gt 0 ]; then
  profile="$HOME/.desktops/$OPENVIBELY_DESKTOP_ID/browser"
  export DISPLAY=":$((100 + (desktop_base - 10000) / 10))"
  export XDG_CONFIG_HOME="$HOME/.desktops/$OPENVIBELY_DESKTOP_ID/config"
  export XDG_CACHE_HOME="$HOME/.desktops/$OPENVIBELY_DESKTOP_ID/cache"
  mkdir -p "$HOME/.desktops/$OPENVIBELY_DESKTOP_ID/home/Files"
  cd "$HOME/.desktops/$OPENVIBELY_DESKTOP_ID/home/Files"
fi
port() { if [ "$desktop_base" -gt 0 ]; then echo "$((desktop_base + $1))"; else echo "$2"; fi; }
cdp=$(port 1 9222); vnc=$(port 2 5900); control=$(port 3 5901); view_ws=$(port 4 6080); control_ws=$(port 5 6081); research=$(port 6 9223)
mkdir -p "$HOME/Files" "$profile"
# This volume belongs exclusively to this one desktop. Container replacement
# changes its hostname/PIDs; stale Chromium singleton links must not block it.
rm -f "$profile/SingletonLock" "$profile/SingletonSocket" "$profile/SingletonCookie"
display_number=${DISPLAY#:}
# The old X server may have been killed during container shutdown. This session
# is launched only after its previous process group has exited.
rm -f "/tmp/.X${display_number}-lock" "/tmp/.X11-unix/X${display_number}"
Xvfb "$DISPLAY" -screen 0 1440x900x24 -nolisten tcp &
for i in {1..50}; do xdpyinfo >/dev/null 2>&1 && break; sleep .1; done
xdpyinfo >/dev/null 2>&1 || exit 1
# Measured capture-to-render latency: 10 ms polling, no artificial send delay.
# Keep both viewing and control streams on the same low-latency settings.
x11vnc -display "$DISPLAY" -localhost -rfbport "$vnc" -viewonly -forever -shared -nopw -nowait_bog -wait 10 -defer 0 >/tmp/vnc-view.log 2>&1 &
x11vnc -display "$DISPLAY" -localhost -rfbport "$control" -forever -shared -nopw -nowait_bog -wait 10 -defer 0 >/tmp/vnc-control.log 2>&1 &
websockify --web=/usr/share/novnc "127.0.0.1:$view_ws" "127.0.0.1:$vnc" >/tmp/websockify-view.log 2>&1 &
websockify "127.0.0.1:$control_ws" "127.0.0.1:$control" >/tmp/websockify-control.log 2>&1 &
dbus-run-session -- xfce4-session >/tmp/xfce.log 2>&1 &
chrome=$(node -e 'console.log(require("/opt/browser/node_modules/playwright").chromium.executablePath())')
"$chrome" --no-sandbox --disable-dev-shm-usage --no-first-run --no-default-browser-check --password-store=basic --remote-debugging-address=127.0.0.1 --remote-debugging-port="$cdp" --user-data-dir="$profile" --window-size=1280,800 about:blank >/tmp/browser.log 2>&1 &
browser_pid=$!
"$chrome" --headless=new --no-sandbox --disable-dev-shm-usage --no-first-run --host-resolver-rules="MAP * ~NOTFOUND" --remote-debugging-address=127.0.0.1 --remote-debugging-port="$research" --user-data-dir="/tmp/research-browser-$research" about:blank >/tmp/research-browser.log 2>&1 &
research_pid=$!
openvibely-computer &
bridge_pid=$!
if [ "$desktop_base" -eq 0 ]; then node /usr/local/lib/openvibely-auth-sync.cjs & fi
shutdown() {
  trap - TERM INT
  if [ "$desktop_base" -eq 0 ]; then curl --max-time 12 -s -X POST http://127.0.0.1:8090/desktop-shutdown >/dev/null || true; fi
  # Browser.close flushes cookies and profile databases before Docker stops X11.
  OPENVIBELY_CDP_PORT="$cdp" timeout 8s node -e '(async()=>{const b=await require("/opt/browser/node_modules/playwright").chromium.connectOverCDP("http://127.0.0.1:"+process.env.OPENVIBELY_CDP_PORT);const s=await b.newBrowserCDPSession();await s.send("Browser.close").catch(()=>{});})().catch(()=>process.exit(1))' >/dev/null 2>&1 || true
  for i in {1..50}; do kill -0 "$browser_pid" 2>/dev/null || break; sleep .1; done
  kill "$bridge_pid" "$browser_pid" "$research_pid" 2>/dev/null || true
  wait "$bridge_pid" 2>/dev/null || true
}
trap 'shutdown; exit 0' TERM INT
wait "$bridge_pid" || true
shutdown
