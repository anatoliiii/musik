#!/usr/bin/env bash
# Pass Compose global options as arguments; defaults to prebuilt CI images.
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ $# -eq 0 ]]; then
  set -- -f docker-compose.images.yml
fi
compose=(docker compose "$@")
"${compose[@]}" config --quiet
# Download everything before interrupting the running application.
"${compose[@]}" pull
# A registry cannot change three mutable tags atomically. Reject mixed revisions
# (including a partially published release) before stopping any running writer.
mapfile -t images < <("${compose[@]}" config --images player worker migrate)
if [[ ${#images[@]} -ne 3 ]]; then
  printf '%s\n' 'Expected three application images in the resolved Compose configuration.' >&2
  exit 1
fi
revision=""
for image in "${images[@]}"; do
  current=$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image")
  if [[ -z "$current" || "$current" == '<no value>' || ( -n "$revision" && "$revision" != "$current" ) ]]; then
    printf '%s\n' 'Missing or different CI revisions; retry after CI finishes publishing.' >&2
    exit 1
  fi
  revision="$current"
done
"${compose[@]}" stop -t 60 player worker
export MUSIK_WRITERS_STOPPED=1
if ! "${compose[@]}" up -d --force-recreate migrate || ! "${compose[@]}" wait migrate; then
  printf '%s\n' 'Preparation failed. Player and worker remain stopped; inspect compose logs migrate.' >&2
  exit 1
fi
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 worker
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 player
printf '%s\n' 'Update complete; worker and player are healthy.'
