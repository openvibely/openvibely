#!/bin/bash
# Runs inside the exact image being distributed; never substitutes newer sources.
set -euo pipefail
out=${1:?usage: export-computer-sources OUTPUT_DIRECTORY}
notices=/usr/share/licenses/openvibely-computer
mkdir -p "$out"
if [[ -n "$(ls -A "$out")" ]]; then
  echo 'Source export requires an empty output directory.' >&2
  exit 1
fi
# COMPLETE is written only after every download and checksum succeeds.
cp -a "$notices" "$out/notices"
printf '%s\n' "${OPENVIBELY_SOURCE_IMAGE_ID:?image identity is required}" > "$out/image-id.txt"
dpkg --print-architecture > "$out/architecture.txt"
cp /etc/os-release "$out/os-release"
mkdir -p "$out/ubuntu" "$out/chromium" "$out/npm"
# Ubuntu 24.04 uses deb822 sources. Preserve repositories and signature settings.
python3 - <<'PY'
from pathlib import Path
p = Path('/etc/apt/sources.list.d/ubuntu.sources')
text = p.read_text()
if 'Types: deb\n' not in text:
    raise SystemExit('Unsupported Ubuntu source configuration; refusing an incomplete export')
p.with_name('openvibely-source.sources').write_text(text.replace('Types: deb\n', 'Types: deb-src\n'))
PY
apt-get update
while IFS=$'\t' read -r package version; do
  destination="$out/ubuntu/$package"
  mkdir -p "$destination"
  (cd "$destination" && apt-get source --download-only --only-source "$package=$version")
done < <(cut -f4,5 "$notices/ubuntu-packages.tsv" | LC_ALL=C sort -u)
version=$(cat "$notices/chromium-version.txt")
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]] || exit 1
url="https://commondatastorage.googleapis.com/chromium-browser-official/chromium-$version.tar.xz"
printf '%s\n' "$url" > "$out/chromium/source-url.txt"
curl --fail --location --retry 3 --output "$out/chromium/chromium-$version.tar.xz" "$url"
# These are the installed, unmodified JavaScript packages, including their notices.
tar -czf "$out/npm/installed-packages.tar.gz" -C /opt/browser package.json package-lock.json node_modules
python3 - "$out" <<'PY'
import hashlib
import sys
from pathlib import Path
root = Path(sys.argv[1])
with (root / 'SHA256SUMS').open('w') as manifest:
    for path in sorted(root.rglob('*')):
        if path.is_file() and path.name != 'SHA256SUMS':
            with path.open('rb') as source:
                digest = hashlib.file_digest(source, 'sha256').hexdigest()
            manifest.write(f'{digest}  {path.relative_to(root)}\n')
PY
printf 'Source export completed for %s\n' "$OPENVIBELY_SOURCE_IMAGE_ID" > "$out/COMPLETE"
