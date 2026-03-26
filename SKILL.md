---
name: icloud-reminders
description: Manage Outlook Tasks via a thin Microsoft Graph Python CLI. Syncs natively to Apple Reminders via Outlook account on macOS.
consumer:
  openclaw:
    connector: icloud-reminders
    config:
      - repo: .env
      - ~/.config/icloud-reminders/outlook.json
      - ~/.config/icloud-reminders/outlook_token_cache.json
    homepage: https://github.com/tarekbecker/icloud-reminders-cli
---

# Outlook Tasks CLI

`python/reminders_cli.py` is the canonical CLI. Use:

```bash
bash scripts/reminders.sh --help
scripts/reminders --help
```

## Auth Setup (one-time)

1. Register an app at https://portal.azure.com (personal accounts, `Tasks.ReadWrite`)
2. Save `OUTLOOK_CLIENT_ID` in the repo `.env`
3. Run `reminders auth`
4. Follow device-code prompt and sign in as `paulino.oliveira@outlook.fr`

OpenClaw should assume:

- Outlook is the source of truth
- there is no local queue, SQLite state, or delayed sync layer anymore
- auth is already bootstrapped unless `auth` explicitly fails
- Apple Reminders sync is provided by macOS Internet Accounts, not by this repo

## Core Commands

```bash
bash scripts/reminders.sh auth
bash scripts/reminders.sh sync
bash scripts/reminders.sh lists
bash scripts/reminders.sh list --list Sebastian
bash scripts/reminders.sh add "Buy milk" --list Sebastian --notes "2%" --priority high --due 2026-03-26T18:30
bash scripts/reminders.sh edit outlook-task:<list_id>:<task_id> --title "Buy oat milk"
bash scripts/reminders.sh move outlook-task:<list_id>:<task_id> --list Tasks
bash scripts/reminders.sh delete outlook-task:<list_id>:<task_id>
```

## OpenClaw Operating Rules

- Prefer direct CRUD commands only: `list`, `add`, `edit`, `move`, `delete`
- Default list for task work is `Sebastian` unless the user names another list
- Use `bash scripts/reminders.sh --json list --list <LIST>` when you need stable machine-readable task data
- Treat the returned `outlook-task:<list_id>:<task_id>` ref as opaque; never parse or rewrite it outside this CLI
- After `move`, use the new ref returned by the command. The old ref is invalid because move is implemented as create-in-target + delete-in-source
- Expect brief Outlook consistency lag after writes, especially after `move`; if a fresh `list` does not show the task immediately, retry after a short delay instead of inventing local state
- Supported priorities are `high`, `medium`, and `low`. `none` is only a compatibility alias and maps to `medium`
- Do not use or expect tags, hashtags, or flagging; they are intentionally unsupported
- Do not refer to queue commands, queue-sync, local state DB, or CloudKit/iCloud concepts; those paths are retired

## State Files

- `.env`
- `~/.config/icloud-reminders/outlook.json`
- `~/.config/icloud-reminders/outlook_token_cache.json`
