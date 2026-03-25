#!/usr/bin/env python3
"""iCloud Reminders CLI -- wraps pyicloud for all Reminders operations."""

import argparse
import json
import sys
import time
import uuid
from datetime import datetime
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
    if CREDS_FILE.exists():
        CREDS_FILE.chmod(0o600)
    with open(CREDS_FILE, "w") as f:
        json.dump({"apple_id": apple_id, "password": password}, f)
    CREDS_FILE.chmod(0o600)


def get_api(prompt_creds=False):
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
        api = PyiCloudService(
            apple_id, password, cookie_directory=str(CONFIG_DIR)
        )
    except PyiCloudFailedLoginException as e:
        die(f"Login failed: {e}")

    if api.requires_2fa:
        print("2FA required. Enter the 6-digit code sent to your device:")
        code = input("Code: ").strip()
        result = api.validate_2fa_code(code)
        if not result:
            die("2FA validation failed.")
        if not api.is_trusted_session:
            print("Trusting session...")
            api.trust_session()

    return api


def _reminders_params(api):
    return {
        **api.reminders.params,
        "clientVersion": "4.0",
        "lang": "en-us",
        "usertz": get_localzone_name(),
    }


def _fetch_raw(api):
    """Fetch raw startup data (includes guid/etag on every reminder)."""
    resp = api.reminders.session.get(
        f"{api.reminders.service_root}/rd/startup",
        params=_reminders_params(api),
    )
    resp.raise_for_status()
    return resp.json()


def _format_due(due_arr):
    if not due_arr:
        return None
    try:
        return datetime(
            due_arr[1], due_arr[2], due_arr[3],
            due_arr[4], due_arr[5]
        ).strftime("%Y-%m-%dT%H:%M")
    except Exception:
        return str(due_arr)


def _parse_due(s):
    for fmt in ("%Y-%m-%dT%H:%M", "%Y-%m-%d"):
        try:
            return datetime.strptime(s, fmt)
        except ValueError:
            continue
    die(f"Invalid date: {s!r} (use YYYY-MM-DD or YYYY-MM-DDTHH:MM)")


def _due_array(dt):
    if dt is None:
        return None
    return [
        int(f"{dt.year}{dt.month:02}{dt.day:02}"),
        dt.year, dt.month, dt.day, dt.hour, dt.minute,
    ]


# ---------------------------------------------------------------------------
# Commands
# ---------------------------------------------------------------------------

def cmd_auth(args):
    api = get_api(prompt_creds=True)
    apple_id = api.data.get("dsInfo", {}).get("appleId", "?")
    print(f"Authenticated as: {apple_id}")


def cmd_sync(args):
    api = get_api()
    api.reminders.refresh()
    total = sum(len(v) for v in api.reminders.lists.values())
    print(f"Lists: {len(api.reminders.lists)}, Reminders: {total}")


def cmd_lists(args):
    api = get_api()
    if args.json:
        out = [{"name": k, **v} for k, v in api.reminders.collections.items()]
        print(json.dumps(out, indent=2))
    else:
        for name, col in api.reminders.collections.items():
            count = len(api.reminders.lists.get(name, []))
            print(f"{name}  ({count} items)  [{col['guid']}]")


def cmd_list(args):
    api = get_api()
    data = _fetch_raw(api)

    col_by_guid = {c["guid"]: c["title"] for c in data.get("Collections", [])}

    reminders = [r for r in data.get("Reminders", []) if not r.get("completedDate")]

    if args.list:
        target_guid = None
        for c in data.get("Collections", []):
            if c["title"].lower() == args.list.lower():
                target_guid = c["guid"]
                break
        if target_guid is None:
            die(f"List not found: {args.list!r}")
        reminders = [r for r in reminders if r.get("pGuid") == target_guid]

    if args.json:
        out = []
        for r in reminders:
            out.append({
                "guid": r.get("guid"),
                "title": r.get("title"),
                "list": col_by_guid.get(r.get("pGuid"), r.get("pGuid")),
                "priority": PRIORITY_LABEL.get(r.get("priority", 0), "none"),
                "due": _format_due(r.get("dueDate")),
                "notes": r.get("description"),
                "flagged": r.get("flagged", False),
            })
        print(json.dumps(out, indent=2))
    else:
        for r in reminders:
            pri = PRIORITY_LABEL.get(r.get("priority", 0), "none")
            due = _format_due(r.get("dueDate")) or ""
            lst = col_by_guid.get(r.get("pGuid"), "")
            parts = [r.get("title", "(no title)"), f"({r.get('guid', '?')})" ]
            if pri != "none":
                parts.append(f"[{pri}]")
            if due:
                parts.append(f"[due: {due}]")
            if lst and not args.list:
                parts.append(f"[{lst}]")
            print("  ".join(parts))


def cmd_add(args):
    api = get_api()
    due_dt = _parse_due(args.due) if args.due else None
    pri = PRIORITY_MAP.get(args.priority, 0)

    pguid = "tasks"
    if args.list:
        api.reminders.refresh()
        col = api.reminders.collections.get(args.list)
        if col is None:
            die(f"List not found: {args.list!r}")
        pguid = col["guid"]

    new_guid = str(uuid.uuid4())
    payload = {
        "Reminders": {
            "title": args.title,
            "description": args.notes or "",
            "pGuid": pguid,
            "etag": None,
            "order": None,
            "priority": pri,
            "recurrence": None,
            "alarms": [],
            "startDate": None,
            "startDateTz": None,
            "startDateIsAllDay": False,
            "completedDate": None,
            "dueDate": _due_array(due_dt),
            "dueDateIsAllDay": False,
            "lastModifiedDate": None,
            "createdDate": None,
            "isFamily": None,
            "createdDateExtended": int(time.time() * 1000),
            "guid": new_guid,
        },
        "ClientState": {"Collections": list(api.reminders.collections.values())},
    }

    resp = api.reminders.session.post(
        f"{api.reminders.service_root}/rd/reminders/tasks",
        json=payload,
        params=_reminders_params(api),
    )
    resp.raise_for_status()
    print(f"Created: {args.title}  ({new_guid})")


def cmd_edit(args):
    api = get_api()
    data = _fetch_raw(api)

    reminder = next(
        (r for r in data.get("Reminders", []) if r.get("guid") == args.guid), None
    )
    if reminder is None:
        die(f"Reminder not found: {args.guid}")

    if args.title:
        reminder["title"] = args.title
    if args.notes is not None:
        reminder["description"] = args.notes
    if args.priority:
        reminder["priority"] = PRIORITY_MAP.get(args.priority, reminder.get("priority", 0))
    if args.due:
        reminder["dueDate"] = _due_array(_parse_due(args.due))
    if args.flagged:
        reminder["flagged"] = True
    if args.unflagged:
        reminder["flagged"] = False

    col_state = [{"guid": c["guid"], "ctag": c["ctag"]} for c in data.get("Collections", [])]
    payload = {
        "Reminders": reminder,
        "ClientState": {"Collections": col_state},
    }

    resp = api.reminders.session.put(
        f"{api.reminders.service_root}/rd/reminders/tasks",
        json=payload,
        params=_reminders_params(api),
    )
    resp.raise_for_status()
    print(f"Updated: {reminder['title']}  ({args.guid})")


def cmd_delete(args):
    api = get_api()
    data = _fetch_raw(api)

    reminder = next(
        (r for r in data.get("Reminders", []) if r.get("guid") == args.guid), None
    )
    if reminder is None:
        die(f"Reminder not found: {args.guid}")

    # Mark complete -- iCloud treats completion as deletion in most views
    now = datetime.utcnow()
    reminder["completedDate"] = [
        int(f"{now.year}{now.month:02}{now.day:02}"),
        now.year, now.month, now.day, now.hour, now.minute,
    ]

    col_state = [{"guid": c["guid"], "ctag": c["ctag"]} for c in data.get("Collections", [])]
    payload = {
        "Reminders": reminder,
        "ClientState": {"Collections": col_state},
    }

    resp = api.reminders.session.put(
        f"{api.reminders.service_root}/rd/reminders/tasks",
        json=payload,
        params=_reminders_params(api),
    )
    resp.raise_for_status()
    print(f"Completed/deleted: {reminder.get('title')}  ({args.guid})")


# ---------------------------------------------------------------------------
# Argument parser
# ---------------------------------------------------------------------------

def build_parser():
    p = argparse.ArgumentParser(prog="reminders_cli", description="iCloud Reminders CLI")
    p.add_argument("--json", action="store_true", help="Output JSON")
    sub = p.add_subparsers(dest="command", required=True)

    sub.add_parser("auth", help="Authenticate (re-prompts credentials)")
    sub.add_parser("sync", help="Refresh reminders and print summary")

    sub.add_parser("lists", help="Show all reminder lists")

    p_list = sub.add_parser("list", help="List incomplete reminders")
    p_list.add_argument("--list", metavar="NAME", help="Filter by list name")

    p_add = sub.add_parser("add", help="Create a reminder")
    p_add.add_argument("title", help="Reminder title")
    p_add.add_argument("--list", metavar="NAME")
    p_add.add_argument("--priority", choices=["high", "medium", "low", "none"], default="none")
    p_add.add_argument("--notes", metavar="TEXT")
    p_add.add_argument("--due", metavar="DATE", help="YYYY-MM-DD or YYYY-MM-DDTHH:MM")

    p_edit = sub.add_parser("edit", help="Edit a reminder by GUID")
    p_edit.add_argument("guid")
    p_edit.add_argument("--title", metavar="TEXT")
    p_edit.add_argument("--notes", metavar="TEXT")
    p_edit.add_argument("--priority", choices=["high", "medium", "low", "none"])
    p_edit.add_argument("--due", metavar="DATE")
    p_edit.add_argument("--flagged", action="store_true", default=False)
    p_edit.add_argument("--unflagged", action="store_true", default=False)

    p_del = sub.add_parser("delete", help="Complete/delete a reminder by GUID")
    p_del.add_argument("guid")

    return p


COMMANDS = {
    "auth": cmd_auth,
    "sync": cmd_sync,
    "lists": cmd_lists,
    "list": cmd_list,
    "add": cmd_add,
    "edit": cmd_edit,
    "delete": cmd_delete,
}


def main():
    parser = build_parser()
    args = parser.parse_args()
    try:
        COMMANDS[args.command](args)
    except KeyboardInterrupt:
        print()
        sys.exit(1)
    except Exception as e:
        die(f"Error: {e}")


if __name__ == "__main__":
    main()
