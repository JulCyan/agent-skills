#!/bin/sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

if ! command -v node >/dev/null 2>&1; then
  echo "shopify-media-sync: Node.js 20.19 or newer is required by the portable launcher" >&2
  exit 2
fi

exec node "$SCRIPT_DIR/launcher.mjs" "$@"
