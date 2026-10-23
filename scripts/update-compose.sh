#!/usr/bin/env bash
# Update a Compose installation: get the images, stop the writers, migrate
# the database (with a verified backup), start worker and player again.
#
#   scripts/update-compose.sh                     # prebuilt images (docker-compose.images.yml)
#   scripts/update-compose.sh --build             # build from this checkout (docker-compose.yml)
#   scripts/update-compose.sh -f a.yml -f b.yml   # explicit Compose options
#   scripts/update-compose.sh --build -f a.yml    # explicit options, local build
set -euo pipefail
cd "$(dirname "$0")/.."
build=0
if [[ "${1:-}" == "--build" ]]; then
  build=1
  shift
fi
if [[ $# -eq 0 ]]; then
  if [[ $build -eq 1 ]]; then set -- -f docker-compose.yml; else set -- -f docker-compose.images.yml; fi
fi
compose=(docker compose "$@")
"${compose[@]}" config --quiet
# Get everything before interrupting the running application.
if [[ $build -eq 1 ]]; then
  # One checkout builds all three images, so they always match.
  "${compose[@]}" build player worker migrate
else
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
fi
"${compose[@]}" stop -t 60 player worker
export MUSIK_WRITERS_STOPPED=1
if ! "${compose[@]}" up -d --force-recreate migrate || ! "${compose[@]}" wait migrate; then
  printf '%s\n' 'Preparation failed. Player and worker remain stopped; inspect compose logs migrate.' >&2
  exit 1
fi
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 worker
"${compose[@]}" up -d --no-deps --wait --wait-timeout 180 player
printf '%s\n' 'Update complete; worker and player are healthy.'
