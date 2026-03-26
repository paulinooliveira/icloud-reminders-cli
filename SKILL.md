---
name: icloud-reminders
description: Manage Outlook Tasks via Microsoft Graph Python CLI. Local-first queue workflow with explicit queue-sync. Syncs natively to Apple Reminders via Outlook account on macOS.
consumer:
  openclaw:
    connector: icloud-reminders
    config:
      - ~/.config/icloud-reminders/outlook.json
      - ~/.config/icloud-reminders/state.db
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
2. Run `reminders auth --client-id <CLIENT_ID>`
3. Follow device-code prompt and sign in as `paulino.oliveira@outlook.fr`

## Core Commands

```bash
bash scripts/reminders.sh auth --client-id <CLIENT_ID>
bash scripts/reminders.sh sync
bash scripts/reminders.sh lists
bash scripts/reminders.sh list --list Sebastian
bash scripts/reminders.sh add "Buy milk" --list Sebastian --notes "2%" --priority high --due 2026-03-26T18:30
bash scripts/reminders.sh edit outlook-task:<list_id>:<task_id> --title "Buy oat milk"
bash scripts/reminders.sh delete outlook-task:<list_id>:<task_id>
```

## Queue Commands

```bash
bash scripts/reminders.sh queue-upsert morning-briefing --title "Repair Morning Briefing"
bash scripts/reminders.sh queue-state-json
bash scripts/reminders.sh queue-complete morning-briefing
bash scripts/reminders.sh queue-delete morning-briefing
bash scripts/reminders.sh queue-child-upsert morning-briefing refresh --title "Run refresh"
bash scripts/reminders.sh queue-child-complete morning-briefing refresh
bash scripts/reminders.sh queue-refresh morning-briefing
bash scripts/reminders.sh queue-sync --list Sebastian
bash scripts/reminders.sh queue-audit
```

## State Files

- `~/.config/icloud-reminders/outlook.json`
- `~/.config/icloud-reminders/state.db`
- `~/.config/icloud-reminders/queue-sync.lock`

## Queue Model

Queue writes are local-first. `queue-sync` is the explicit Outlook push step.
