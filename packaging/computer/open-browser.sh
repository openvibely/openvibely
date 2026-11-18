#!/bin/bash
# Use the same task profile and CDP endpoint for startup, desktop links and relaunches.
set -euo pipefail
[[ "$HOME" =~ ^/home/ubuntu/\.desktops/task-[a-z0-9-]+/home$ ]] || exit 1
display_number=${DISPLAY#:}
display_number=${display_number%%.*}
[[ "$display_number" =~ ^[0-9]+$ && "$display_number" -ge 100 ]] || exit 1
profile="${HOME%/home}/browser"
IFS= read -r browser < /usr/local/lib/openvibely-chromium-path
[[ "$browser" = /* && -x "$browser" ]] || exit 1
exec "$browser" --disable-dev-shm-usage --no-first-run --no-default-browser-check --password-store=basic --remote-debugging-address=127.0.0.1 --remote-debugging-port="$((10001 + (display_number - 100) * 10))" --user-data-dir="$profile" --window-size=1280,800 "$@"
