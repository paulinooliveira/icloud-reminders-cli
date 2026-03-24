---
name: icloud-reminders
description: Manage Apple iCloud Reminders via CloudKit API. Use for listing, adding, completing, deleting reminders, managing lists, and hierarchical subtasks. Works with 2FA-protected accounts via cached sessions.
---

# iCloud Reminders (Go)

Access and manage Apple iCloud Reminders via CloudKit API. Full CRUD with hierarchical subtask support.

**Pure Go — no Python or pyicloud required.** Authentication, 2FA, session management and CloudKit API calls are all implemented natively in Go.

## Installation

### Homebrew (Recommended)

The easiest way to install on macOS and Linux:

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
reminders list --all

# Search by title
reminders search "milk"

# Show all lists
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

# Add reminder
reminders add "Buy milk" -l "Einkauf"
reminders add "Buy milk" -l "4400A74B-9D82-4F9D-8CB8-392C72BF856A"  # list id also works
reminders add "Review calendar gaps" -l "Sebastian" --parent "explorer"
reminders add "Review calendar gaps" -l "Sebastian" --tag explorer

# Add with due date and priority
reminders add "Call mom" --due 2026-02-25 --priority high

# Add with notes
reminders add "Buy milk" -l "Einkauf" --notes "Get the organic 2% stuff"

# Add as subtask
reminders add "Butter" --parent ABC123

# Add multiple at once (batch)
reminders add-batch "Butter" "Käse" "Milch" -l "Einkauf"

# Edit a reminder (update title, due date, notes, or priority)
reminders edit abc123 --title "New title"
reminders edit abc123 --due 2026-03-01 --priority high
reminders edit abc123 --notes "Updated notes"
reminders edit abc123 --priority none

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
    ├── auth.go             # reminders auth
    ├── list.go             # reminders list
    ├── add.go              # reminders add / add-batch
    └── ...
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| "not authenticated" | Run `reminders auth` |
| "invalid Apple ID or password" | Re-run `reminders auth --force` |
| "2FA failed" | Re-run `auth`, enter a fresh code |
| "Missing change tag" | Run `reminders sync` |
| "List not found" | Check name with `reminders lists` |
| Binary not found | Run `bash scripts/build.sh` or check your PATH |

## See Also

- [Homebrew Tap Setup](HOMEBREW.md) — maintainer documentation
