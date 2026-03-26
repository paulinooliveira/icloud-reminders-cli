---
name: icloud-reminders
description: Manage Outlook Tasks (Microsoft Graph) via a thin Python CLI. Syncs natively to Apple Reminders via Outlook account on macOS.
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

Create a repo-local `.env` for the Outlook app settings:

```bash
cat > .env <<'EOF'
OUTLOOK_CLIENT_ID=c0b35c2b-0d9d-4d87-ae05-6e02b196a456
OUTLOOK_ACCOUNT_EMAIL=paulino.oliveira@outlook.fr
EOF
```

`scripts/reminders.sh` loads `.env` automatically.

Register a Microsoft Entra app:
1. Go to https://portal.azure.com > App registrations > New registration
2. Supported account types: **Personal Microsoft accounts only**
3. Add a delegated API permission: `Tasks.ReadWrite` (Microsoft Graph)
4. Enable "Allow public client flows" (for device-code auth)
5. Copy the Application (client) ID

Then authenticate:

```bash
bash scripts/reminders.sh auth
```

Follow the device-code prompt to sign in as `paulino.oliveira@outlook.fr`.
The token is cached in `~/.config/icloud-reminders/outlook_token_cache.json` and reused on subsequent calls.

Config is stored at `~/.config/icloud-reminders/outlook.json`.

## Supported Commands

```bash
# Auth and reads
bash scripts/reminders.sh auth
bash scripts/reminders.sh sync
bash scripts/reminders.sh lists
bash scripts/reminders.sh list --list Sebastian

# Task CRUD (IDs are outlook-task:<list_id>:<task_id> refs)
bash scripts/reminders.sh add "Buy milk" --list Sebastian --notes "2%" --priority high --due 2026-03-26T18:30
bash scripts/reminders.sh edit outlook-task:<list_id>:<task_id> --title "Buy oat milk"
bash scripts/reminders.sh move outlook-task:<list_id>:<task_id> --list Tasks
bash scripts/reminders.sh delete outlook-task:<list_id>:<task_id>
```

## Operating Model

- Outlook is the only source of truth
- there is no local queue, SQLite state, or background sync layer
- task refs are opaque CLI IDs and should be reused exactly as returned
- moving a task creates a new task in the target list and deletes the source, so the task ref changes
- Outlook can lag briefly after writes, especially after `move`; re-read after a short delay rather than trying to maintain local shadow state

## Task IDs

Task references are opaque strings of the form:

```
outlook-task:<list_id>:<task_id>
```

Pass these to `edit` and `delete`. They are printed by `add` and `list`.

## Priority Support

Outlook supports three practical priority levels: `high`, `medium`, and `low`.
The CLI still accepts `none` as a compatibility alias, but it maps to `medium`.

## Not Supported

- tags / hashtags
- flagging
- queue commands
- local-first sync or SQLite task state

## State Files

- `.env`: repo-local environment config (`OUTLOOK_CLIENT_ID`, account email)
- `~/.config/icloud-reminders/outlook.json`: Outlook client config
- `~/.config/icloud-reminders/outlook_token_cache.json`: Outlook auth token cache

## Apple Reminders sync

Add `paulino.oliveira@outlook.fr` as an Exchange/Outlook account in macOS
System Settings > Internet Accounts. Apple Reminders will sync Outlook task
lists natively, including the Sebastian list.
