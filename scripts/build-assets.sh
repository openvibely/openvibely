#!/usr/bin/env bash
# Builds the front-end assets embedded in the OpenVibely binary (web/static/dist) from
# pinned downloads. No Node: CSS is built with Tailwind's standalone binary.
#
#   scripts/build-assets.sh                 # download pinned inputs, rebuild dist/
#   scripts/build-assets.sh --check         # rebuild, then fail if dist/ changed (CI)
#   scripts/build-assets.sh --check-latest  # show the newest published version of each pin
#
# Updating a library: change its version and URL below, set its sha256 to the new file's
# (`curl -sL <url> | shasum -a 256`), run this script, and commit the script and dist/.
#
# Rebuild whenever templates change: the stylesheets only contain classes that appear
# written out in full in web/templates or internal Go files.
#
# Output keeps the cascade the old CDN setup had:
#   app.css            DaisyUI (unused components removed), loaded where the DaisyUI CDN
#                      stylesheet was
#   app-utilities.css  Tailwind's reset and utility classes, loaded last in <head> where
#                      the Tailwind CDN script injected them
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
assets="$root/web/assets"
cache="$assets/.cache"
dist="$root/web/static/dist"
mode=${1:-}

TAILWIND_VERSION=3.4.17
DAISYUI_VERSION=4.12.14

# dist path | source URL | sha256
FILES=(
  "vendor/htmx.min.js|https://cdn.jsdelivr.net/npm/htmx.org@2.0.4/dist/htmx.min.js|e209dda5c8235479f3166defc7750e1dbcd5a5c1808b7792fc2e6733768fb447"
  "vendor/idiomorph-ext.min.js|https://cdn.jsdelivr.net/npm/idiomorph@0.3.0/dist/idiomorph-ext.min.js|763ad5ebd0963ea9436cb480f303fc4b7e543c37c649925f032c568b4dbab7e6"
  "vendor/mermaid.min.js|https://cdn.jsdelivr.net/npm/mermaid@11.17.0/dist/mermaid.min.js|8d8e0eec56d3a83b4b3c87f42050845546dee93ebe1875d2117c12e6947c0cb3"
  "vendor/mermaid.LICENSE|https://cdn.jsdelivr.net/npm/mermaid@11.17.0/LICENSE|ec9fb67dcb25eccc416ed56e1aab819222c805a2a4bfe4cb19e7556bf2ffde80"
  "vendor/marked.min.js|https://cdn.jsdelivr.net/npm/marked@15.0.4/marked.min.js|74c9f2e02c180c3e6caa09881a0b24032c86473af90acb1c87b6dc7255d491dd"
  "vendor/highlight.min.js|https://cdn.jsdelivr.net/npm/@highlightjs/cdn-assets@11.11.1/highlight.min.js|c4a399dd6f488bc97a3546e3476747b3e714c99c57b9473154c6fb8d259b9381"
  "vendor/highlight-github-dark.min.css|https://cdn.jsdelivr.net/npm/@highlightjs/cdn-assets@11.11.1/styles/github-dark.min.css|9f208d022102b1d0c7aebfecd8e42ca7997d5de636649d2b31ea63093d809019"
  "vendor/chart.umd.min.js|https://cdn.jsdelivr.net/npm/chart.js@4.4.1/dist/chart.umd.js|74401d738dd3e03ee5dfb3b6841210fe2c4ead8a960c4011ca4ba0b78a9fd8f3"
)
DAISYUI_URL="https://cdn.jsdelivr.net/npm/daisyui@${DAISYUI_VERSION}/dist/full.css"
DAISYUI_SHA256=23631336a8036404206b974e543c2e7842d250ff10b522fb0d8d0df6f7bbadb3

if [[ "$mode" == "--check-latest" ]]; then
  latest() { curl -fsSL "https://registry.npmjs.org/$1/latest" | sed -n 's/.*"version":"\([^"]*\)".*/\1/p'; }
  printf '%-26s %-10s %s\n' package pinned latest
  printf '%-26s %-10s %s\n' tailwindcss "$TAILWIND_VERSION" "$(latest tailwindcss)"
  printf '%-26s %-10s %s\n' daisyui "$DAISYUI_VERSION" "$(latest daisyui)"
  seen=" "
  for entry in "${FILES[@]}"; do
    url=${entry#*|}; url=${url%%|*}
    path=${url#https://cdn.jsdelivr.net/npm/}
    if [[ "$path" == @* ]]; then spec=$(cut -d/ -f1-2 <<<"$path"); else spec=${path%%/*}; fi
    name=${spec%@*}; version=${spec##*@}
    [[ "$seen" == *" $name "* ]] && continue
    seen+="$name "
    printf '%-26s %-10s %s\n' "$name" "$version" "$(latest "$name")"
  done
  exit 0
fi

case "$(uname -s)-$(uname -m)" in
  Darwin-arm64)              tw_platform=macos-arm64; tw_sha=a1d0c7985759accca0bf12e51ac1dcbf0f6cf2fffb62e6e0f62d091c477a10a3 ;;
  Darwin-x86_64)             tw_platform=macos-x64;   tw_sha=6cbdad74be776c087ffa5e9a057512c54898f9fe8828d3362212dfe32fc933a3 ;;
  Linux-x86_64)              tw_platform=linux-x64;   tw_sha=7d24f7fa191d2193b78cd5f5a42a6093e14409521908529f42d80b11fde1f1d4 ;;
  Linux-aarch64|Linux-arm64) tw_platform=linux-arm64; tw_sha=69b1378b8133192d7d2feb12a116fa12d035594f58db3eff215879e4ad8cf39b ;;
  *) echo "unsupported platform $(uname -s)-$(uname -m)" >&2; exit 1 ;;
esac
TAILWIND_URL="https://github.com/tailwindlabs/tailwindcss/releases/download/v${TAILWIND_VERSION}/tailwindcss-${tw_platform}"

# fetch downloads url into the cache (keyed by checksum), verifies it, and prints its path.
fetch() {
  local url=$1 sha=$2 path="$cache/$2"
  if [[ ! -f "$path" ]] || [[ "$(shasum -a 256 "$path" | cut -d' ' -f1)" != "$sha" ]]; then
    mkdir -p "$cache"
    curl -fsSL --retry 3 -o "$path.tmp" "$url"
    local got
    got=$(shasum -a 256 "$path.tmp" | cut -d' ' -f1)
    if [[ "$got" != "$sha" ]]; then
      rm -f "$path.tmp"
      echo "checksum mismatch for $url: got $got, pinned $sha" >&2
      exit 1
    fi
    mv "$path.tmp" "$path"
  fi
  echo "$path"
}

tailwind=$(fetch "$TAILWIND_URL" "$tw_sha")
chmod +x "$tailwind"
daisyui=$(fetch "$DAISYUI_URL" "$DAISYUI_SHA256")

rm -rf "$dist"
mkdir -p "$dist/vendor"
cp "$assets/kanban.js" "$dist/kanban.js"
for entry in "${FILES[@]}"; do
  IFS='|' read -r dest url sha <<<"$entry"
  cp "$(fetch "$url" "$sha")" "$dist/$dest"
done

# Tailwind drops rules in @layer components whose classes never appear in the content
# files, which removes the DaisyUI components the app does not use.
{
  echo '@tailwind components;'
  echo '@layer components {'
  cat "$daisyui"
  echo '}'
} >"$cache/app.input.css"
printf '@tailwind base;\n@tailwind utilities;\n' >"$cache/app-utilities.input.css"

cd "$assets"
"$tailwind" -c tailwind.config.js -i "$cache/app.input.css" -o "$dist/app.css" --minify 2>/dev/null &
app_pid=$!
"$tailwind" -c tailwind.config.js -i "$cache/app-utilities.input.css" -o "$dist/app-utilities.css" --minify 2>/dev/null &
utilities_pid=$!
app_status=0
utilities_status=0
wait "$app_pid" || app_status=$?
wait "$utilities_pid" || utilities_status=$?
if [[ "$app_status" -ne 0 || "$utilities_status" -ne 0 ]]; then
  echo "Tailwind asset build failed (app.css: $app_status, app-utilities.css: $utilities_status)." >&2
  exit 1
fi

if [[ "$mode" == "--check" ]]; then
  if [[ -n "$(git -C "$root" status --porcelain -- web/static/dist)" ]]; then
    echo "web/static/dist is out of date; run scripts/build-assets.sh and commit the result." >&2
    git -C "$root" status --short -- web/static/dist >&2
    exit 1
  fi
fi
echo "built $(du -sh "$dist" | cut -f1) of assets in web/static/dist"
