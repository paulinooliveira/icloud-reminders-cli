#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-render}"
TUNNEL_ID="${REMINDERS_TUNNEL_ID:-}"
HOSTNAME="${REMINDERS_PUBLIC_HOST:-}"
CREDENTIALS_FILE="${REMINDERS_TUNNEL_CREDENTIALS:-}"
RUNTIME_DIR="${REMINDERS_TUNNEL_RUNTIME_DIR:-$HOME/.config/icloud-reminders/cloudflared}"
LAUNCH_DIR="${REMINDERS_MCP_LAUNCH_DIR:-$HOME/Library/LaunchAgents}"
LOG_DIR="${REMINDERS_TUNNEL_LOG_DIR:-$HOME/Library/Logs/icloud-reminders-mcp}"
STAGE="${REMINDERS_TUNNEL_STAGE_DIR:-$RUNTIME_DIR/staging}"
CONFIG="$RUNTIME_DIR/config.yml"
PLIST="$LAUNCH_DIR/com.paulino.icloud-reminders-tunnel.plist"

render() {
  [[ -n "$TUNNEL_ID" && -n "$HOSTNAME" && -n "$CREDENTIALS_FILE" ]] || { echo "REMINDERS_TUNNEL_ID, REMINDERS_PUBLIC_HOST and REMINDERS_TUNNEL_CREDENTIALS are required" >&2; return 64; }
  mkdir -p "$STAGE/LaunchAgents"
  sed -e "s|{{TUNNEL_ID}}|$TUNNEL_ID|g" -e "s|{{HOSTNAME}}|$HOSTNAME|g" -e "s|{{CREDENTIALS_FILE}}|$CREDENTIALS_FILE|g" \
    "$ROOT/configs/cloudflared/reminders-tunnel.yml.template" > "$STAGE/config.yml"
  sed -e "s|{{CONFIG_PATH}}|$CONFIG|g" -e "s|{{LOG_DIR}}|$LOG_DIR|g" \
    "$ROOT/configs/cloudflared/com.paulino.icloud-reminders-tunnel.plist.template" > "$STAGE/LaunchAgents/com.paulino.icloud-reminders-tunnel.plist"
}

case "$MODE" in
  render)
    render
    find "$STAGE" -type f -print | LC_ALL=C sort | while read -r file; do shasum -a 256 "$file"; done ;;
  install)
    render
    [[ -f "$CREDENTIALS_FILE" ]] || { echo "missing tunnel credentials: $CREDENTIALS_FILE" >&2; exit 1; }
    mkdir -p "$RUNTIME_DIR" "$LAUNCH_DIR" "$LOG_DIR"
    install -m 0600 "$STAGE/config.yml" "$CONFIG"
    install -m 0644 "$STAGE/LaunchAgents/com.paulino.icloud-reminders-tunnel.plist" "$PLIST"
    cloudflared tunnel --config "$CONFIG" ingress validate
    launchctl bootout "gui/$(id -u)/com.paulino.icloud-reminders-tunnel" 2>/dev/null || true
    launchctl bootstrap "gui/$(id -u)" "$PLIST"
    echo "installed config=$CONFIG plist=$PLIST" ;;
  --check|check)
    render
    cmp -s "$STAGE/config.yml" "$CONFIG" || { echo "drift: $CONFIG" >&2; exit 1; }
    cmp -s "$STAGE/LaunchAgents/com.paulino.icloud-reminders-tunnel.plist" "$PLIST" || { echo "drift: $PLIST" >&2; exit 1; }
    cloudflared tunnel --config "$CONFIG" ingress validate
    echo "check clean" ;;
  *) echo "usage: $0 render|install|--check" >&2; exit 64 ;;
esac
