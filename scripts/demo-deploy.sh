#!/bin/sh
set -eu

repo_root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
demo_dir="$repo_root/deploy/demo"

cd "$demo_dir"

if [ ! -f .env ]; then
  echo "deploy/demo/.env is missing; copy .env.example and fill its secrets" >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
. ./.env
set +a

: "${SHINYHUB_IMAGE:?SHINYHUB_IMAGE is required}"
: "${SHINYHUB_DEPLOY_TOKEN:?SHINYHUB_DEPLOY_TOKEN is required}"

docker run --rm --network host \
  --entrypoint /usr/local/bin/shinyhub \
  -e SHINYHUB_HOST=http://127.0.0.1:8080 \
  -e SHINYHUB_TOKEN="$SHINYHUB_DEPLOY_TOKEN" \
  -v "$repo_root:$repo_root:ro" \
  -w "$demo_dir" \
  "$SHINYHUB_IMAGE" \
  fleet apply --prune --yes --file fleet.toml
