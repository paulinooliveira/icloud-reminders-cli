---
name: icloud-reminders
description: Manage Outlook Tasks via a thin Microsoft Graph Python CLI. Syncs natively to Apple Reminders via Outlook account on macOS.
consumer:
  openclaw:
    connector: icloud-reminders
    config:
      - ~/.config/icloud-reminders/outlook.json
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

## State Files

- `.env`
- `~/.config/icloud-reminders/outlook.json`
- `~/.config/icloud-reminders/outlook_token_cache.json`
