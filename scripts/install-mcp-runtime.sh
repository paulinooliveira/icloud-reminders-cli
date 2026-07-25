#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-render}"
RUNTIME_DIR="${REMINDERS_MCP_RUNTIME_DIR:-$HOME/.config/icloud-reminders/mcp}"
BIN_DIR="${REMINDERS_MCP_BIN_DIR:-$HOME/.local/bin}"
LAUNCH_DIR="${REMINDERS_MCP_LAUNCH_DIR:-$HOME/Library/LaunchAgents}"
LOG_DIR="${REMINDERS_MCP_LOG_DIR:-$HOME/Library/Logs/icloud-reminders-mcp}"
STAGE="${REMINDERS_MCP_STAGE_DIR:-$RUNTIME_DIR/staging}"
BINARY="$BIN_DIR/reminders"
PROBE="$BIN_DIR/reminders-mcp-probe"
KEYS="$RUNTIME_DIR/keys.json"
PUBLIC_HOST="${REMINDERS_PUBLIC_HOST:-reminders.belo.re}"
PLIST="$LAUNCH_DIR/com.paulino.icloud-reminders-mcp.plist"

render() {
  mkdir -p "$STAGE/bin" "$STAGE/LaunchAgents"
  go build -trimpath -o "$STAGE/bin/reminders" ./cmd/reminders
  go build -trimpath -o "$STAGE/bin/reminders-mcp-probe" ./cmd/reminders-mcp-probe
  sed -e "s|{{BINARY_PATH}}|$BINARY|g" -e "s|{{KEYS_FILE}}|$KEYS|g" \
      -e "s|{{PUBLIC_HOST}}|$PUBLIC_HOST|g" \
      -e "s|{{HOME}}|$HOME|g" -e "s|{{REPO_ROOT}}|$ROOT|g" -e "s|{{LOG_DIR}}|$LOG_DIR|g" \
      "$ROOT/configs/reminders-mcp.plist.template" > "$STAGE/LaunchAgents/com.paulino.icloud-reminders-mcp.plist"
}

check_keys() {
  [[ -f "$KEYS" && -r "$KEYS" ]] || { echo "missing readable keys file: $KEYS" >&2; return 1; }
  [[ "$(stat -f '%Lp' "$KEYS")" == "600" ]] || { echo "keys file must be mode 600: $KEYS" >&2; return 1; }
}

init_keys() {
  [[ ! -e "$KEYS" ]] || { echo "refusing to overwrite existing keys file: $KEYS" >&2; return 1; }
  command -v openssl >/dev/null
  command -v security >/dev/null
  local writer_token reader_token writer_hash reader_hash tmp token_file
  writer_token="$(openssl rand -hex 32)"
  reader_token="$(openssl rand -hex 32)"
  writer_hash="$(printf %s "$writer_token" | shasum -a 256 | awk '{print $1}')"
  reader_hash="$(printf %s "$reader_token" | shasum -a 256 | awk '{print $1}')"
  mkdir -p "$RUNTIME_DIR"
  tmp="$(mktemp "$RUNTIME_DIR/keys.json.XXXXXX")"
  chmod 0600 "$tmp"
  printf '%s\n' '{' \
    '  "keys": [' \
    "    {\"id\":\"agent-writer\",\"key_hash\":\"$writer_hash\",\"lists\":[\"mcp-canary\"],\"write\":true,\"enabled\":true}," \
    "    {\"id\":\"agent-read-only\",\"key_hash\":\"$reader_hash\",\"lists\":[\"mcp-canary\"],\"write\":false,\"enabled\":true}" \
    '  ]' '}' >"$tmp"
  token_file="$(mktemp "$RUNTIME_DIR/tokens.env.XXXXXX")"
  chmod 0600 "$token_file"
  printf 'REMINDERS_MCP_TOKEN=%q\nREMINDERS_MCP_READ_ONLY_TOKEN=%q\n' "$writer_token" "$reader_token" >"$token_file"
  if security add-generic-password -U -a "$USER" -s icloud-reminders-mcp-writer -w "$writer_token" >/dev/null 2>&1 && \
     security add-generic-password -U -a "$USER" -s icloud-reminders-mcp-reader -w "$reader_token" >/dev/null 2>&1; then
    rm -f "$token_file"
    echo "stored plaintext tokens in macOS Keychain services icloud-reminders-mcp-writer and icloud-reminders-mcp-reader"
  else
    echo "Keychain unavailable non-interactively; tokens saved mode 0600 at $token_file" >&2
  fi
  mv "$tmp" "$KEYS"
  chmod 0600 "$KEYS"
  echo "created $KEYS"
}

case "$MODE" in
  init-keys)
    init_keys ;;
  render)
    render
    find "$STAGE" -type f -print | LC_ALL=C sort | while read -r file; do shasum -a 256 "$file"; done ;;
  install)
    render
    check_keys
    mkdir -p "$BIN_DIR" "$LAUNCH_DIR" "$LOG_DIR"
    install -m 0755 "$STAGE/bin/reminders" "$BINARY"
    install -m 0755 "$STAGE/bin/reminders-mcp-probe" "$PROBE"
    install -m 0644 "$STAGE/LaunchAgents/com.paulino.icloud-reminders-mcp.plist" "$PLIST"
    launchctl bootout "gui/$(id -u)/com.paulino.icloud-reminders-mcp" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$PLIST"
    echo "installed binary=$BINARY plist=$PLIST" ;;
  --check|check)
    render
    check_keys
    cmp -s "$STAGE/bin/reminders" "$BINARY" || { echo "drift: $BINARY" >&2; exit 1; }
    cmp -s "$STAGE/bin/reminders-mcp-probe" "$PROBE" || { echo "drift: $PROBE" >&2; exit 1; }
    cmp -s "$STAGE/LaunchAgents/com.paulino.icloud-reminders-mcp.plist" "$PLIST" || { echo "drift: $PLIST" >&2; exit 1; }
    echo "check clean" ;;
  *) echo "usage: $0 init-keys|render|install|--check" >&2; exit 64 ;;
esac
