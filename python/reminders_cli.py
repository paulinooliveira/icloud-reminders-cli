#!/usr/bin/env python3
"""
reminders_cli.py -- Outlook Tasks CLI.

This is a thin Python wrapper around Microsoft Graph To Do operations.
Outlook is the source of truth; there is no local queue or SQLite state.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
from typing import Any


def err(msg: str) -> None:
    print(msg, file=sys.stderr)


def die(msg: str, code: int = 1) -> None:
    err(msg)
    sys.exit(code)


def _get_client():
    from outlook_backend import get_client

    return get_client()


def _priority_value(label: str | None) -> int | None:
    if label is None:
        return None
    return {
        "high": 1,
        "medium": 5,
        "low": 9,
        "none": 5,
    }.get(label, 5)


def cmd_auth(args: Any, _client: Any) -> None:
    from outlook_backend import get_client, load_outlook_conf, save_outlook_conf

    conf = load_outlook_conf()
    client_id = (
        getattr(args, "client_id", None)
        or conf.get("client_id")
        or os.environ.get("OUTLOOK_CLIENT_ID", "")
    )
    if not client_id:
        die(
            "Provide --client-id or set OUTLOOK_CLIENT_ID.\n"
            "Register an app at https://portal.azure.com "
            "(personal accounts, delegated Tasks.ReadWrite)."
        )

    conf["client_id"] = client_id
    save_outlook_conf(conf)

    client = get_client()
    me = client.me()
    print(f"Authenticated as: {me.get('userPrincipalName') or me.get('mail', '?')}")


def cmd_sync(args: Any, client: Any) -> None:
    from outlook_backend import list_task_lists, list_tasks

    lists = list_task_lists(client)
    total = sum(len(list_tasks(client, item["id"])) for item in lists)
    print(f"Lists: {len(lists)}, Active tasks: {total}")


def cmd_lists(args: Any, client: Any) -> None:
    from outlook_backend import list_task_lists

    lists = list_task_lists(client)
    if args.json:
        print(
            json.dumps(
                [{"name": item["displayName"], "id": item["id"]} for item in lists],
                indent=2,
            )
        )
        return

    for item in lists:
        print(f"  {item['displayName']}  ({item['id']})")


def cmd_list(args: Any, client: Any) -> None:
    from outlook_backend import (
        _task_to_reminder,
        fmt_task,
        get_or_create_list,
        list_task_lists,
        list_tasks,
    )

    if args.list:
        task_list = get_or_create_list(client, args.list)
        reminders = [
            _task_to_reminder(task, task_list["id"])
            for task in list_tasks(client, task_list["id"])
        ]
    else:
        reminders = []
        for task_list in list_task_lists(client):
            reminders.extend(
                _task_to_reminder(task, task_list["id"])
                for task in list_tasks(client, task_list["id"])
            )

    if args.json:
        print(json.dumps(reminders, indent=2, default=str))
        return

    print(f"\nTasks: {len(reminders)}\n")
    for reminder in reminders:
        print(fmt_task(reminder))


def cmd_add(args: Any, client: Any) -> None:
    from outlook_backend import create_task, encode_task_ref, get_or_create_list, list_task_lists

    if args.list:
        task_list = get_or_create_list(client, args.list)
    else:
        task_lists = list_task_lists(client)
        if not task_lists:
            die("No task lists found. Run 'auth' first.")
        task_list = task_lists[0]

    task = create_task(
        client,
        task_list["id"],
        args.title,
        body=args.notes,
        due=args.due,
        priority=_priority_value(args.priority) or 5,
    )
    ref = encode_task_ref(task_list["id"], task["id"])
    print(f"Added: '{args.title}' ({ref})")


def cmd_edit(args: Any, client: Any) -> None:
    from outlook_backend import decode_task_ref, update_task

    try:
        list_id, task_id = decode_task_ref(args.guid)
    except ValueError as exc:
        die(str(exc))

    update_task(
        client,
        list_id,
        task_id,
        title=args.title,
        body=args.notes,
        due=args.due,
        priority=_priority_value(args.priority),
    )
    print(f"Updated: {args.guid}")


def cmd_move(args: Any, client: Any) -> None:
    from outlook_backend import move_task

    result = move_task(client, args.guid, args.list)
    if result.get("moved"):
        print(f"Moved: {args.guid} -> {result['id']}")
    else:
        print(f"Unchanged: already in list {args.list}")


def cmd_delete(args: Any, client: Any) -> None:
    from outlook_backend import decode_task_ref, delete_task

    try:
        list_id, task_id = decode_task_ref(args.guid)
    except ValueError as exc:
        die(str(exc))

    delete_task(client, list_id, task_id)
    print(f"Deleted: {args.guid}")


def cmd_complete(args: Any, client: Any) -> None:
    from outlook_backend import complete_task, decode_task_ref

    try:
        list_id, task_id = decode_task_ref(args.guid)
    except ValueError as exc:
        die(str(exc))

    complete_task(client, list_id, task_id)
    print(f"Completed: {args.guid}")


def cmd_reopen(args: Any, client: Any) -> None:
    from outlook_backend import decode_task_ref, update_task

    try:
        list_id, task_id = decode_task_ref(args.guid)
    except ValueError as exc:
        die(str(exc))

    update_task(client, list_id, task_id, completed=False)
    print(f"Reopened: {args.guid}")


def main() -> None:
    parser = argparse.ArgumentParser(
        prog="reminders_cli",
        description="Outlook Tasks CLI (Microsoft Graph backend)",
    )
    parser.add_argument("--json", action="store_true", help="JSON output")
    sub = parser.add_subparsers(dest="command")

    p_auth = sub.add_parser("auth", help="Authenticate with Outlook")
    p_auth.add_argument("--client-id", dest="client_id", help="Azure app client ID")

    sub.add_parser("sync", help="Show summary of Outlook lists and tasks")
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

    p_move = sub.add_parser("move", help="Move a task to another list")
    p_move.add_argument("guid")
    p_move.add_argument("--list", "-l", required=True)

    p_delete = sub.add_parser("delete", help="Delete a task")
    p_delete.add_argument("guid")

    p_complete = sub.add_parser("complete", help="Mark a task complete")
    p_complete.add_argument("guid")

    p_reopen = sub.add_parser("reopen", help="Reopen a completed task")
    p_reopen.add_argument("guid")

    args = parser.parse_args()
    if not args.command:
        parser.print_help()
        sys.exit(1)

    if args.command == "auth":
        try:
            cmd_auth(args, None)
        except Exception as exc:
            die(f"Error: {exc}")
        return

    client = _get_client()
    commands = {
        "sync": cmd_sync,
        "lists": cmd_lists,
        "list": cmd_list,
        "add": cmd_add,
        "edit": cmd_edit,
        "move": cmd_move,
        "delete": cmd_delete,
        "complete": cmd_complete,
        "reopen": cmd_reopen,
    }
    try:
        commands[args.command](args, client)
    except KeyboardInterrupt:
        sys.exit(130)
    except Exception as exc:
        die(f"Error: {exc}")


if __name__ == "__main__":
    main()
