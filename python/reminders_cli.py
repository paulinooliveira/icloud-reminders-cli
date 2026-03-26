#!/usr/bin/env python3
"""
reminders_cli.py -- Outlook Tasks CLI (replaces iCloud/CloudKit backend).

All remote operations go through Microsoft Graph To Do via
outlook_backend.OutlookRemindersAPI.  Queue writes are local-first;
queue-sync pushes pending operations to Outlook.
"""

from __future__ import annotations

import argparse
import fcntl
import json
import os
import sys
from pathlib import Path
from typing import Any

# ── Paths ───────────────────────────────────────────────────────────────────

CONFIG_DIR     = Path.home() / ".config" / "icloud-reminders"
QUEUE_SYNC_LOCK = CONFIG_DIR / "queue-sync.lock"
_DB_PATH       = CONFIG_DIR / "state.db"

# ── Helpers ──────────────────────────────────────────────────────────────────

def err(msg: str) -> None:
    print(msg, file=sys.stderr)

def die(msg: str, code: int = 1) -> None:
    err(msg)
    sys.exit(code)


def _get_client():
    from outlook_backend import get_client
    return get_client()


# ── Generic commands ─────────────────────────────────────────────────────────

def cmd_auth(args: Any, _client: Any) -> None:
    """Authenticate with Outlook and persist config/tokens."""
    from outlook_backend import load_outlook_conf, save_outlook_conf, get_client

    conf = load_outlook_conf()
    client_id = getattr(args, "client_id", None) or conf.get("client_id") or \
                os.environ.get("OUTLOOK_CLIENT_ID", "")
    if not client_id:
        die(
            "Provide --client-id or set OUTLOOK_CLIENT_ID.\\n"
            "Register an app at https://portal.azure.com "
            "(personal accounts, delegated Tasks.ReadWrite)."
        )

    conf["client_id"] = client_id
    save_outlook_conf(conf)

    # Trigger token acquisition (device-code flow prints instructions).
    client = get_client()
    me = client.me()
    print(f"Authenticated as: {me.get('userPrincipalName') or me.get('mail', '?')}")


def cmd_sync(args: Any, client: Any) -> None:
    """Show a summary of Outlook task lists and active task counts."""
    from outlook_backend import list_task_lists, list_tasks
    lists = list_task_lists(client)
    total = sum(len(list_tasks(client, l["id"])) for l in lists)
    print(f"Lists: {len(lists)}, Active tasks: {total}")


def cmd_lists(args: Any, client: Any) -> None:
    from outlook_backend import list_task_lists
    lists = list_task_lists(client)
    if args.json:
        print(json.dumps([{"name": l["displayName"], "id": l["id"]} for l in lists], indent=2))
        return
    for l in lists:
        print(f"  {l['displayName']}  ({l['id']})")


def cmd_list(args: Any, client: Any) -> None:
    from outlook_backend import get_or_create_list, list_task_lists, list_tasks, fmt_task, _task_to_reminder
    if args.list:
        lst = get_or_create_list(client, args.list)
        tasks = list_tasks(client, lst["id"])
        reminders = [_task_to_reminder(t, lst["id"]) for t in tasks]
    else:
        reminders = []
        for lst in list_task_lists(client):
            tasks = list_tasks(client, lst["id"])
            reminders += [_task_to_reminder(t, lst["id"]) for t in tasks]

    if args.json:
        print(json.dumps(reminders, indent=2, default=str))
        return
    print(f"\nTasks: {len(reminders)}\n")
    for r in reminders:
        print(fmt_task(r))


def cmd_add(args: Any, client: Any) -> None:
    from outlook_backend import get_or_create_list, list_task_lists, create_task, encode_task_ref
    if args.list:
        lst = get_or_create_list(client, args.list)
    else:
        lists = list_task_lists(client)
        if not lists:
            die("No task lists found. Run 'auth' first.")
        lst = lists[0]

    prio_map = {"high": 1, "medium": 5, "low": 9, "none": 0}
    priority = prio_map.get(args.priority or "", 0)
    task = create_task(
        client, lst["id"], args.title,
        body=args.notes,
        due=args.due,
        priority=priority,
    )
    ref = encode_task_ref(lst["id"], task["id"])
    print(f"Added: '{args.title}' ({ref})")


def cmd_edit(args: Any, client: Any) -> None:
    from outlook_backend import decode_task_ref, update_task
    prio_map = {"high": 1, "medium": 5, "low": 9, "none": 0}
    try:
        list_id, task_id = decode_task_ref(args.guid)
    except ValueError as e:
        die(str(e))
    priority = prio_map.get(args.priority or "", None) if args.priority else None
    update_task(
        client, list_id, task_id,
        title=args.title,
        body=args.notes,
        due=args.due,
        priority=priority,
    )
    print(f"Updated: {args.guid}")


def cmd_delete(args: Any, client: Any) -> None:
    from outlook_backend import decode_task_ref, delete_task
    try:
        list_id, task_id = decode_task_ref(args.guid)
    except ValueError as e:
        die(str(e))
    delete_task(client, list_id, task_id)
    print(f"Deleted: {args.guid}")


# ── Queue layer ───────────────────────────────────────────────────────────────

from state import StateDB as _StateDB
from sebastian_queue import (
    QueueItem, QueueManager, ChecklistItem,
    PRIORITY_MAP as _PMAP, _parse_checklist_line, render_notes,
    _deserialize_item, _serialize_item,
)


class _StateDBAdapter:
    """Bridge QueueManager to StateDB."""

    def __init__(self, db: _StateDB) -> None:
        self._db = db

    def get_queue_item(self, key: str):
        row = self._db.get_queue_item(key)
        if not row:
            return None
        children = {}
        for c in self._db.list_queue_children(key):
            ck = c["child_key"]
            children[ck] = {
                "key": ck, "title": c["title"],
                "cloud_id": c.get("cloud_id", ""),
                "due": c.get("due_at"), "priority": c.get("priority_value", 0),
                "flagged": bool(c.get("flagged", 0)),
                "updated_at": c.get("updated_at", ""),
            }
        row["checklist"]   = json.loads(row.get("checklist_json") or "[]")
        row["tags"]        = json.loads(row.get("tags_json") or "[]")
        row["blocked"]     = bool(row.get("blocked", 0))
        row["flagged"]     = bool(row.get("flagged", False))
        row["priority"]    = row.get("priority_value", 0)
        row["status_line"] = row.get("status_line") or ""
        row["executor"]    = row.get("executor") or ""
        row["due"]         = row.get("due_at")
        row["section"]     = row.get("section_name") or ""
        row["notes"]       = row.get("legacy_notes") or ""
        row["hours_budget"]  = row.get("hours_budget", 0.0)
        row["tokens_budget"] = row.get("tokens_budget", 0)
        row["cloud_id"]    = row.get("cloud_id") or ""
        row["updated_at"]  = row.get("updated_at") or ""
        row["children"]    = children
        row["key"]         = key
        row["title"]       = row.get("title", "")
        return row

    def set_queue_item(self, key: str, data: dict) -> None:
        children = data.pop("children", {})
        self._db.upsert_queue_item(
            key, data["title"],
            cloud_id=data.get("cloud_id") or None,
            section_name=data.get("section") or None,
            tags_json=json.dumps(data.get("tags", [])),
            priority_value=data.get("priority", 0),
            due_at=data.get("due"),
            status_line=data.get("status_line") or None,
            checklist_json=json.dumps(data.get("checklist", [])),
            hours_budget=data.get("hours_budget", 0.0),
            tokens_budget=data.get("tokens_budget", 0),
            executor=data.get("executor") or None,
            blocked=int(bool(data.get("blocked", False))),
            legacy_notes=data.get("notes") or None,
            updated_at=data.get("updated_at"),
        )
        for ck, cv in children.items():
            self._db.upsert_queue_child(
                key, ck, cv["title"],
                cloud_id=cv.get("cloud_id") or None,
                due_at=cv.get("due"),
                priority_value=cv.get("priority", 0),
                flagged=int(bool(cv.get("flagged", False))),
                updated_at=cv.get("updated_at") or None,
            )

    def delete_queue_item(self, key: str) -> None:
        self._db.delete_queue_item(key)

    def list_queue_items(self) -> list:
        return [self.get_queue_item(r["queue_key"])
                for r in self._db.list_queue_items()
                if self.get_queue_item(r["queue_key"])]


class _NoopRemindersAPI:
    """Queue writes that skip remote sync (local-first mutations)."""
    def create_reminder(self, title, list_name=None, **kw):
        return {"id": ""}
    def edit_reminder(self, ref, **kw): pass
    def complete_reminder(self, ref): pass
    def delete_reminder(self, ref): pass
    def get_reminders(self, list_name=None): return []


def _make_mgr_local(args: Any):
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    list_name = getattr(args, "list", None) or "Sebastian"
    return QueueManager(db, _NoopRemindersAPI(), list_name), db


def _make_mgr(client: Any, args: Any):
    from outlook_backend import OutlookRemindersAPI
    list_name = getattr(args, "list", None) or "Sebastian"
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    rem_api = OutlookRemindersAPI(client, list_name)
    return QueueManager(db, rem_api, list_name), db


# ── Queue command handlers ────────────────────────────────────────────────────

def cmd_queue_upsert(args: Any, _client: Any) -> None:
    mgr, _ = _make_mgr_local(args)
    checklist = [_parse_checklist_line(r) for r in (getattr(args, "item", []) or [])]
    prio = _PMAP.get(getattr(args, "priority", "") or "", 0)
    flagged = True if getattr(args, "flagged", False) else (False if getattr(args, "unflagged", False) else None)
    blocked = True if getattr(args, "blocked", False) else (False if getattr(args, "unblocked", False) else None)
    kw: dict[str, Any] = dict(
        section=getattr(args, "section", None),
        tags=getattr(args, "tag", None) or None,
        priority=prio if getattr(args, "priority", None) else None,
        due=getattr(args, "due", None),
        flagged=flagged,
        status_line=getattr(args, "status", None),
        checklist=checklist if checklist else None,
        hours_budget=getattr(args, "hours_budget", None),
        tokens_budget=getattr(args, "tokens_budget", None),
        executor=getattr(args, "executor", None),
        blocked=blocked,
    )
    kw = {k: v for k, v in kw.items() if v is not None}
    title = getattr(args, "title", None)
    if not title:
        existing = mgr._db.get_queue_item(args.key)
        if not existing:
            die("--title required when creating a new queue item")
        title = existing["title"]
    item = mgr.upsert(args.key, title, **kw)
    print(f"upserted: {item.key}  cloud_id={item.cloud_id or '-'}  title={item.title!r}")


def cmd_queue_state_json(args: Any, _client: Any) -> None:
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    print(json.dumps([_serialize_item(_deserialize_item(r)) for r in db.list_queue_items()],
                     indent=2, default=str))


def cmd_queue_complete(args: Any, _client: Any) -> None:
    mgr, _ = _make_mgr_local(args)
    mgr.close(args.key, complete=True)
    print(f"completed: {args.key}")


def cmd_queue_delete(args: Any, _client: Any) -> None:
    mgr, _ = _make_mgr_local(args)
    mgr.close(args.key, complete=False)
    print(f"deleted: {args.key}")


def cmd_queue_child_upsert(args: Any, _client: Any) -> None:
    mgr, _ = _make_mgr_local(args)
    prio = _PMAP.get(getattr(args, "priority", "") or "", 0)
    flagged = True if getattr(args, "flagged", False) else (False if getattr(args, "unflagged", False) else None)
    kw: dict[str, Any] = dict(
        due=getattr(args, "due", None),
        priority=prio if getattr(args, "priority", None) else None,
        flagged=flagged,
    )
    kw = {k: v for k, v in kw.items() if v is not None}
    child = mgr.upsert_child(args.parent_key, args.child_key, args.title, **kw)
    print(f"child upserted: {args.parent_key}/{child.key}  cloud_id={child.cloud_id or '-'}")


def cmd_queue_child_complete(args: Any, _client: Any) -> None:
    mgr, _ = _make_mgr_local(args)
    mgr.close_child(args.parent_key, args.child_key)
    print(f"child completed: {args.parent_key}/{args.child_key}")


def cmd_queue_refresh(args: Any, client: Any) -> None:
    """Re-render notes for a queue item and push to Outlook."""
    from outlook_backend import OutlookRemindersAPI
    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    raw = db.get_queue_item(args.key)
    if not raw:
        die(f"Queue key {args.key!r} not found")
    item = _deserialize_item(raw)
    if not item.cloud_id:
        die(f"No cloud_id for {args.key!r}; run queue-sync first")
    from outlook_backend import decode_task_ref, update_task
    try:
        list_id, task_id = decode_task_ref(item.cloud_id)
    except ValueError as e:
        die(str(e))
    notes = render_notes(item)
    update_task(client, list_id, task_id, body=notes)
    print(f"refreshed: {args.key}  cloud_id={item.cloud_id}")


def _try_acquire_queue_sync_lock():
    QUEUE_SYNC_LOCK.parent.mkdir(parents=True, exist_ok=True)
    fh = open(QUEUE_SYNC_LOCK, "a+")
    try:
        fcntl.flock(fh.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
    except BlockingIOError:
        fh.close()
        return None
    return fh


def cmd_queue_sync(args: Any, client: Any) -> None:
    """Push all local queue items to Outlook Tasks."""
    from outlook_backend import OutlookRemindersAPI, decode_task_ref, update_task, get_or_create_list, list_tasks, _task_to_reminder

    lock = _try_acquire_queue_sync_lock()
    if lock is None:
        return
    try:
        list_name = getattr(args, "list", None) or "Sebastian"
        db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
        rem_api = OutlookRemindersAPI(client, list_name)

        lst = get_or_create_list(client, list_name)
        cloud_tasks = list_tasks(client, lst["id"])
        cloud_by_id: dict[str, dict] = {}
        for t in cloud_tasks:
            ref = f"outlook-task:{lst['id']}:{t['id']}"
            cloud_by_id[ref] = t

        created = updated = failed = 0
        for raw in db.list_queue_items():
            item = _deserialize_item(raw)
            notes = render_notes(item)
            title = item.title
            try:
                if not item.cloud_id:
                    # Title-match to avoid duplicates.
                    existing_ref = next(
                        (ref for ref, t in cloud_by_id.items()
                         if t.get("title") == title),
                        None
                    )
                    if existing_ref:
                        cloud_id = existing_ref
                    else:
                        result = rem_api.create_reminder(
                            title,
                            priority=item.priority,
                            notes=notes,
                            due=item.due,
                        )
                        cloud_id = result.get("id", "")
                        created += 1
                    if cloud_id:
                        db._db.upsert_queue_item(item.key, item.title, cloud_id=cloud_id)
                else:
                    cur = cloud_by_id.get(item.cloud_id)
                    cur_body = (cur.get("body") or {}).get("content", "") if cur else ""
                    cur_title = cur.get("title", "") if cur else ""
                    if cur and cur_title == title and cur_body == notes:
                        continue
                    try:
                        list_id, task_id = decode_task_ref(item.cloud_id)
                    except ValueError:
                        failed += 1
                        continue
                    update_task(client, list_id, task_id,
                                title=title, body=notes,
                                priority=item.priority, due=item.due)
                    updated += 1
            except Exception as e:
                err(f"  sync failed: {item.key}: {e}")
                failed += 1

        parts = []
        if created: parts.append(f"{created} created")
        if updated: parts.append(f"{updated} updated")
        if failed:  parts.append(f"{failed} failed")
        print("queue-sync: " + (", ".join(parts) if parts else "nothing to do"))
    finally:
        try:
            lock.close()
        except Exception:
            pass


def cmd_queue_audit(args: Any, client: Any) -> None:
    """Compare local queue DB against Outlook Tasks."""
    from outlook_backend import get_or_create_list, list_tasks

    db = _StateDBAdapter(_StateDB(str(_DB_PATH)))
    db_items = {r["key"]: _deserialize_item(r) for r in db.list_queue_items()}

    lst = get_or_create_list(client, "Sebastian")
    cloud_tasks = list_tasks(client, lst["id"])
    cloud_by_ref = {f"outlook-task:{lst['id']}:{t['id']}": t.get("title", "") for t in cloud_tasks}

    mismatches = []
    for key, item in db_items.items():
        if item.cloud_id and item.cloud_id not in cloud_by_ref:
            mismatches.append(f"MISSING_IN_OUTLOOK  key={key}  ref={item.cloud_id}")
        elif not item.cloud_id:
            mismatches.append(f"NO_CLOUD_ID         key={key}  title={item.title!r}")
    for ref, ctitle in cloud_by_ref.items():
        if ref not in {i.cloud_id for i in db_items.values()}:
            mismatches.append(f"UNTRACKED_IN_DB     ref={ref}  title={ctitle!r}")

    if not mismatches:
        print("OK -- queue state matches Outlook")
    else:
        for m in mismatches:
            print(m)


# ── Argument parser ───────────────────────────────────────────────────────────

def main() -> None:
    parser = argparse.ArgumentParser(
        prog="reminders_cli",
        description="Outlook Tasks CLI (Microsoft Graph backend)",
    )
    parser.add_argument("--json", action="store_true", help="JSON output")
    sub = parser.add_subparsers(dest="command")

    # Auth
    p_auth = sub.add_parser("auth", help="Authenticate with Outlook")
    p_auth.add_argument("--client-id", dest="client_id", help="Azure app client ID")

    sub.add_parser("sync",  help="Show summary of Outlook lists and tasks")
    sub.add_parser("lists", help="List all Outlook task lists")

    p_list = sub.add_parser("list", help="List tasks")
    p_list.add_argument("--list", "-l", help="Filter by list name")

    p_add = sub.add_parser("add", help="Add a task")
    p_add.add_argument("title")
    p_add.add_argument("--list", "-l")
    p_add.add_argument("--notes", "-n")
    p_add.add_argument("--priority", "-p", choices=["high", "medium", "low", "none"])
    p_add.add_argument("--due", "-d")

    p_edit = sub.add_parser("edit", help="Edit a task")
    p_edit.add_argument("guid")
    p_edit.add_argument("--title", "-t")
    p_edit.add_argument("--notes", "-n")
    p_edit.add_argument("--priority", "-p", choices=["high", "medium", "low", "none"])
    p_edit.add_argument("--due", "-d")

    p_del = sub.add_parser("delete", help="Delete a task")
    p_del.add_argument("guid")

    # Queue
    p_qu = sub.add_parser("queue-upsert")
    p_qu.add_argument("key")
    p_qu.add_argument("--title")
    p_qu.add_argument("--list", default="Sebastian")
    p_qu.add_argument("--section")
    p_qu.add_argument("--tag", dest="tag", action="append", default=[])
    p_qu.add_argument("--priority", choices=["high","medium","low","none"])
    p_qu.add_argument("--executor")
    p_qu.add_argument("--hours-budget", dest="hours_budget", type=float)
    p_qu.add_argument("--tokens-budget", dest="tokens_budget", type=int)
    p_qu.add_argument("--status")
    p_qu.add_argument("--item", action="append", default=[])
    p_qu.add_argument("--blocked",   dest="blocked",   action="store_true", default=False)
    p_qu.add_argument("--unblocked", dest="unblocked", action="store_true", default=False)
    p_qu.add_argument("--due")
    p_qu.add_argument("--flagged",   dest="flagged",   action="store_true", default=False)
    p_qu.add_argument("--unflagged", dest="unflagged", action="store_true", default=False)

    sub.add_parser("queue-state-json")

    p_qc = sub.add_parser("queue-complete")
    p_qc.add_argument("key")

    p_qd = sub.add_parser("queue-delete")
    p_qd.add_argument("key")

    p_qcu = sub.add_parser("queue-child-upsert")
    p_qcu.add_argument("parent_key")
    p_qcu.add_argument("child_key")
    p_qcu.add_argument("--title", required=True)
    p_qcu.add_argument("--due")
    p_qcu.add_argument("--priority", choices=["high","medium","low","none"])
    p_qcu.add_argument("--flagged",   action="store_true", default=False)
    p_qcu.add_argument("--unflagged", action="store_true", default=False)

    p_qcc = sub.add_parser("queue-child-complete")
    p_qcc.add_argument("parent_key")
    p_qcc.add_argument("child_key")

    p_qr = sub.add_parser("queue-refresh")
    p_qr.add_argument("key")

    p_qsync = sub.add_parser("queue-sync")
    p_qsync.add_argument("--list", "-l", default="Sebastian")

    sub.add_parser("queue-audit")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    # Local-only commands: no network, no auth.
    LOCAL_CMDS = {
        "queue-state-json":    cmd_queue_state_json,
        "queue-upsert":        cmd_queue_upsert,
        "queue-complete":      cmd_queue_complete,
        "queue-delete":        cmd_queue_delete,
        "queue-child-upsert":  cmd_queue_child_upsert,
        "queue-child-complete": cmd_queue_child_complete,
    }
    if args.command in LOCAL_CMDS:
        try:
            LOCAL_CMDS[args.command](args, None)
        except Exception as e:
            die(f"Error: {e}")
        return

    # auth only needs a client when actually acquiring tokens.
    if args.command == "auth":
        try:
            cmd_auth(args, None)
        except Exception as e:
            die(f"Error: {e}")
        return

    client = _get_client()
    CMDS = {
        "sync":                cmd_sync,
        "lists":               cmd_lists,
        "list":                cmd_list,
        "add":                 cmd_add,
        "edit":                cmd_edit,
        "delete":              cmd_delete,
        "queue-refresh":       cmd_queue_refresh,
        "queue-sync":          cmd_queue_sync,
        "queue-audit":         cmd_queue_audit,
    }
    try:
        CMDS[args.command](args, client)
    except KeyboardInterrupt:
        sys.exit(130)
    except Exception as e:
        die(f"Error: {e}")


if __name__ == "__main__":
    main()
