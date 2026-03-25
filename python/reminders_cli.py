#!/usr/bin/env python3
"""iCloud Reminders CLI -- wraps pyicloud for auth, raw /rd/ API for everything else."""

import argparse
import json
import sys
import time
import uuid
from pathlib import Path

from tzlocal import get_localzone_name

CONFIG_DIR = Path.home() / ".config" / "icloud-reminders" / "pyicloud_session"
CREDS_FILE = Path.home() / ".config" / "icloud-reminders" / "credentials.json"

PRIORITY_MAP = {"high": 1, "medium": 5, "low": 9, "none": 0}
PRIORITY_LABEL = {1: "high", 5: "medium", 9: "low", 0: "none"}


def err(msg):
    print(msg, file=sys.stderr)


def die(msg, code=1):
    err(msg)
    sys.exit(code)


def load_creds():
    if CREDS_FILE.exists():
        with open(CREDS_FILE) as f:
            return json.load(f)
    return {}


def save_creds(apple_id, password):
    CREDS_FILE.parent.mkdir(parents=True, exist_ok=True)
    with open(CREDS_FILE, "w") as f:
        json.dump({"apple_id": apple_id, "password": password}, f)
    CREDS_FILE.chmod(0o600)


def get_api(prompt_creds=False):
    """Return a PyiCloudService instance. Does NOT touch api.reminders."""
    from pyicloud import PyiCloudService
    from pyicloud.exceptions import PyiCloudFailedLoginException

    creds = load_creds()
    apple_id = creds.get("apple_id")
    password = creds.get("password")

    if prompt_creds or not apple_id:
        apple_id = input("Apple ID: ").strip()
        password = input("Password: ").strip()
        save_creds(apple_id, password)

    CONFIG_DIR.mkdir(parents=True, exist_ok=True)

    try:
        api = PyiCloudService(apple_id, password, cookie_directory=str(CONFIG_DIR))
    except PyiCloudFailedLoginException as e:
        die(f"Login failed: {e}")

    if api.requires_2fa:
        print("2FA required. Enter the 6-digit code sent to your device:")
        code = input("Code: ").strip()
        if not api.validate_2fa_code(code):
            die("2FA validation failed.")
        if not api.is_trusted_session:
            print("Trusting session...")
            api.trust_session()

    return api


# --- Raw /rd/ API helpers (bypass pyicloud's broken RemindersService) ---

def _rd_url(api):
    return api._webservices.get("reminders", {}).get("url", "")


def _rd_params(api):
    return {
        **api.params,
        "clientVersion": "4.0",
        "lang": "en-us",
        "usertz": get_localzone_name(),
    }


def _rd_startup(api):
    """Fetch all reminders + collections via /rd/startup (raw JSON)."""
    url = _rd_url(api)
    if not url:
        die("Reminders service URL not found. Try re-authenticating.")
    resp = api.session.get(f"{url}/rd/startup", params=_rd_params(api))
    resp.raise_for_status()
    return resp.json()


def _find_reminder(data, guid):
    for r in data.get("Reminders", []):
        if r.get("guid") == guid:
            return r
    return None


def _find_collection(data, name):
    for c in data.get("Collections", []):
        if c.get("title", "").lower() == name.lower():
            return c
    return None


def _format_due(due_arr):
    if not due_arr or len(due_arr) < 4:
        return None
    # due_arr = [YYYYMMDD, year, month, day, hour, minute]
    try:
        y, m, d = due_arr[1], due_arr[2], due_arr[3]
        if len(due_arr) >= 6 and (due_arr[4] or due_arr[5]):
            return f"{y:04d}-{m:02d}-{d:02d}T{due_arr[4]:02d}:{due_arr[5]:02d}"
        return f"{y:04d}-{m:02d}-{d:02d}"
    except (ValueError, TypeError):
        return str(due_arr)


# --- Commands ---

def cmd_auth(args, api):
    print(f"Authenticated as: {load_creds().get('apple_id', '?')}")


def cmd_sync(args, api):
    data = _rd_startup(api)
    colls = data.get("Collections", [])
    rems = data.get("Reminders", [])
    print(f"Lists: {len(colls)}, Reminders: {len(rems)}")


def cmd_lists(args, api):
    data = _rd_startup(api)
    colls = data.get("Collections", [])
    rems = data.get("Reminders", [])
    if args.json:
        print(json.dumps([{"name": c["title"], "guid": c["guid"]} for c in colls], indent=2))
        return
    for c in colls:
        count = sum(1 for r in rems if r.get("pGuid") == c["guid"])
        print(f"  {c['title']} ({count})")


def cmd_list(args, api):
    data = _rd_startup(api)
    rems = data.get("Reminders", [])
    if args.list:
        col = _find_collection(data, args.list)
        if not col:
            die(f"List '{args.list}' not found")
        rems = [r for r in rems if r.get("pGuid") == col["guid"]]

    if args.json:
        print(json.dumps(rems, indent=2, default=str))
        return

    print(f"\nReminders: {len(rems)}\n")
    for r in rems:
        parts = [f"  • {r.get('title', '(untitled)')}"]
        p = r.get("priority", 0)
        if p and p in PRIORITY_LABEL:
            parts.append(f"[{PRIORITY_LABEL[p]}]")
        due = _format_due(r.get("dueDate"))
        if due:
            parts.append(f"[due {due}]")
        parts.append(f"({r.get('guid', '?')})")
        print("  ".join(parts))


def cmd_add(args, api):
    data = _rd_startup(api)
    pguid = "tasks"
    if args.list:
        col = _find_collection(data, args.list)
        if not col:
            die(f"List '{args.list}' not found")
        pguid = col["guid"]

    due_dates = None
    if args.due:
        from datetime import datetime as dt
        try:
            if "T" in args.due:
                d = dt.fromisoformat(args.due)
            else:
                d = dt.strptime(args.due, "%Y-%m-%d")
            due_dates = [
                int(f"{d.year}{d.month:02d}{d.day:02d}"),
                d.year, d.month, d.day, d.hour, d.minute,
            ]
        except ValueError as e:
            die(f"Invalid date: {e}")

    reminder = {
        "title": args.title,
        "description": args.notes or "",
        "pGuid": pguid,
        "etag": None,
        "order": None,
        "priority": PRIORITY_MAP.get(args.priority, 0) if args.priority else 0,
        "recurrence": None,
        "alarms": [],
        "startDate": None,
        "startDateTz": None,
        "startDateIsAllDay": False,
        "completedDate": None,
        "dueDate": due_dates,
        "dueDateIsAllDay": False if due_dates and due_dates[4] else True if due_dates else False,
        "lastModifiedDate": None,
        "createdDate": None,
        "isFamily": None,
        "createdDateExtended": int(time.time() * 1000),
        "guid": str(uuid.uuid4()),
    }

    collections = [{"guid": c["guid"], "ctag": c.get("ctag")}
                   for c in data.get("Collections", [])]

    url = _rd_url(api)
    resp = api.session.post(
        f"{url}/rd/reminders/tasks",
        json={"Reminders": reminder, "ClientState": {"Collections": collections}},
        params=_rd_params(api),
    )
    if not resp.ok:
        die(f"Add failed: {resp.status_code} {resp.text[:200]}")

    print(f"Added: '{args.title}' ({reminder['guid']})")


def cmd_edit(args, api):
    data = _rd_startup(api)
    reminder = _find_reminder(data, args.guid)
    if not reminder:
        die(f"Reminder '{args.guid}' not found")

    if args.title is not None:
        reminder["title"] = args.title
    if args.notes is not None:
        reminder["description"] = args.notes
    if args.priority is not None:
        reminder["priority"] = PRIORITY_MAP.get(args.priority, 0)
    if args.flagged:
        reminder["flagged"] = True
    if args.unflagged:
        reminder["flagged"] = False

    if args.due is not None:
        from datetime import datetime as dt
        try:
            if "T" in args.due:
                d = dt.fromisoformat(args.due)
            else:
                d = dt.strptime(args.due, "%Y-%m-%d")
            reminder["dueDate"] = [
                int(f"{d.year}{d.month:02d}{d.day:02d}"),
                d.year, d.month, d.day, d.hour, d.minute,
            ]
        except ValueError as e:
            die(f"Invalid date: {e}")

    collections = [{"guid": c["guid"], "ctag": c.get("ctag")}
                   for c in data.get("Collections", [])]

    url = _rd_url(api)
    resp = api.session.post(
        f"{url}/rd/reminders/tasks",
        json={"Reminders": reminder, "ClientState": {"Collections": collections}},
        params=_rd_params(api),
    )
    if not resp.ok:
        die(f"Edit failed: {resp.status_code} {resp.text[:200]}")

    print(f"Updated: {args.guid}")


def cmd_delete(args, api):
    data = _rd_startup(api)
    reminder = _find_reminder(data, args.guid)
    if not reminder:
        die(f"Reminder '{args.guid}' not found")

    now = int(time.time() * 1000)
    reminder["completedDate"] = [now // 1000 // 86400, 1970, 1, 1, 0, 0]

    collections = [{"guid": c["guid"], "ctag": c.get("ctag")}
                   for c in data.get("Collections", [])]

    url = _rd_url(api)
    resp = api.session.post(
        f"{url}/rd/reminders/tasks",
        json={"Reminders": reminder, "ClientState": {"Collections": collections}},
        params=_rd_params(api),
    )
    if not resp.ok:
        die(f"Delete failed: {resp.status_code} {resp.text[:200]}")

    print(f"Deleted: {args.guid}")


# --- Main ---

def main():
    parser = argparse.ArgumentParser(prog="reminders_cli", description="iCloud Reminders CLI")
    parser.add_argument("--json", action="store_true", help="JSON output")
    sub = parser.add_subparsers(dest="command")

    sub.add_parser("auth", help="Authenticate with iCloud")
    sub.add_parser("sync", help="Refresh and show summary")
    sub.add_parser("lists", help="Show all reminder lists")

    p_list = sub.add_parser("list", help="List reminders")
    p_list.add_argument("--list", "-l", help="Filter by list name")

    p_add = sub.add_parser("add", help="Add a reminder")
    p_add.add_argument("title")
    p_add.add_argument("--list", "-l", dest="list", help="List name")
    p_add.add_argument("--notes", "-n")
    p_add.add_argument("--priority", "-p", choices=["high", "medium", "low", "none"])
    p_add.add_argument("--due", "-d")

    p_edit = sub.add_parser("edit", help="Edit a reminder")
    p_edit.add_argument("guid")
    p_edit.add_argument("--title", "-t")
    p_edit.add_argument("--notes", "-n")
    p_edit.add_argument("--priority", "-p", choices=["high", "medium", "low", "none"])
    p_edit.add_argument("--due", "-d")
    p_edit.add_argument("--flagged", action="store_true")
    p_edit.add_argument("--unflagged", action="store_true")

    p_del = sub.add_parser("delete", help="Complete/delete a reminder")
    p_del.add_argument("guid")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    prompt = args.command == "auth"
    api = get_api(prompt_creds=prompt)

    cmds = {
        "auth": cmd_auth, "sync": cmd_sync, "lists": cmd_lists,
        "list": cmd_list, "add": cmd_add, "edit": cmd_edit, "delete": cmd_delete,
    }
    try:
        cmds[args.command](args, api)
    except Exception as e:
        die(f"Error: {e}")


if __name__ == "__main__":
    main()
