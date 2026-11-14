#!/bin/bash
# Export dependency sources for a built computer image. Publish the output next
# to that image; run separately for each distributed architecture/image ID.
set -euo pipefail
if [[ $# != 2 ]]; then
  echo 'Usage: scripts/export-computer-sources.sh IMAGE EMPTY_OUTPUT_DIRECTORY' >&2
  exit 1
fi
image_id=$(docker image inspect --format '{{.Id}}' "$1")
mkdir -p "$2"
output=$(cd "$2" && pwd)
if [[ -n "$(ls -A "$output")" ]]; then
  echo 'Source export requires an empty output directory.' >&2
  exit 1
fi
docker run --rm --entrypoint /usr/local/bin/export-computer-sources \
  --env "OPENVIBELY_SOURCE_IMAGE_ID=$image_id" \
  --mount "type=bind,source=$output,target=/source-export" \
  "$image_id" /source-export
test -f "$output/COMPLETE"
printf 'Sources for %s exported to %s\n' "$image_id" "$output"
