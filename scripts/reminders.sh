#!/usr/bin/env bash
# Outlook Tasks CLI (Python entrypoint)

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_DIR="$SCRIPT_DIR/.."
VENV_PYTHON="$REPO_DIR/.venv/bin/python3"
PYTHON_CLI="$REPO_DIR/python/reminders_cli.py"

if [[ ! -f "$PYTHON_CLI" ]]; then
  echo "Missing Python CLI at $PYTHON_CLI" >&2
  exit 1
fi

if [[ -x "$VENV_PYTHON" ]]; then
  PYTHON_BIN="$VENV_PYTHON"
elif [[ -n "${PYTHON_BIN:-}" && -x "$PYTHON_BIN" ]]; then
  : # use caller-supplied PYTHON_BIN
else
  PYTHON_BIN="python3"
fi

exec "$PYTHON_BIN" "$PYTHON_CLI" "$@"
