---
name: icloud-reminders
description: Manage Outlook Tasks (Microsoft Graph) via a local-first Python CLI. Syncs natively to Apple Reminders via Outlook account on macOS.
---

# Outlook Tasks CLI

`python/reminders_cli.py` is the canonical CLI. The Outlook Tasks (Microsoft Graph) backend
replaces the previous iCloud/CloudKit implementation.

These entrypoints invoke the Python CLI:

```bash
bash scripts/reminders.sh --help
scripts/reminders --help
```

## Setup

Install dependencies:

```bash
python3 -m pip install -r python/requirements.txt
```

Register a Microsoft Entra app:
1. Go to https://portal.azure.com > App registrations > New registration
2. Supported account types: **Personal Microsoft accounts only**
3. Add a delegated API permission: `Tasks.ReadWrite` (Microsoft Graph)
4. Enable "Allow public client flows" (for device-code auth)
5. Copy the Application (client) ID

Then authenticate:

```bash
bash scripts/reminders.sh auth --client-id <YOUR_APP_CLIENT_ID>
```

Follow the device-code prompt to sign in as `paulino.oliveira@outlook.fr`.
The token is cached by azure-identity and reused on subsequent calls.

Config is stored at `~/.config/icloud-reminders/outlook.json`.

## Supported Commands

```bash
# Auth and reads
bash scripts/reminders.sh auth --client-id <CLIENT_ID>
bash scripts/reminders.sh sync
bash scripts/reminders.sh lists
bash scripts/reminders.sh list --list Sebastian

# Task CRUD (IDs are outlook-task:<list_id>:<task_id> refs)
bash scripts/reminders.sh add "Buy milk" --list Sebastian --notes "2%" --priority high --due 2026-03-26T18:30
bash scripts/reminders.sh edit outlook-task:<list_id>:<task_id> --title "Buy oat milk"
bash scripts/reminders.sh delete outlook-task:<list_id>:<task_id>

# Queue workflow (local-first; queue-sync pushes to Outlook)
bash scripts/reminders.sh queue-upsert morning-briefing --title "Repair Morning Briefing" --hours-budget 1.5 --status "stale for next run"
bash scripts/reminders.sh queue-state-json
bash scripts/reminders.sh queue-complete morning-briefing
bash scripts/reminders.sh queue-delete morning-briefing
bash scripts/reminders.sh queue-child-upsert morning-briefing refresh --title "Run refresh"
bash scripts/reminders.sh queue-child-complete morning-briefing refresh
bash scripts/reminders.sh queue-refresh morning-briefing
bash scripts/reminders.sh queue-sync --list Sebastian
bash scripts/reminders.sh queue-audit
```

## Task IDs

Task references are opaque strings of the form:

```
outlook-task:<list_id>:<task_id>
```

Pass these to `edit` and `delete`. They are printed by `add` and `list`.

## State Files

- `~/.config/icloud-reminders/outlook.json`: Outlook client config
- `~/.config/icloud-reminders/state.db`: local queue/state database
- `~/.config/icloud-reminders/queue-sync.lock`: queue sync lock

## Queue Model

Queue writes are local-first. `queue-upsert`, `queue-complete`, `queue-delete`,
and child mutations update local SQLite state immediately. `queue-sync` is the
explicit Outlook push step.

## Apple Reminders sync

Add `paulino.oliveira@outlook.fr` as an Exchange/Outlook account in macOS
System Settings > Internet Accounts. Apple Reminders will sync Outlook task
lists natively, including the Sebastian list.
