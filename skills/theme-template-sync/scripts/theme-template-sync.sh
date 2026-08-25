#!/bin/sh
set -eu

SCRIPT_PATH=$0
case "$SCRIPT_PATH" in
  */*) ;;
  *) SCRIPT_PATH=$(command -v "$SCRIPT_PATH") ;;
esac
SCRIPT_PARENT=${SCRIPT_PATH%/*}
if [ "$SCRIPT_PARENT" = "$SCRIPT_PATH" ]; then
  SCRIPT_PARENT=.
fi
SCRIPT_DIR=$(CDPATH= cd -- "$SCRIPT_PARENT" && pwd)

THEME_TEMPLATE_SYNC_CALLER_CWD=$PWD
export THEME_TEMPLATE_SYNC_CALLER_CWD

if [ -n "${THEME_TEMPLATE_SYNC_BINARY:-}" ]; then
  if [ ! -x "$THEME_TEMPLATE_SYNC_BINARY" ]; then
    printf '%s\n' '{"status":"NEEDS_SETUP","reason":"trusted-binary-not-executable"}'
    exit 2
  fi
  exec "$THEME_TEMPLATE_SYNC_BINARY" "$@"
fi

if command -v go >/dev/null 2>&1; then
  TASK_BUILD_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/theme-template-sync.XXXXXX") || {
    printf '%s\n' '{"status":"NEEDS_SETUP","reason":"temporary-build-directory-failed"}'
    exit 2
  }
  TASK_BUILD_BINARY=$TASK_BUILD_ROOT/theme-template-sync
  cleanup_build() {
    rm -f "$TASK_BUILD_BINARY"
    rmdir "$TASK_BUILD_ROOT" 2>/dev/null || :
  }
  trap cleanup_build 0
  if ! (
    cd "$SCRIPT_DIR/template-sync-go"
    GOFLAGS= GOTOOLCHAIN=local GOWORK=off \
      go build -o "$TASK_BUILD_BINARY" ./cmd/theme-template-sync >/dev/null 2>&1
  ); then
    printf '%s\n' '{"status":"NEEDS_SETUP","reason":"bundled-go-build-failed"}'
    printf '%s\n' 'failed to build bundled Go executor' >&2
    exit 2
  fi
  if "$TASK_BUILD_BINARY" "$@"; then
    TASK_EXIT_STATUS=0
  else
    TASK_EXIT_STATUS=$?
  fi
  cleanup_build
  trap - 0
  exit "$TASK_EXIT_STATUS"
fi

printf '%s\n' '{"status":"NEEDS_SETUP","reason":"trusted-binary-and-go-missing"}'
exit 2
