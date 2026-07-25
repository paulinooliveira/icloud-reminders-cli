#!/usr/bin/env bash
# Fail-closed REM.0-REM.8 verifier. Any unavailable required gate is FAIL.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MODE="${1:-local}"
ORIGIN="${REMINDERS_MCP_ORIGIN:-http://127.0.0.1:9181}"
PUBLIC_HOST="${REMINDERS_PUBLIC_HOST:-}"
PUBLIC_IP="${REMINDERS_PUBLIC_IP:-}"
TOKEN="${REMINDERS_MCP_TOKEN:-}"
READ_ONLY_TOKEN="${REMINDERS_MCP_READ_ONLY_TOKEN:-}"
ALLOW_MUTATIONS="${REMINDERS_VERIFY_ALLOW_MUTATIONS:-0}"
ALLOW_TUNNEL_RESTART="${REMINDERS_VERIFY_ALLOW_TUNNEL_RESTART:-0}"
TUNNEL_RESTART_HOST="${REMINDERS_VERIFY_TUNNEL_RESTART_HOST:-}"
DENIED_LIST="${REMINDERS_MCP_DENIED_LIST:-Belo}"
ALLOWED_LIST="${REMINDERS_MCP_ALLOWED_LIST:-mcp-canary}"
NONCE="REM-$(date -u +%Y%m%dT%H%M%SZ)-$$"
ARTIFACT_DIR="${ARTIFACT_DIR:-$ROOT/exports/rem-evidence/$NONCE}"
BIN="${REMINDERS_BINARY:-$HOME/.local/bin/reminders}"
RUNTIME_BIN="${REMINDERS_MCP_RUNTIME_BINARY:-$HOME/Applications/Reminders MCP.app/Contents/MacOS/reminders}"
PROBE="${REMINDERS_MCP_PROBE:-$ROOT/build/reminders-mcp-probe}"
KEYS_FILE="${REMINDERS_MCP_KEYS_FILE:-$HOME/.config/icloud-reminders/mcp/keys.json}"
mkdir -p "$ARTIFACT_DIR" "$ROOT/build"
LOG="$ARTIFACT_DIR/gates.log"
: >"$LOG"

PASS=0
FAIL=0
RESULTS=()
record() {
  local id="$1" status="$2" detail="$3"
  RESULTS+=("$id|$status|$detail")
  if [[ "$status" == PASS ]]; then PASS=$((PASS+1)); else FAIL=$((FAIL+1)); fi
  printf '[%s] %s - %s\n' "$status" "$id" "$detail" | tee -a "$LOG"
}
call_probe() {
  local token="$1" tool="$2" args="$3" endpoint="$4"
  if [[ -n "$PUBLIC_IP" && "$endpoint" == https://* ]]; then
    REMINDERS_MCP_TOKEN="$token" "$PROBE" --transport http --endpoint "$endpoint/mcp" --resolve-ip "$PUBLIC_IP" --tool "$tool" --args "$args" --timeout 45s
  else
    REMINDERS_MCP_TOKEN="$token" "$PROBE" --transport http --endpoint "$endpoint/mcp" --tool "$tool" --args "$args" --timeout 45s
  fi
}
curl_public() {
  if [[ -n "$PUBLIC_IP" && "$1" == https://* ]]; then
    local host=${1#https://}; host=${host%%/*}
    curl --resolve "$host:443:$PUBLIC_IP" "${@:2}" "$1"
  else
    curl "${@:2}" "$1"
  fi
}

[[ -x "$BIN" ]] || { echo "installed reminders binary not executable: $BIN" >&2; exit 1; }
go build -trimpath -o "$PROBE" ./cmd/reminders-mcp-probe

# REM.0: real in-process EventKit backend, using the exact installed MCP app identity.
if out=$(REMINDERS_EVENTKIT_NO_PROMPT=1 "$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool status --timeout 45s 2>&1) && \
   printf '%s' "$out" | rg -q '"authenticated":true' && printf '%s' "$out" | rg -q 'eventkit/in-process'; then
  printf '%s\n' "$out" >"$ARTIFACT_DIR/rem0-status.json"
  record REM.0 PASS "installed binary reports in-process EventKit Full Access"
else
  record REM.0 FAIL "in-process EventKit not authorized: ${out:-no output}"
fi

# REM.1: real stdio response has bounded pagination metadata.
if out=$("$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool show --args "{\"list\":\"$ALLOWED_LIST\",\"limit\":1}" --timeout 45s 2>&1) && \
   printf '%s' "$out" | rg -q 'total_count' && printf '%s' "$out" | rg -q 'has_more' && printf '%s' "$out" | rg -q '"limit":1'; then
  printf '%s\n' "$out" >"$ARTIFACT_DIR/rem1-stdio.json"
  record REM.1 PASS "stdio show returned limit/total_count/has_more"
else
  printf '%s\n' "${out:-}" >"$ARTIFACT_DIR/rem1-stdio.err"
  record REM.1 FAIL "bounded stdio response contract missing"
fi

# REM.2: stdio MCP lists/show plus an explicitly authorized isolated canary mutation.
title="$NONCE"
if [[ "$ALLOW_MUTATIONS" != 1 ]]; then
  record REM.2 FAIL "set REMINDERS_VERIFY_ALLOW_MUTATIONS=1 to authorize canary add+complete"
elif lists=$("$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool lists --timeout 45s 2>&1) && \
   add=$("$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool add --args "{\"title\":\"$title\",\"list\":\"$ALLOWED_LIST\",\"notes\":\"DoD canary; safe to complete\"}" --timeout 45s 2>&1); then
  id=$(printf '%s' "$add" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d["structuredContent"]["reminder"]["id"])' 2>/dev/null || true)
  if [[ -n "$id" ]] && complete=$("$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool complete --args "{\"id\":\"$id\",\"list\":\"$ALLOWED_LIST\"}" --timeout 45s 2>&1); then
    printf '%s\n%s\n%s\n' "$lists" "$add" "$complete" >"$ARTIFACT_DIR/rem2-roundtrip.log"
    record REM.2 PASS "stdio lists/add/complete round trip id=$id"
  else
    record REM.2 FAIL "stdio complete failed id=${id:-missing}"
  fi
else
  record REM.2 FAIL "stdio lists or add failed"
fi

# REM.3: the real in-process backend must fail loud under a deterministic
# authorization-denied negative control; no subprocess fake is accepted.
if out=$(REMINDERS_EVENTKIT_TEST_AUTH=denied "$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool lists --timeout 10s 2>&1); then
  record REM.3 FAIL "denied in-process EventKit unexpectedly succeeded"
elif printf '%s' "$out" | rg -qi 'EventKit.*denied|Reminders access denied'; then
  printf '%s\n' "$out" >"$ARTIFACT_DIR/rem3-denied.log"
  record REM.3 PASS "in-process EventKit denial failed loudly"
else
  record REM.3 FAIL "backend denial lacked structured diagnostic"
fi

if [[ "$MODE" == local ]]; then
  for id in REM.4 REM.5 REM.6 REM.7 REM.8; do record "$id" FAIL "requires installed HTTP runtime; run mode=runtime or remote"; done
else
  base="$ORIGIN"
  [[ "$MODE" == remote ]] && base="https://$PUBLIC_HOST"
  if [[ -z "$TOKEN" || ( "$MODE" == remote && -z "$PUBLIC_HOST" ) ]]; then
    for id in REM.4 REM.5 REM.6 REM.7 REM.8; do record "$id" FAIL "missing REMINDERS_MCP_TOKEN or public host"; done
  else
    listeners=$(lsof -nP -iTCP:9181 -sTCP:LISTEN 2>/dev/null || true)
    printf '%s\n' "$listeners" >"$ARTIFACT_DIR/rem4-listeners.txt"
    if [[ -n "$listeners" ]] && ! printf '%s' "$listeners" | rg -q 'TCP (\*|0\.0\.0\.0|\[::\]):9181'; then
      record REM.4 PASS "HTTP MCP listener is loopback-only"
    else record REM.4 FAIL "missing or non-loopback listener"; fi

    code=$(curl_public "$base/mcp" -sS -o "$ARTIFACT_DIR/rem5-no-token.json" -w '%{http_code}' -X POST || true)
    wrong=$(curl_public "$base/mcp" -sS -o "$ARTIFACT_DIR/rem5-wrong-token.json" -w '%{http_code}' -X POST -H 'Authorization: Bearer wrong' || true)
    if [[ "$code" == 401 && "$wrong" == 401 ]] && ! rg -q "$NONCE" "$ARTIFACT_DIR/rem5-"*.json; then
      record REM.5 PASS "no/wrong token denied 401 without canary leak"
    else record REM.5 FAIL "auth denial codes no=$code wrong=$wrong"; fi

    route=$(curl_public "$base/health" -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $TOKEN" || true)
    if [[ "$route" == 403 ]]; then record REM.6 PASS "non-MCP route denied; dedicated endpoint probed"
    else record REM.6 FAIL "non-MCP route returned $route"; fi

    denied=$(call_probe "$TOKEN" show "{\"list\":\"$DENIED_LIST\",\"limit\":1}" "$base" 2>&1 || true)
    write_status=SKIP
    if [[ -n "$READ_ONLY_TOKEN" ]]; then
      if call_probe "$READ_ONLY_TOKEN" add "{\"title\":\"$NONCE-DENY\",\"list\":\"$ALLOWED_LIST\"}" "$base" >"$ARTIFACT_DIR/rem7-write-deny.log" 2>&1; then write_status=FAIL; else write_status=PASS; fi
    fi
    revoke_status=FAIL
    if [[ "$ALLOW_MUTATIONS" == 1 && -f "$KEYS_FILE" ]]; then
      backup="$ARTIFACT_DIR/keys.backup"
      cp "$KEYS_FILE" "$backup"; chmod 0600 "$backup"
      restore_keys() { install -m 0600 "$backup" "$KEYS_FILE"; rm -f "$backup"; }
      trap restore_keys EXIT
      python3 - "$KEYS_FILE" <<'PY'
import json, os, sys, tempfile
path=sys.argv[1]
with open(path) as f: doc=json.load(f)
for key in doc["keys"]:
    if key["id"] == "agent-writer": key["enabled"] = False
fd,tmp=tempfile.mkstemp(prefix="keys.", dir=os.path.dirname(path)); os.fchmod(fd,0o600)
with os.fdopen(fd,"w") as f: json.dump(doc,f); f.flush(); os.fsync(f.fileno())
os.replace(tmp,path)
PY
      sleep 1
      if call_probe "$TOKEN" status '{}' "$base" >"$ARTIFACT_DIR/rem7-revoked.log" 2>&1; then revoke_status=FAIL; else revoke_status=PASS; fi
      restore_keys
      trap - EXIT
      sleep 1
      if ! call_probe "$TOKEN" status '{}' "$base" >"$ARTIFACT_DIR/rem7-restored.log" 2>&1; then revoke_status=FAIL; fi
    fi
    if printf '%s' "$denied" | rg -q 'list_not_allowed' && [[ "$write_status" == PASS && "$revoke_status" == PASS ]]; then
      record REM.7 PASS "list allowlist, read-only denial, revoke and restore enforced"
    else record REM.7 FAIL "list/read-only/revocation proof missing"; fi

    local_ok=$(REMINDERS_EVENTKIT_NO_PROMPT=1 "$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool status --timeout 45s 2>/dev/null | rg -q '"authenticated":true' && echo yes || echo no)
    remote_ok=$(call_probe "$TOKEN" status '{}' "$base" 2>/dev/null | rg -q '"authenticated":true' && echo yes || echo no)
    fallback_ok=no
    recovered_ok=no
    agent_restart_ok=no
    if [[ "$ALLOW_TUNNEL_RESTART" == 1 && "$TUNNEL_RESTART_HOST" == "$PUBLIC_HOST" && -n "$PUBLIC_HOST" ]] && launchctl print "gui/$(id -u)/com.paulino.icloud-reminders-tunnel" >/dev/null 2>&1; then
      launchctl kill SIGTERM "gui/$(id -u)/com.paulino.icloud-reminders-tunnel" 2>/dev/null || true
      sleep 1
      if REMINDERS_EVENTKIT_NO_PROMPT=1 "$PROBE" --transport stdio --binary "$RUNTIME_BIN" --tool status --timeout 45s 2>/dev/null | rg -q '"authenticated":true'; then fallback_ok=yes; fi
      launchctl kickstart -k "gui/$(id -u)/com.paulino.icloud-reminders-tunnel" 2>/dev/null || true
      # cloudflared typically needs 7-10 seconds to establish all QUIC
      # connections after a deliberate stop. Use one fixed readiness window;
      # do not hide a failed recovery behind request retries.
      sleep 12
      if call_probe "$TOKEN" status '{}' "$base" 2>/dev/null | rg -q '"authenticated":true'; then recovered_ok=yes; fi
      launchctl kickstart -k "gui/$(id -u)/com.paulino.icloud-reminders-mcp" 2>/dev/null || true
      # The LaunchAgent has ThrottleInterval=10; allow one deterministic
      # scheduling window after replacing its SSH session.
      sleep 12
      if call_probe "$TOKEN" status '{}' "$base" 2>/dev/null | rg -q '"authenticated":true'; then agent_restart_ok=yes; fi
    fi
    agent=$(launchctl print "gui/$(id -u)/com.paulino.icloud-reminders-mcp" 2>/dev/null || true)
    tunnel=$(launchctl print "gui/$(id -u)/com.paulino.icloud-reminders-tunnel" 2>/dev/null || true)
    printf '%s\n%s\n' "$agent" "$tunnel" >"$ARTIFACT_DIR/rem8-launchctl.txt"
    ssh_restricted=no
    if [[ -x "$HOME/.local/bin/reminders-mcp-ssh-wrapper" ]] && \
       ! ssh -T -n -o BatchMode=yes -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes \
         -i "$HOME/.ssh/icloud-reminders-mcp" localhost arbitrary-command \
         >"$ARTIFACT_DIR/rem8-ssh-command-denied.log" 2>&1 && \
       rg -q 'command not allowed' "$ARTIFACT_DIR/rem8-ssh-command-denied.log"; then
      ssh_restricted=yes
    fi
    if [[ "$local_ok" == yes && "$remote_ok" == yes && "$fallback_ok" == yes && "$recovered_ok" == yes && "$agent_restart_ok" == yes && "$ssh_restricted" == yes ]] && printf '%s' "$agent" | rg -q 'state = running|pid =' && printf '%s' "$tunnel" | rg -q 'state = running|pid ='; then
      record REM.8 PASS "tunnel stop preserved local MCP; remote recovered; MCP restart retained authorization; LaunchAgents running"
    else record REM.8 FAIL "backend authorization, exact-host tunnel-restart opt-in, or LaunchAgent proof missing"; fi
  fi
fi

python3 - "$ARTIFACT_DIR/report.json" "$NONCE" "$MODE" "$PASS" "$FAIL" "${RESULTS[@]}" <<'PY'
import json, os, platform, subprocess, sys
path, nonce, mode, passed, failed, *raw = sys.argv[1:]
def cmd(*args):
    try: return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT).strip()
    except Exception as exc: return f"ERROR: {exc}"
results=[]
for item in raw:
    gate,status,detail=item.split("|",2); results.append({"gate":gate,"status":status,"detail":detail})
doc={"nonce":nonce,"mode":mode,"git_sha":cmd("git","rev-parse","HEAD"),"git_status":cmd("git","status","--short"),
     "environment":{"platform":platform.platform(),"go":cmd("go","version"),"backend":"eventkit/in-process"},
     "passed":int(passed),"failed":int(failed),"results":results}
with open(path,"w") as f: json.dump(doc,f,indent=2,sort_keys=True)
PY
find "$ARTIFACT_DIR" -type f -maxdepth 1 ! -name SHA256SUMS -print0 | sort -z | xargs -0 shasum -a 256 >"$ARTIFACT_DIR/SHA256SUMS"
printf 'evidence=%s passed=%d failed=%d\n' "$ARTIFACT_DIR" "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]]
