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
APP_ROOT="${REMINDERS_MCP_APP_DIR:-$HOME/Applications/Reminders MCP.app}"
MCP_BINARY="$APP_ROOT/Contents/MacOS/reminders"
PROBE="$BIN_DIR/reminders-mcp-probe"
KEYS="$RUNTIME_DIR/keys.json"
SSH_DIR="${REMINDERS_MCP_SSH_DIR:-$HOME/.ssh}"
SSH_KEY="$SSH_DIR/icloud-reminders-mcp"
SSH_WRAPPER="$BIN_DIR/reminders-mcp-ssh-wrapper"
AUTHORIZED_KEYS="$SSH_DIR/authorized_keys"
PUBLIC_HOST="${REMINDERS_PUBLIC_HOST:-reminders.belo.re}"
PLIST="$LAUNCH_DIR/com.paulino.icloud-reminders-mcp.plist"

render() {
  mkdir -p "$STAGE/bin" "$STAGE/LaunchAgents" "$STAGE/RemindersMCP.app/Contents/MacOS"
  find "$STAGE/RemindersMCP.app/Contents/MacOS" -type f -delete
  go build -trimpath -o "$STAGE/bin/reminders" ./cmd/reminders
  go build -trimpath -o "$STAGE/bin/reminders-mcp-probe" ./cmd/reminders-mcp-probe
  install -m 0755 "$STAGE/bin/reminders" "$STAGE/RemindersMCP.app/Contents/MacOS/reminders"
  install -m 0644 "$ROOT/configs/RemindersMCP-Info.plist" "$STAGE/RemindersMCP.app/Contents/Info.plist"
  find "$STAGE/RemindersMCP.app" -type d -exec chmod 0755 {} +
  if [[ "$(uname -s)" == Darwin ]]; then
    codesign --deep --force --sign - --identifier com.paulino.icloud-reminders.mcp "$STAGE/RemindersMCP.app"
    codesign --verify --deep --strict "$STAGE/RemindersMCP.app"
  fi
  sed -e "s|{{BINARY_PATH}}|$MCP_BINARY|g" -e "s|{{KEYS_FILE}}|$KEYS|g" \
      -e "s|{{SSH_KEY}}|$SSH_KEY|g" \
      -e "s|{{PUBLIC_HOST}}|$PUBLIC_HOST|g" \
      -e "s|{{HOME}}|$HOME|g" -e "s|{{REPO_ROOT}}|$ROOT|g" -e "s|{{LOG_DIR}}|$LOG_DIR|g" \
      "$ROOT/configs/reminders-mcp.plist.template" > "$STAGE/LaunchAgents/com.paulino.icloud-reminders-mcp.plist"
  sed -e "s|{{BINARY_PATH}}|$MCP_BINARY|g" -e "s|{{KEYS_FILE}}|$KEYS|g" \
      -e "s|{{PUBLIC_HOST}}|$PUBLIC_HOST|g" -e "s|{{HOME}}|$HOME|g" \
      "$ROOT/configs/reminders-mcp-ssh-wrapper.sh.template" > "$STAGE/reminders-mcp-ssh-wrapper"
  chmod 0755 "$STAGE/reminders-mcp-ssh-wrapper"
}

setup_ssh_access() {
  mkdir -p "$SSH_DIR"
  chmod 0700 "$SSH_DIR"
  if [[ ! -f "$SSH_KEY" ]]; then
    ssh-keygen -q -t ed25519 -N '' -C icloud-reminders-mcp-localhost -f "$SSH_KEY"
  fi
  chmod 0600 "$SSH_KEY"
  chmod 0644 "$SSH_KEY.pub"
  touch "$AUTHORIZED_KEYS"
  chmod 0600 "$AUTHORIZED_KEYS"
  local public_key marker tmp
  public_key="$(cat "$SSH_KEY.pub")"
  marker="icloud-reminders-mcp-localhost"
  tmp="$(mktemp "$SSH_DIR/authorized_keys.XXXXXX")"
  chmod 0600 "$tmp"
  awk -v marker="$marker" 'index($0, marker) == 0' "$AUTHORIZED_KEYS" > "$tmp"
  printf 'from="127.0.0.1,::1",restrict,command="%s" %s\n' "$SSH_WRAPPER" "$public_key" >> "$tmp"
  mv "$tmp" "$AUTHORIZED_KEYS"
  chmod 0600 "$AUTHORIZED_KEYS"
  ssh -T -n -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes \
    -i "$SSH_KEY" localhost reminders-mcp-authorize >/dev/null
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
    mkdir -p "$BIN_DIR" "$LAUNCH_DIR" "$LOG_DIR" "$(dirname "$APP_ROOT")"
    install -m 0755 "$STAGE/bin/reminders" "$BINARY"
    install -m 0755 "$STAGE/bin/reminders-mcp-probe" "$PROBE"
    mkdir -p "$APP_ROOT/Contents/MacOS"
    find "$APP_ROOT/Contents/MacOS" -type f ! -name reminders -delete
    install -m 0755 "$STAGE/RemindersMCP.app/Contents/MacOS/reminders" "$MCP_BINARY"
    install -m 0644 "$STAGE/RemindersMCP.app/Contents/Info.plist" "$APP_ROOT/Contents/Info.plist"
    mkdir -p "$APP_ROOT/Contents/_CodeSignature"
    install -m 0644 "$STAGE/RemindersMCP.app/Contents/_CodeSignature/CodeResources" "$APP_ROOT/Contents/_CodeSignature/CodeResources"
    find "$APP_ROOT" -type d -exec chmod 0755 {} +
    codesign --verify --deep --strict "$APP_ROOT"
    install -m 0755 "$STAGE/reminders-mcp-ssh-wrapper" "$SSH_WRAPPER"
    setup_ssh_access
    install -m 0644 "$STAGE/LaunchAgents/com.paulino.icloud-reminders-mcp.plist" "$PLIST"
    launchctl bootout "gui/$(id -u)/com.paulino.icloud-reminders-mcp" 2>/dev/null || true
    pkill -TERM -f "^$MCP_BINARY mcp --transport http" 2>/dev/null || true
    sleep 1
    if ! launchctl bootstrap "gui/$(id -u)" "$PLIST"; then
      launchctl kickstart -k "gui/$(id -u)/com.paulino.icloud-reminders-mcp"
    fi
    echo "installed binary=$BINARY plist=$PLIST" ;;
  authorize)
    [[ -x "$MCP_BINARY" ]] || { echo "install the runtime before authorization" >&2; exit 1; }
    label="gui/$(id -u)/com.paulino.icloud-reminders-mcp"
    launchctl bootout "$label" 2>/dev/null || true
    pkill -TERM -f "^$MCP_BINARY mcp --transport http" 2>/dev/null || true
    for _ in 1 2 3 4 5; do
      pgrep -f "^$MCP_BINARY mcp --transport http" >/dev/null 2>&1 || break
      sleep 1
    done
    unset REMINDERS_EVENTKIT_NO_PROMPT
    open -gj -n "$APP_ROOT" --args mcp-authorize
    echo "authorization requested for Reminders MCP; approve Full Access in the macOS prompt"
    echo "after approval, run: launchctl bootstrap gui/$(id -u) $PLIST" ;;
  --check|check)
    render
    check_keys
    cmp -s "$STAGE/bin/reminders" "$BINARY" || { echo "drift: $BINARY" >&2; exit 1; }
    cmp -s "$STAGE/bin/reminders-mcp-probe" "$PROBE" || { echo "drift: $PROBE" >&2; exit 1; }
    cmp -s "$STAGE/RemindersMCP.app/Contents/MacOS/reminders" "$MCP_BINARY" || { echo "drift: $MCP_BINARY" >&2; exit 1; }
    cmp -s "$STAGE/RemindersMCP.app/Contents/Info.plist" "$APP_ROOT/Contents/Info.plist" || { echo "drift: $APP_ROOT/Contents/Info.plist" >&2; exit 1; }
    codesign --verify --strict "$APP_ROOT"
    cmp -s "$STAGE/reminders-mcp-ssh-wrapper" "$SSH_WRAPPER" || { echo "drift: $SSH_WRAPPER" >&2; exit 1; }
    [[ -f "$SSH_KEY" && -f "$SSH_KEY.pub" ]] || { echo "missing dedicated SSH key" >&2; exit 1; }
    rg -q 'icloud-reminders-mcp-localhost' "$AUTHORIZED_KEYS" || { echo "missing restricted localhost authorized key" >&2; exit 1; }
    cmp -s "$STAGE/LaunchAgents/com.paulino.icloud-reminders-mcp.plist" "$PLIST" || { echo "drift: $PLIST" >&2; exit 1; }
    echo "check clean" ;;
  *) echo "usage: $0 init-keys|render|install|authorize|--check" >&2; exit 64 ;;
esac
