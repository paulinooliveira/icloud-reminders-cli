#!/usr/bin/env bash
# Native Apple Reminders CLI development wrapper.

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
GO_BIN="$SCRIPT_DIR/reminders"
# Build Go binary if missing
if [[ ! -x "$GO_BIN" ]]; then
  echo "Building Go binary..." >&2
  bash "$SCRIPT_DIR/build.sh" >&2
fi

exec "$GO_BIN" "$@"
