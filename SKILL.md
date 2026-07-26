---
name: icloud-reminders
description: Manage Apple Reminders through native macOS EventKit using a bounded CLI or MCP connection.
allowed-tools:
  - Bash(reminders:*)
---

# Native Apple Reminders

Use the `reminders` CLI or its MCP server. The backend is always in-process
EventKit; there is no Apple-ID login, browser session, private web API, or
fallback backend.

## Commands

```bash
reminders status
reminders lists
reminders show --list "mcp-canary" --limit 50
reminders get --list "mcp-canary" <id-or-prefix>
reminders add --list "mcp-canary" "Pumpkin 🎃" --due 2026-07-26
reminders complete --list "mcp-canary" <id-or-prefix>
reminders delete <id> [id...]
reminders mcp --transport stdio
```

Always specify a list for reminder reads and writes. `show` returns bounded
pagination metadata and never silently truncates. Remote MCP clients are also
restricted by their token's list allowlist and write permission.
