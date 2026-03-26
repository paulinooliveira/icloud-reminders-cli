#!/usr/bin/env python3
"""
outlook_backend.py -- Microsoft Graph / Outlook Tasks backend.

Replaces iCloud/CloudKit. All remote operations go through the
Microsoft Graph To Do API using delegated DeviceCode auth targeting
personal Microsoft accounts (e.g. paulino.oliveira@outlook.fr).

Config  : ~/.config/icloud-reminders/outlook.json
Token   : azure-identity persistent cache (icloud-reminders-outlook)

ID model
--------
Every remote reference is an opaque string:
  outlook-task:<list_id>:<task_id>
  outlook-checklist:<list_id>:<task_id>:<item_id>
"""

from __future__ import annotations

import json
import os
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

CONFIG_DIR   = Path.home() / ".config" / "icloud-reminders"
OUTLOOK_CONF = CONFIG_DIR / "outlook.json"

GRAPH_SCOPES = ["Tasks.ReadWrite", "User.Read"]
GRAPH_BASE   = "https://graph.microsoft.com/v1.0"

PRIORITY_TO_GRAPH      = {0: "normal", 1: "high", 5: "normal", 9: "low"}
IMPORTANCE_TO_PRIORITY = {"high": 1, "normal": 0, "low": 9}


# ── Opaque ref helpers ──────────────────────────────────────────────────

def encode_task_ref(list_id: str, task_id: str) -> str:
    return f"outlook-task:{list_id}:{task_id}"


def decode_task_ref(ref: str) -> tuple[str, str]:
    if not ref.startswith("outlook-task:"):
        raise ValueError(f"Not an outlook task ref: {ref!r}")
    parts = ref[len("outlook-task:"):].split(":", 1)
    if len(parts) != 2:
        raise ValueError(f"Malformed outlook task ref: {ref!r}")
    return parts[0], parts[1]


def encode_checklist_ref(list_id: str, task_id: str, item_id: str) -> str:
    return f"outlook-checklist:{list_id}:{task_id}:{item_id}"


def decode_checklist_ref(ref: str) -> tuple[str, str, str]:
    if not ref.startswith("outlook-checklist:"):
        raise ValueError(f"Not a checklist ref: {ref!r}")
    parts = ref[len("outlook-checklist:"):].split(":", 2)
    if len(parts) != 3:
        raise ValueError(f"Malformed checklist ref: {ref!r}")
    return parts[0], parts[1], parts[2]


# ── Config helpers ──────────────────────────────────────────────────────

def load_outlook_conf() -> dict:
    if OUTLOOK_CONF.exists():
        with open(OUTLOOK_CONF) as f:
            return json.load(f)
    return {}


def save_outlook_conf(conf: dict) -> None:
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    with open(OUTLOOK_CONF, "w") as f:
        json.dump(conf, f, indent=2)


# ── Auth helpers ────────────────────────────────────────────────────────

def _build_credential(client_id: str) -> Any:
    from azure.identity import DeviceCodeCredential
    CONFIG_DIR.mkdir(parents=True, exist_ok=True)
    kwargs: dict[str, Any] = {
        "client_id": client_id,
        "tenant_id": "consumers",
    }
    try:
        from azure.identity import TokenCachePersistenceOptions  # type: ignore[attr-defined]
        kwargs["cache_persistence_options"] = TokenCachePersistenceOptions(
            name="icloud-reminders-outlook",
            allow_unencrypted_storage=True,
        )
    except (ImportError, AttributeError):
        pass
    return DeviceCodeCredential(**kwargs)


def get_client() -> "GraphClient":
    """Return a ready GraphClient, triggering device-code auth when needed."""
    conf = load_outlook_conf()
    client_id = conf.get("client_id") or os.environ.get("OUTLOOK_CLIENT_ID", "")
    if not client_id:
        print(
            "No Outlook client_id configured.\n"
            "Register an app at https://portal.azure.com "
            "(personal accounts, delegated Tasks.ReadWrite), then run:\n\n"
            "  reminders auth --client-id <YOUR_APP_CLIENT_ID>",
            file=sys.stderr,
        )
        sys.exit(1)
    return GraphClient(_build_credential(client_id))


# ── Thin synchronous Graph HTTP client ─────────────────────────────────

class GraphClient:
    """Minimal synchronous REST wrapper backed by azure-identity tokens."""

    def __init__(self, credential: Any) -> None:
        self._cred = credential

    def _token(self) -> str:
        return self._cred.get_token(*GRAPH_SCOPES).token

    def _headers(self) -> dict:
        return {
            "Authorization": f"Bearer {self._token()}",
            "Content-Type": "application/json",
        }

    def get(self, path: str, params: dict | None = None) -> Any:
        import requests
        r = requests.get(f"{GRAPH_BASE}{path}", headers=self._headers(),
                         params=params, timeout=30)
        r.raise_for_status()
        return r.json()

    def post(self, path: str, body: dict) -> Any:
        import requests
        r = requests.post(f"{GRAPH_BASE}{path}", headers=self._headers(),
                          json=body, timeout=30)
        r.raise_for_status()
        return r.json()

    def patch(self, path: str, body: dict) -> Any:
        import requests
        r = requests.patch(f"{GRAPH_BASE}{path}", headers=self._headers(),
                           json=body, timeout=30)
        r.raise_for_status()
        return r.json()

    def delete(self, path: str) -> None:
        import requests
        r = requests.delete(f"{GRAPH_BASE}{path}", headers=self._headers(),
                            timeout=30)
        r.raise_for_status()

    def me(self) -> dict:
        return self.get("/me")


# ── Task list helpers ───────────────────────────────────────────────────

def list_task_lists(client: GraphClient) -> list[dict]:
    return client.get("/me/todo/lists").get("value", [])


def get_or_create_list(client: GraphClient, name: str) -> dict:
    """Return the named task list, creating it if absent."""
    for lst in list_task_lists(client):
        if lst.get("displayName", "").lower() == name.lower():
            return lst
    return client.post("/me/todo/lists", {"displayName": name})


# ── Task helpers ────────────────────────────────────────────────────────

def _due_body(due: str | None) -> dict:
    if not due:
        return {}
    text = str(due).strip()
    try:
        d = (datetime.fromisoformat(text.replace("Z", "+00:00"))
             if "T" in text
             else datetime.strptime(text, "%Y-%m-%d").replace(tzinfo=timezone.utc))
    except ValueError:
        return {}
    return {"dueDateTime": {
        "dateTime": d.strftime("%Y-%m-%dT%H:%M:%S.0000000"),
        "timeZone": "UTC",
    }}


def _parse_due(graph_due: dict | None) -> str | None:
    if not graph_due:
        return None
    raw = graph_due.get("dateTime", "")
    if not raw:
        return None
    try:
        d = datetime.fromisoformat(raw.rstrip("0").rstrip("."))
        return d.strftime("%Y-%m-%d") if (d.hour == 0 and d.minute == 0) else d.strftime("%Y-%m-%dT%H:%M")
    except ValueError:
        return raw[:16]


def list_tasks(
    client: GraphClient, list_id: str, include_completed: bool = False
) -> list[dict]:
    params: dict[str, str] = {"$top": "999"}
    if not include_completed:
        params["$filter"] = "status ne 'completed'"
    return client.get(f"/me/todo/lists/{list_id}/tasks", params=params).get("value", [])


def create_task(
    client: GraphClient, list_id: str, title: str, *,
    body: str | None = None, due: str | None = None, priority: int = 0,
) -> dict:
    payload: dict = {
        "title": title,
        "importance": PRIORITY_TO_GRAPH.get(priority, "normal"),
    }
    if body:
        payload["body"] = {"content": body, "contentType": "text"}
    payload.update(_due_body(due))
    return client.post(f"/me/todo/lists/{list_id}/tasks", payload)


def update_task(
    client: GraphClient, list_id: str, task_id: str, *,
    title: str | None = None, body: str | None = None,
    due: str | None = None, priority: int | None = None,
    completed: bool | None = None,
) -> dict:
    payload: dict = {}
    if title is not None:
        payload["title"] = title
    if body is not None:
        payload["body"] = {"content": body, "contentType": "text"}
    if priority is not None:
        payload["importance"] = PRIORITY_TO_GRAPH.get(priority, "normal")
    if due is not None:
        payload.update(_due_body(due))
    if completed is True:
        payload["status"] = "completed"
    elif completed is False:
        payload["status"] = "notStarted"
    if not payload:
        return {}
    return client.patch(f"/me/todo/lists/{list_id}/tasks/{task_id}", payload)


def complete_task(client: GraphClient, list_id: str, task_id: str) -> dict:
    return update_task(client, list_id, task_id, completed=True)


def delete_task(client: GraphClient, list_id: str, task_id: str) -> None:
    client.delete(f"/me/todo/lists/{list_id}/tasks/{task_id}")


# ── Checklist helpers ───────────────────────────────────────────────────

def list_checklist_items(client: GraphClient, list_id: str, task_id: str) -> list[dict]:
    return client.get(
        f"/me/todo/lists/{list_id}/tasks/{task_id}/checklistItems"
    ).get("value", [])


def create_checklist_item(
    client: GraphClient, list_id: str, task_id: str, display_name: str
) -> dict:
    return client.post(
        f"/me/todo/lists/{list_id}/tasks/{task_id}/checklistItems",
        {"displayName": display_name},
    )


def update_checklist_item(
    client: GraphClient, list_id: str, task_id: str, item_id: str, *,
    display_name: str | None = None, checked: bool | None = None,
) -> dict:
    payload: dict = {}
    if display_name is not None:
        payload["displayName"] = display_name
    if checked is not None:
        payload["isChecked"] = checked
    if not payload:
        return {}
    return client.patch(
        f"/me/todo/lists/{list_id}/tasks/{task_id}/checklistItems/{item_id}",
        payload,
    )


def delete_checklist_item(
    client: GraphClient, list_id: str, task_id: str, item_id: str
) -> None:
    client.delete(
        f"/me/todo/lists/{list_id}/tasks/{task_id}/checklistItems/{item_id}"
    )


# ── High-level duck-typed API ───────────────────────────────────────────

class OutlookRemindersAPI:
    """
    Satisfies the reminders_api duck type used by QueueManager and the
    generic CLI handlers.

    Parent queue items -> Outlook tasks.
    Child queue items  -> Outlook checklist items under the parent.
    """

    def __init__(self, client: GraphClient, list_name: str = "Sebastian") -> None:
        self._client = client
        self._list_name = list_name
        self._list_id: str | None = None

    def _lid(self) -> str:
        if self._list_id is None:
            self._list_id = get_or_create_list(self._client, self._list_name)["id"]
        return self._list_id

    def create_reminder(
        self, title: str, list_name: str | None = None, **kwargs: Any
    ) -> dict:
        lname = list_name or self._list_name
        lst = get_or_create_list(self._client, lname)
        task = create_task(
            self._client, lst["id"], title,
            body=kwargs.get("notes"),
            due=kwargs.get("due"),
            priority=int(kwargs.get("priority") or 0),
        )
        return {"id": encode_task_ref(lst["id"], task["id"])}

    def edit_reminder(self, ref: str, **kwargs: Any) -> dict:
        list_id, task_id = decode_task_ref(ref)
        update_task(
            self._client, list_id, task_id,
            title=kwargs.get("title"),
            body=kwargs.get("notes"),
            due=kwargs.get("due"),
            priority=kwargs.get("priority"),
        )
        return {}

    def complete_reminder(self, ref: str) -> dict:
        list_id, task_id = decode_task_ref(ref)
        complete_task(self._client, list_id, task_id)
        return {}

    def delete_reminder(self, ref: str) -> dict:
        list_id, task_id = decode_task_ref(ref)
        delete_task(self._client, list_id, task_id)
        return {}

    def get_reminders(self, list_name: str | None = None) -> list[dict]:
        lname = list_name or self._list_name
        lst = get_or_create_list(self._client, lname)
        tasks = list_tasks(self._client, lst["id"])
        return [_task_to_reminder(t, lst["id"]) for t in tasks]

    # Child (checklist) operations

    def create_checklist_item(self, parent_ref: str, display_name: str) -> dict:
        list_id, task_id = decode_task_ref(parent_ref)
        item = create_checklist_item(self._client, list_id, task_id, display_name)
        return {"id": encode_checklist_ref(list_id, task_id, item["id"])}

    def update_checklist_item(
        self, ref: str, *, display_name: str | None = None, checked: bool | None = None,
    ) -> dict:
        list_id, task_id, item_id = decode_checklist_ref(ref)
        update_checklist_item(
            self._client, list_id, task_id, item_id,
            display_name=display_name, checked=checked,
        )
        return {}

    def complete_checklist_item(self, ref: str) -> dict:
        list_id, task_id, item_id = decode_checklist_ref(ref)
        update_checklist_item(self._client, list_id, task_id, item_id, checked=True)
        return {}

    def delete_checklist_item(self, ref: str) -> dict:
        list_id, task_id, item_id = decode_checklist_ref(ref)
        delete_checklist_item(self._client, list_id, task_id, item_id)
        return {}


# ── Formatting ──────────────────────────────────────────────────────────

def _task_to_reminder(task: dict, list_id: str) -> dict:
    return {
        "id":        encode_task_ref(list_id, task["id"]),
        "title":     task.get("title", ""),
        "notes":     (task.get("body") or {}).get("content", ""),
        "completed": task.get("status") == "completed",
        "priority":  IMPORTANCE_TO_PRIORITY.get(task.get("importance", "normal"), 0),
        "due":       _parse_due(task.get("dueDateTime")),
    }


def fmt_task(task: dict) -> str:
    ref = task["id"]
    parts = [f"[{ref}]", task["title"]]
    if task.get("due"):
        parts.append(f"due:{task['due']}")
    if task.get("priority"):
        parts.append(f"priority:{PRIORITY_TO_GRAPH.get(task['priority'], 'normal')}")
    if task.get("notes"):
        parts.append("notes:" + task["notes"].splitlines()[0][:60])
    return "  " + "  ".join(parts)
