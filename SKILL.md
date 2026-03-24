---
name: icloud-reminders
description: Manage Apple iCloud Reminders via CloudKit API. Use for listing, adding, completing, deleting reminders, managing lists, and hierarchical subtasks. Works with 2FA-protected accounts via cached sessions.
version: 0.1.0
metadata:
  openclaw:
    requires:
      bins:
        - reminders
      config:
        - ~/.config/icloud-reminders/credentials
        - ~/.config/icloud-reminders/session.json
    install:
      - kind: brew
        tap: tarekbecker/tap
        formula: icloud-reminders
        bins: [reminders]
    emoji: "✅"
    homepage: https://github.com/tarekbecker/icloud-reminders-cli
---

# iCloud Reminders (Go)

Access and manage Apple iCloud Reminders via CloudKit API. Full CRUD with hierarchical subtask support.

**Pure Go — no Python or pyicloud required.** Authentication, 2FA, session management and CloudKit API calls are all implemented natively in Go.

## Installation

### Homebrew (Recommended)

```bash
brew tap tarekbecker/tap
brew install icloud-reminders
```

Upgrade to the latest version:
```bash
brew upgrade icloud-reminders
```

### Install Script

One-line install for any platform:

```bash
curl -sL https://github.com/tarekbecker/icloud-reminders-cli/releases/latest/download/install.sh | bash
```

### Pre-built Binary

Download manually for your platform from [GitHub Releases](https://github.com/tarekbecker/icloud-reminders-cli/releases).

### Build from Source

Requires Go 1.22+:

```bash
bash scripts/build.sh
sudo cp go/reminders /usr/local/bin/
```

> **Development:** Use `scripts/reminders.sh` from the repo root — it auto-builds the binary if missing and loads credentials from the credentials file automatically.

## Setup

1. **Authenticate** (interactive — required on first run):
   ```bash
   reminders auth
   ```

   Credentials are resolved in this order:
   1. `ICLOUD_USERNAME` / `ICLOUD_PASSWORD` environment variables
   2. `~/.config/icloud-reminders/credentials` file (export KEY=value format)
   3. Interactive prompt (fallback)

2. **Session file** (`~/.config/icloud-reminders/session.json`) is created automatically and reused. Run `reminders auth` again when the session expires.

## Commands

```bash
# First-time setup / force re-auth
reminders auth
reminders auth --force

# List all active reminders (hierarchical)
reminders list

# Filter by list name
reminders list -l "🛒 Einkauf"

# Include completed
reminders list --all          # or: -a

# Show only children of a parent reminder (by name or short ID)
reminders list --parent "Supermarkt"
reminders list --parent ABC123DE

# Search by title
reminders search "milk"

# Search including completed
reminders search "milk" --all   # or: -a

# Show all lists (with active counts and short IDs)
reminders lists

# Create a new list
reminders create-list "Sebastian"

# Ensure a top-level anchor reminder exists
reminders ensure-parent "explorer" -l "Sebastian"

# Ensure a native section exists
reminders ensure-section "DK" -l "Belo"

# Rename or delete a native section
reminders rename-section "DK" -l "Belo" --name "Davidson Kempner"
reminders delete-section "Davidson Kempner" -l "Belo"

# Move reminders into or out of a section
reminders set-section ABC123 -l "Belo" --section "DK"
reminders set-section ABC123 -l "Belo" --clear

# Show live sections, including empty ones the CLI created
reminders sections -l "Sebastian"

# Inspect the raw CloudKit record for a reminder
reminders inspect "Section test B1" -l "Sebastian"

# Add reminder (-l is REQUIRED)
reminders add "Buy milk" -l "Einkauf"
reminders add "Buy milk" -l "4400A74B-9D82-4F9D-8CB8-392C72BF856A"   # list id also works
reminders add "Review calendar gaps" -l "Sebastian" --parent "explorer"
reminders add "Review calendar gaps" -l "Sebastian" --tag explorer

# Add with due date and priority
reminders add "Call mom" -l "Einkauf" --due 2026-02-25 --priority high

# Add with notes
reminders add "Buy milk" -l "Einkauf" --notes "Get the organic 2% stuff"

# Add as subtask (-l is REQUIRED even for subtasks)
reminders add "Butter" -l "🛒 Einkauf" --parent ABC123DE

# Add multiple at once (batch; -l is REQUIRED)
reminders add-batch "Butter" "Käse" "Milch" -l "Einkauf"

# Add multiple as subtasks
reminders add-batch "Butter" "Käse" -l "Einkauf" --parent ABC123DE

# Edit a reminder (update title, due date, notes, priority, parent, or flagged state)
reminders edit abc123 --title "New title"
reminders edit abc123 --due 2026-03-01T16:30 --priority high
reminders edit abc123 --notes "Updated notes"
reminders edit abc123 --priority none
reminders edit abc123 --flagged
reminders edit abc123 --unflagged

# Set or clear native Apple Reminders tags
reminders set-tags abc123 --tag p-manager
reminders set-tags abc123 --clear

# Complete reminder
reminders complete abc123

# Delete reminder
reminders delete abc123

# Export as JSON
reminders json

# Force full resync
reminders sync

# Export session cookies (share without password)
reminders export-session session.tar.gz

# Import session from export
reminders import-session session.tar.gz

# Verbose output (any command)
reminders list -v
```

## Session Management

The binary handles sessions automatically:

- **On each run:** tries `accountLogin` with saved cookies to get a fresh CloudKit URL
- **On failure / first run:** triggers full interactive signin + 2FA
- **Trust token:** saved after 2FA so subsequent logins don't require a code
- **Session file:** `~/.config/icloud-reminders/session.json`

## Output Format

```
✅ Reminders: 101 (101 active)

📋 Shopping (12)
  • Supermarket  (ABC123DE)
    • Butter  (FGH456IJ)
    • Cheese  (KLM789NO)
  • Drugstore  (PQR012ST)
    • Baking paper  (UVW345XY)
```

Full record IDs in parentheses — use for `complete`, `delete`, `--parent`. Prefix matching is supported (pass the first few characters).

## Cache & Sync

- **Cache:** `~/.config/icloud-reminders/ck_cache.json` (same JSON format as Python version — shared/compatible)
- **Delta sync:** Fast incremental updates (default)
- **Full sync:** `reminders sync` — can take ~2 min for large accounts

## Architecture

```
scripts/
├── reminders.sh            # Dev wrapper (auto-builds + loads creds)
├── build.sh                # Build script
├── install.sh              # Install script (used by curl | bash one-liner)
└── reminders               # Compiled Go binary (generated)

go/
├── main.go                 # Entry point
├── auth/auth.go            # Native iCloud auth (signin, 2FA, trust, accountLogin)
├── cloudkit/client.go      # CloudKit HTTP API client
├── sync/sync.go            # Delta sync engine
├── writer/writer.go        # Write ops (add/complete/delete)
├── cache/cache.go          # Local JSON cache
├── models/models.go        # Data types
├── utils/utils.go          # CRDT title encoding, timestamps
└── cmd/                    # Cobra CLI commands
    ├── root.go             # Root command; global --verbose / -v flag
    ├── auth.go             # reminders auth [--force]
    ├── list.go             # reminders list [-l] [--parent] [--all/-a]
    ├── lists.go            # reminders lists
    ├── search.go           # reminders search [--all/-a]
    ├── add.go              # reminders add / add-batch (both require -l)
    ├── complete.go         # reminders complete <id>
    ├── delete.go           # reminders delete <id>
    ├── edit.go             # reminders edit <id> [--title] [--due] [--notes] [--priority]
    ├── json_cmd.go         # reminders json
    ├── sync.go             # reminders sync
    ├── export_session.go   # reminders export-session
    └── import_session.go   # reminders import-session
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| "not authenticated" | Run `reminders auth` |
| "invalid Apple ID or password" | Check credentials file |
| "2FA failed" | Re-run `auth`, enter a fresh code |
| "Missing change tag" | Run `reminders sync` |
| "List not found" | Check name with `reminders lists` |
| Binary not found | Run `bash scripts/build.sh` or check your PATH |
