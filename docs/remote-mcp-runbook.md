# Secure remote iCloud Reminders MCP runbook

## Topology and invariants

`agent + Bearer token -> dedicated Cloudflare Tunnel -> 127.0.0.1:9181/mcp -> in-process EventKit`

The LaunchAgent enters the already-authorized Reminders TCC context through a
localhost-only SSH session. The installer creates a dedicated key whose
`authorized_keys` entry is restricted with `from="127.0.0.1,::1"`, `restrict`,
and one forced wrapper command. It cannot get a TTY, forward ports, forward an
agent, or run arbitrary commands. SSH is not an agent-facing authentication
layer; remote agents still authenticate only with the scoped Bearer tokens.

- The MCP process binds only to `127.0.0.1` (or another loopback address).
- `/mcp` is the only accepted HTTP route.
- Tokens are never stored in git; only SHA-256 hashes are stored in the key file.
- Every key must have a non-empty `lists` allowlist. `write` should remain false
  unless that agent must call `add` or `complete`.
- This service uses its own tunnel and LaunchAgent labels. Do not add it to the
  ERPNext or Hindsight tunnel.

## 1. Local prerequisites

```bash
reminders mcp --transport stdio        # MCP status tool must report eventkit/in-process + Full Access
go test ./...
go run ./cmd/reminders-mcp-probe \
  --transport stdio --binary ./scripts/reminders --tool lists
```

Build first if `./scripts/reminders` is absent:

```bash
bash scripts/build.sh
```

## 2. Create per-agent keys

Generate a token once and deliver it through the agent's secret store. Store
only its hash on the origin Mac:

```bash
TOKEN="$(openssl rand -hex 32)"
HASH="$(printf %s "$TOKEN" | shasum -a 256 | awk '{print $1}')"
printf 'token=%s\nhash=%s\n' "$TOKEN" "$HASH"
```

For the initial writer/read-only pair scoped to `mcp-canary`, use the installer.
It writes only hashes to the mode-0600 key file and stores plaintext tokens in
macOS Keychain:

```bash
bash scripts/install-mcp-runtime.sh init-keys
security find-generic-password -w -s icloud-reminders-mcp-writer
security find-generic-password -w -s icloud-reminders-mcp-reader
```

For additional agents, copy the schema in `configs/reminders-keys.example.json`,
add the new hash with the minimum lists/write policy, atomically replace
`~/.config/icloud-reminders/mcp/keys.json`, and preserve mode 0600.

The process reloads a valid changed key file on the next request. A missing,
malformed, unreadable, or insecurely permissioned reload revokes every key
until a valid mode-0600 file is restored (fail closed).

## 3. Install the loopback MCP service

```bash
bash scripts/install-mcp-runtime.sh render
bash scripts/install-mcp-runtime.sh install
```

`install` reloads the LaunchAgent so changed arguments cannot remain stale in
launchd.

### macOS permission for the background service

Reminders permission is process-context-specific. An unrelated tool may report
Full access in Terminal while a direct LaunchAgent process reports `Not
determined`. The installer therefore uses the macOS-authorized `sshd` context
through a dedicated localhost-only forced command. After installing, call the
remote `status` tool. If it is not authorized, request the one-time app grant:

```bash
# Run the installed, signed binary once from the interactive session so macOS
# can associate the consent with its stable identifier/path.
bash scripts/install-mcp-runtime.sh authorize
# Click Allow Full Access in the one-time macOS prompt, then reinstall/reload:
bash scripts/install-mcp-runtime.sh install
launchctl kickstart -k "gui/$(id -u)/com.paulino.icloud-reminders-mcp"
```

If a remote shell cannot present the macOS dialog, run the installer’s
`authorize` mode from the logged-in desktop session. It opens the installed app
with the hidden `mcp-authorize` command so the same signed executable that
serves MCP receives the TCC grant.

The forced SSH wrapper sets `REMINDERS_EVENTKIT_NO_PROMPT=1`: a missing grant
fails immediately with `authenticated:false` instead of hanging on an invisible
TCC dialog. The MCP process also exits if its SSH parent disappears, preventing
an orphan from retaining the loopback port across LaunchAgent restarts.

Do not declare REMOTE_GREEN while remote `status` returns
`"authenticated": false`. macOS does not provide a supported non-interactive
way to bypass or pre-grant TCC.

Verify the origin before creating DNS:

```bash
REMINDERS_MCP_TOKEN="$TOKEN" \
  ~/.local/bin/reminders-mcp-probe --transport http --tool lists
lsof -nP -iTCP:9181 -sTCP:LISTEN
```

The listener must be loopback-only.

## 4. Dedicated Cloudflare Tunnel (canary first)

Create a new tunnel in the intended Cloudflare account. Do not reuse an existing
tunnel UUID or credentials file. Then render and install:

```bash
export REMINDERS_TUNNEL_ID='<new-dedicated-uuid>'
export REMINDERS_PUBLIC_HOST='reminders-canary.example.com'
export REMINDERS_TUNNEL_CREDENTIALS="$HOME/.cloudflared/$REMINDERS_TUNNEL_ID.json"
bash scripts/install-tunnel-runtime.sh render
bash scripts/install-tunnel-runtime.sh install
```

Route the canary hostname to this tunnel, then run the remote gates from another
network/device:

```bash
REMINDERS_MCP_TOKEN="$TOKEN" \
REMINDERS_PUBLIC_HOST="$REMINDERS_PUBLIC_HOST" \
REMINDERS_VERIFY_ALLOW_MUTATIONS=1 \
REMINDERS_VERIFY_ALLOW_TUNNEL_RESTART=1 \
REMINDERS_VERIFY_TUNNEL_RESTART_HOST="$REMINDERS_PUBLIC_HOST" \
bash scripts/verify-remote-reminders.sh remote
```

The explicit opt-ins are required because the full gate creates/completes a
nonce canary, atomically revokes/restores the test key, and briefly restarts the
dedicated tunnel. The restart confirmation must exactly equal the public host,
preventing an accidental restart of a differently targeted runtime. Without
them the corresponding gates fail rather than mutate state implicitly. Run the
disruptive gate during a maintenance window.

Move production DNS only after all REM.0-REM.8 gates pass on the canary.

Remote client configuration uses the endpoint
`https://<hostname>/mcp` and the header `Authorization: Bearer <token>`.

## 5. Revocation and least privilege

To revoke a client, set its entry to `"enabled": false` or remove it with an
atomic file replacement, preserving mode 0600. The next request must return 401.
To reduce access, remove lists or set `write` false. Never use an empty allowlist
as a wildcard: empty lists are rejected.

## 6. Rollback

1. Remove/disable the public DNS route.
2. `launchctl bootout gui/$(id -u)/com.paulino.icloud-reminders-tunnel`.
3. `launchctl bootout gui/$(id -u)/com.paulino.icloud-reminders-mcp` if remote
   service removal is required.
4. Delete revoked plaintext tokens from client secret stores and disable their
   hashes in `keys.json`.

The original CloudKit CLI and local stdio MCP do not depend on the tunnel and
remain usable when the tunnel is stopped.

## 7. Evidence

`scripts/verify-remote-reminders.sh` writes a nonce-stamped packet containing
the exact git SHA, environment, gate results, stdout log, and SHA-256 hashes.
Any missing prerequisite or skipped gate is a failure.
