#!/bin/bash
set -euo pipefail
desktop_base=${OPENVIBELY_DESKTOP_PORT:-0}
if [ "$desktop_base" -eq 0 ]; then
  umask 077
  [[ ! -L /home/ubuntu && ! -L /home/ubuntu/.desktops ]] || exit 1
  mkdir -p /run/ov-desktops /home/ubuntu/.desktops
  # Legacy shared profiles remain preserved but are not exposed to task users.
  find /home/ubuntu -mindepth 1 -maxdepth 1 ! -name .desktops ! -type l -exec chmod go-rwx -- {} +
  chown root:root /home/ubuntu /home/ubuntu/.desktops
  chmod 0711 /home/ubuntu
  chmod 0700 /run/ov-desktops
  # Lock every retained task before serving requests. Otherwise an already
  # migrated task could read an older task that has not started/migrated yet.
  find /home/ubuntu/.desktops -mindepth 1 -maxdepth 1 -type d -exec chown root:root -- {} + -exec chmod 0700 -- {} +
  chmod 0711 /home/ubuntu/.desktops
  exec openvibely-computer
fi
[ "${OPENVIBELY_ISOLATED:-}" = 1 ] || exit 1
base="/home/ubuntu/.desktops/$OPENVIBELY_DESKTOP_ID"
run_desktop() {
 setpriv --reuid="$OPENVIBELY_DESKTOP_UID" --regid="$OPENVIBELY_DESKTOP_UID" --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs env -i PATH=/usr/local/bin:/usr/bin:/bin HOME="$base/home" DISPLAY="$DISPLAY" XAUTHORITY="$XAUTHORITY" XDG_CONFIG_HOME="$base/config" XDG_CACHE_HOME="$base/cache" TZ="${TZ:-UTC}" LANG=C.UTF-8 PLAYWRIGHT_BROWSERS_PATH=/opt/browsers "$@"
}
# Only isolated task desktops reach this point; the supervisor execs above.
[ "$desktop_base" -gt 0 ] || exit 1
profile="$base/browser"
export DISPLAY=":$((100 + (desktop_base - 10000) / 10))"
export XDG_CONFIG_HOME="$base/config"
export XDG_CACHE_HOME="$base/cache"
[[ ! -L "$base/home/Files" ]] || exit 1
mkdir -p "$base/home/Files"
cd "$base/home/Files"
cdp=$((desktop_base + 1)); vnc=$((desktop_base + 2)); control=$((desktop_base + 3)); view_ws=$((desktop_base + 4)); control_ws=$((desktop_base + 5))
mkdir -p "$HOME/Files" "$profile"
# This volume belongs exclusively to this one desktop. Container replacement
# changes its hostname/PIDs; stale Chromium singleton links must not block it.
rm -f "$profile/SingletonLock" "$profile/SingletonSocket" "$profile/SingletonCookie"
display_number=${DISPLAY#:}
# The old X server may have been killed during container shutdown. This session
# is launched only after its previous process group has exited.
rm -f "/tmp/.X${display_number}-lock" "/tmp/.X11-unix/X${display_number}"
export XAUTHORITY="/tmp/ov-display-authority"
touch "$XAUTHORITY"
chmod 0600 "$XAUTHORITY"
xauth -f "$XAUTHORITY" add "$DISPLAY" . "$(mcookie)"
chown "0:$OPENVIBELY_DESKTOP_UID" "$XAUTHORITY"
chmod 0640 "$XAUTHORITY"
chown "$OPENVIBELY_SHELL_UID:$OPENVIBELY_DESKTOP_UID" "$base/home/Files"
chmod 0770 "$base/home/Files"
run_desktop Xvfb "$DISPLAY" -auth "$XAUTHORITY" -screen 0 1440x900x24 -nolisten tcp &
for i in {1..50}; do xdpyinfo >/dev/null 2>&1 && break; sleep .1; done
xdpyinfo >/dev/null 2>&1 || exit 1
# Measured capture-to-render latency: 10 ms polling, no artificial send delay.
# Keep both viewing and control streams on the same low-latency settings.
run_desktop x11vnc -noshm -display "$DISPLAY" -localhost -rfbport "$vnc" -viewonly -forever -shared -nopw -nowait_bog -wait 10 -defer 0 >"/run/ov-desktops/$OPENVIBELY_DESKTOP_ID/vnc-view.log" 2>&1 &
run_desktop x11vnc -noshm -display "$DISPLAY" -localhost -rfbport "$control" -forever -shared -nopw -nowait_bog -wait 10 -defer 0 >"/run/ov-desktops/$OPENVIBELY_DESKTOP_ID/vnc-control.log" 2>&1 &
run_desktop websockify --web=/usr/share/novnc "127.0.0.1:$view_ws" "127.0.0.1:$vnc" >"/run/ov-desktops/$OPENVIBELY_DESKTOP_ID/websockify-view.log" 2>&1 &
run_desktop websockify "127.0.0.1:$control_ws" "127.0.0.1:$control" >"/run/ov-desktops/$OPENVIBELY_DESKTOP_ID/websockify-control.log" 2>&1 &
run_desktop dbus-run-session -- xfce4-session >"/run/ov-desktops/$OPENVIBELY_DESKTOP_ID/xfce.log" 2>&1 &
chrome=$(node -e 'console.log(require("/opt/browser/node_modules/playwright").chromium.executablePath())')
run_desktop "$chrome" --no-sandbox --disable-dev-shm-usage --no-first-run --no-default-browser-check --password-store=basic --remote-debugging-address=127.0.0.1 --remote-debugging-port="$cdp" --user-data-dir="$profile" --window-size=1280,800 about:blank >"/run/ov-desktops/$OPENVIBELY_DESKTOP_ID/browser.log" 2>&1 &
browser_pid=$!
openvibely-computer &
bridge_pid=$!

shutdown() {
  trap - TERM INT
  # Browser.close flushes cookies and profile databases before Docker stops X11.
  OPENVIBELY_CDP_PORT="$cdp" timeout 8s setpriv --reuid="$OPENVIBELY_DESKTOP_UID" --regid="$OPENVIBELY_DESKTOP_UID" --clear-groups --bounding-set=-all --inh-caps=-all --ambient-caps=-all --no-new-privs node -e '(async()=>{const b=await require("/opt/browser/node_modules/playwright").chromium.connectOverCDP("http://127.0.0.1:"+process.env.OPENVIBELY_CDP_PORT);const s=await b.newBrowserCDPSession();await s.send("Browser.close").catch(()=>{});})().catch(()=>process.exit(1))' >/dev/null 2>&1 || true
  for i in {1..50}; do kill -0 "$browser_pid" 2>/dev/null || break; sleep .1; done
  kill "$bridge_pid" "$browser_pid" 2>/dev/null || true
  wait "$bridge_pid" 2>/dev/null || true
}
trap 'shutdown; exit 0' TERM INT
wait "$bridge_pid" || true
shutdown
