# Native Apple Reminders CLI + MCP

This project exposes Apple Reminders through the supported macOS EventKit
framework. It contains no iCloud web login, Apple-ID password handling,
reverse-engineered web client, browser session, or fallback backend.

The same native service powers:

- the `reminders` CLI;
- local MCP over stdio;
- token-authenticated remote MCP through a dedicated Cloudflare Tunnel.

## Requirements

- macOS with Reminders enabled in iCloud;
- Xcode command-line tools and Go;
- Full Access under **System Settings > Privacy & Security > Reminders**.

## Build and install

```bash
./scripts/build.sh
./scripts/install-mcp-runtime.sh install
```

If macOS has not authorized the installed app yet:

```bash
./scripts/install-mcp-runtime.sh authorize
```

## CLI

All data commands use in-process EventKit and emit JSON.

```bash
reminders status
reminders lists
reminders show --list mcp-canary --limit 50
reminders get --list mcp-canary <id-or-prefix>
reminders add --list mcp-canary "Pumpkin 🎃"
reminders complete --list mcp-canary <id-or-prefix>
reminders delete <id> [id...]
```

`show` is bounded: default 50, maximum 200, with `total_count`, `limit`,
`offset`, and `has_more` in every response.

## MCP

Local stdio:

```bash
reminders mcp --transport stdio
```

Remote deployment uses a loopback HTTP origin, bearer-token hashes at rest,
per-key list allowlists, read-only/write policy, and a dedicated tunnel. See
[`docs/remote-mcp-runbook.md`](docs/remote-mcp-runbook.md).

## Security boundary

- Native EventKit is the only reminders backend.
- The remote origin binds to `127.0.0.1`.
- Tokens are stored only as SHA-256 hashes in a mode-0600 keys file.
- `lists`, `show`, `get`, `add`, `complete`, and `status` are the complete MCP
  v1 surface. Destructive delete/edit/list-management tools are not exposed.

## Verification

```bash
go test ./...
./scripts/install-mcp-runtime.sh --check
./scripts/verify-remote-reminders.sh
```
