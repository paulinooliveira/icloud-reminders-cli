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

`delete` is deliberately local CLI-only. It accepts exact EventKit IDs, deletes
permanently from the iCloud-backed store, and is not exposed over MCP. For bulk
cleanup, export and review an ID manifest first, retain counts before/after, and
follow [`docs/native-cleanup-runbook.md`](docs/native-cleanup-runbook.md).

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

## iPhone synchronization

The Mac's EventKit totals are authoritative for this tool. After a large native
deletion, iOS can temporarily show a much larger stale count while processing
deletion tombstones. Force-quit Reminders and restart the iPhone first. If the
count still disagrees, toggle **Settings → Apple Account → iCloud → See All →
Reminders** off, choose **Delete from iPhone**, restart, and enable it again.
This clears only the device cache and downloads the current iCloud state.

## Verification

```bash
go test ./...
./scripts/install-mcp-runtime.sh --check
./scripts/verify-remote-reminders.sh
```
