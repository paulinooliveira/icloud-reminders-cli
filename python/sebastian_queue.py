"""
queue.py - Sebastian queue management layer.

Replaces Go: cmd/queue_upsert.go, cmd/queue_close.go,
             cmd/queue_child.go, internal/queue/queue.go

Depends on:
  state.py         (Worker B) - StateDB
  reminders_cli.py (Worker A) - duck-typed reminders_api

reminders_api duck-type contract:
  create_reminder(title, list_name, **kwargs) -> {"id": str, ...}
  edit_reminder(guid, **kwargs) -> dict
  complete_reminder(guid) -> dict
  delete_reminder(guid) -> dict
  get_reminders(list_name) -> list[dict]
"""

from __future__ import annotations

import re
from dataclasses import dataclass, field
from datetime import datetime, timezone
from typing import Any

# Stub imports -- wired by integration layer once Workers A/B land
try:
    from state import StateDB  # noqa: F401
except ImportError:
    StateDB = object  # type: ignore[misc,assignment]

try:
    from reminders_cli import RemindersAPI  # noqa: F401
except ImportError:
    RemindersAPI = object  # type: ignore[misc,assignment]


VALID_MARKERS = {"[ ]", "[x]", "[!]", "[~]"}
PRIORITY_MAP = {"high": 1, "medium": 5, "low": 9, "none": 0, "": 0}
PRIORITY_LABEL = {1: "high", 5: "medium", 9: "low", 0: "none"}


@dataclass
class ChecklistItem:
    marker: str  # "[ ]", "[x]", "[!]", "[~]"
    text: str


@dataclass
class ChildItem:
    key: str
    title: str
    cloud_id: str = ""
    due: str | None = None
    priority: int = 0
    flagged: bool = False
    updated_at: str = ""


@dataclass
class QueueItem:
    key: str
    title: str
    section: str = ""
    tags: list[str] = field(default_factory=list)
    priority: int = 0
    due: str | None = None
    notes: str = ""
    flagged: bool = False
    status_line: str = ""
    checklist: list[ChecklistItem] = field(default_factory=list)
    hours_budget: float = 0.0
    tokens_budget: int = 0
    executor: str = ""
    blocked: bool = False
    cloud_id: str = ""
    children: dict[str, ChildItem] = field(default_factory=dict)
    updated_at: str = ""


def render_notes(item: QueueItem) -> str:
    """Deterministically render the note body from queue item state."""
    lines: list[str] = []
    if item.status_line.strip():
        lines.append(item.status_line.strip())
    for c in item.checklist:
        lines.append(f"{_normalize_marker(c.marker)} {c.text.strip()}")
    return "\n".join(lines)


def _normalize_marker(raw: str) -> str:
    """Normalize a checklist marker to one of the four canonical forms."""
    clean = raw.strip()
    if clean in VALID_MARKERS:
        return clean
    # Accept bare inner char (x, !, ~)
    inner = clean.strip("[]").strip()
    if inner in ("x", "!", "~"):
        return f"[{inner}]"
    return "[ ]"


def _blocked_title(title: str, blocked: bool) -> str:
    base = title.strip().removesuffix(" [blocked]").strip()
    return f"{base} [blocked]" if blocked else base


def _now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def _dedupe(lst: list[str]) -> list[str]:
    """Deduplicate preserving insertion order."""
    seen: set[str] = set()
    out: list[str] = []
    for item in lst:
        s = item.strip()
        if s and s not in seen:
            seen.add(s)
            out.append(s)
    return out


class QueueManager:
    """
    Manages Sebastian queue items: syncs to Apple Reminders and persists
    state via StateDB (SQLite).  Both dependencies are duck-typed so they
    can be swapped for fakes in tests.
    """

    def __init__(
        self,
        state_db: Any,
        reminders_api: Any,
        list_name: str = "Sebastian",
    ) -> None:
        self._db = state_db
        self._api = reminders_api
        self._list = list_name

    def upsert(self, key: str, title: str, **kwargs: Any) -> QueueItem:
        """Create or update a queue item, syncing to Apple Reminders."""
        item = self._load_item(key) or QueueItem(key=key, title=title)

        item.title = title
        for attr in (
            "section", "tags", "priority", "due", "flagged",
            "status_line", "checklist", "hours_budget",
            "tokens_budget", "executor", "blocked",
        ):
            if attr in kwargs and kwargs[attr] is not None:
                setattr(item, attr, kwargs[attr])

        item.tags = _dedupe(item.tags)
        item.title = _blocked_title(item.title, item.blocked)
        item.notes = render_notes(item)
        item.updated_at = _now_iso()

        if item.cloud_id:
            self._api.edit_reminder(
                item.cloud_id,
                title=item.title,
                notes=item.notes,
                due=item.due,
                priority=item.priority,
                flagged=item.flagged,
                section=item.section or None,
                tags=item.tags or None,
            )
        else:
            result = self._api.create_reminder(
                item.title,
                self._list,
                notes=item.notes,
                due=item.due,
                priority=item.priority,
                flagged=item.flagged,
                section=item.section or None,
                tags=item.tags or None,
            )
            item.cloud_id = result.get("id", "")

        self._save_item(item)
        return item

    def close(self, key: str, complete: bool = True) -> None:
        """Complete or delete a queue item and remove it from state."""
        item = self._require_item(key)
        # Close children first so they are not orphaned in Reminders
        for child_key in list(item.children):
            self._close_child_reminder(item, child_key, complete=complete)
        if item.cloud_id:
            if complete:
                self._api.complete_reminder(item.cloud_id)
            else:
                self._api.delete_reminder(item.cloud_id)
        self._delete_item(key)

    def upsert_child(
        self, parent_key: str, child_key: str, title: str, **kwargs: Any
    ) -> ChildItem:
        """Create or update a child task under a parent queue item."""
        parent = self._require_item(parent_key)
        child = parent.children.get(child_key) or ChildItem(key=child_key, title=title)

        prior_title = child.title
        child.title = title
        child.updated_at = _now_iso()
        for attr in ("due", "priority", "flagged"):
            if attr in kwargs and kwargs[attr] is not None:
                setattr(child, attr, kwargs[attr])

        if child.cloud_id:
            changes: dict[str, Any] = {}
            if title != prior_title:
                changes["title"] = title
            if "due" in kwargs:
                changes["due"] = child.due
            if "priority" in kwargs:
                changes["priority"] = child.priority
            if "flagged" in kwargs:
                changes["flagged"] = child.flagged
            if changes:
                self._api.edit_reminder(child.cloud_id, **changes)
        else:
            result = self._api.create_reminder(
                title,
                self._list,
                due=child.due,
                priority=child.priority,
                flagged=child.flagged,
                parent_id=parent.cloud_id or None,
            )
            child.cloud_id = result.get("id", "")

        parent.children[child_key] = child
        self._save_item(parent)
        return child

    def close_child(self, parent_key: str, child_key: str) -> None:
        """Complete a child task and remove it from the parent state."""
        parent = self._require_item(parent_key)
        self._close_child_reminder(parent, child_key, complete=True)
        self._save_item(parent)

    def list_items(self) -> list[QueueItem]:
        """Return all queue items with their children."""
        return self._load_all_items()

    def render_notes(self, item: QueueItem) -> str:
        return render_notes(item)

    # ------------------------------------------------------------------
    # Persistence helpers (adapts to StateDB interface when wired)
    # ------------------------------------------------------------------

    def _load_item(self, key: str) -> QueueItem | None:
        if hasattr(self._db, "get_queue_item"):
            raw = self._db.get_queue_item(key)
            return _deserialize_item(raw) if raw else None
        return None

    def _require_item(self, key: str) -> QueueItem:
        item = self._load_item(key)
        if item is None:
            raise KeyError(f"Queue key {key!r} not found; create it with upsert first")
        return item

    def _save_item(self, item: QueueItem) -> None:
        if hasattr(self._db, "set_queue_item"):
            self._db.set_queue_item(item.key, _serialize_item(item))

    def _delete_item(self, key: str) -> None:
        if hasattr(self._db, "delete_queue_item"):
            self._db.delete_queue_item(key)

    def _load_all_items(self) -> list[QueueItem]:
        if hasattr(self._db, "list_queue_items"):
            return [_deserialize_item(r) for r in self._db.list_queue_items()]
        return []

    def _close_child_reminder(
        self, parent: QueueItem, child_key: str, complete: bool
    ) -> None:
        child = parent.children.get(child_key)
        if child and child.cloud_id:
            if complete:
                self._api.complete_reminder(child.cloud_id)
            else:
                self._api.delete_reminder(child.cloud_id)
        parent.children.pop(child_key, None)


def _serialize_item(item: QueueItem) -> dict[str, Any]:
    return {
        "key": item.key,
        "title": item.title,
        "section": item.section,
        "tags": item.tags,
        "priority": item.priority,
        "due": item.due,
        "notes": item.notes,
        "flagged": item.flagged,
        "status_line": item.status_line,
        "checklist": [{"marker": c.marker, "text": c.text} for c in item.checklist],
        "hours_budget": item.hours_budget,
        "tokens_budget": item.tokens_budget,
        "executor": item.executor,
        "blocked": item.blocked,
        "cloud_id": item.cloud_id,
        "updated_at": item.updated_at,
        "children": {
            k: {
                "key": v.key,
                "title": v.title,
                "cloud_id": v.cloud_id,
                "due": v.due,
                "priority": v.priority,
                "flagged": v.flagged,
                "updated_at": v.updated_at,
            }
            for k, v in item.children.items()
        },
    }


def _deserialize_item(raw: dict[str, Any]) -> QueueItem:
    checklist = [
        ChecklistItem(marker=c["marker"], text=c["text"])
        for c in raw.get("checklist", [])
    ]
    children = {
        k: ChildItem(
            key=v["key"],
            title=v["title"],
            cloud_id=v.get("cloud_id", ""),
            due=v.get("due"),
            priority=v.get("priority", 0),
            flagged=v.get("flagged", False),
            updated_at=v.get("updated_at", ""),
        )
        for k, v in raw.get("children", {}).items()
    }
    return QueueItem(
        key=raw["key"],
        title=raw["title"],
        section=raw.get("section", ""),
        tags=raw.get("tags", []),
        priority=raw.get("priority", 0),
        due=raw.get("due"),
        notes=raw.get("notes", ""),
        flagged=raw.get("flagged", False),
        status_line=raw.get("status_line", ""),
        checklist=checklist,
        hours_budget=raw.get("hours_budget", 0.0),
        tokens_budget=raw.get("tokens_budget", 0),
        executor=raw.get("executor", ""),
        blocked=raw.get("blocked", False),
        cloud_id=raw.get("cloud_id", ""),
        updated_at=raw.get("updated_at", ""),
        children=children,
    )


def cmd_queue_upsert(args: Any, state_db: Any, reminders_api: Any) -> None:
    """queue-upsert <key> --title TEXT [options]"""
    mgr = QueueManager(state_db, reminders_api, getattr(args, "list", "Sebastian"))
    checklist: list[ChecklistItem] = [
        _parse_checklist_line(raw) for raw in (getattr(args, "item", []) or [])
    ]
    item = mgr.upsert(
        args.key,
        args.title,
        section=getattr(args, "section", None),
        tags=getattr(args, "tags", None),
        priority=PRIORITY_MAP.get(getattr(args, "priority", "") or "", 0),
        due=getattr(args, "due", None),
        flagged=getattr(args, "flagged", None),
        status_line=getattr(args, "status", None),
        checklist=checklist or None,
        hours_budget=getattr(args, "hours_budget", None),
        tokens_budget=getattr(args, "tokens_budget", None),
        executor=getattr(args, "executor", None),
        blocked=getattr(args, "blocked", None),
    )
    print(f"Queue upserted: {item.title} [{item.key}] cloud_id={item.cloud_id}")


def cmd_queue_close(args: Any, state_db: Any, reminders_api: Any) -> None:
    """queue-close <key> [--delete]"""
    mgr = QueueManager(state_db, reminders_api)
    complete = not getattr(args, "delete", False)
    mgr.close(args.key, complete=complete)
    verb = "completed" if complete else "deleted"
    print(f"Queue {verb}: {args.key}")


def cmd_queue_child_upsert(args: Any, state_db: Any, reminders_api: Any) -> None:
    """queue-child-upsert <parent-key> <child-key> --title TEXT [options]"""
    mgr = QueueManager(state_db, reminders_api)
    child = mgr.upsert_child(
        args.parent_key,
        args.child_key,
        args.title,
        due=getattr(args, "due", None),
        priority=PRIORITY_MAP.get(getattr(args, "priority", "") or "", 0) or None,
        flagged=getattr(args, "flagged", None),
    )
    print(f"Queue child upserted: {child.title} [{args.parent_key}/{args.child_key}]")


def cmd_queue_child_close(args: Any, state_db: Any, reminders_api: Any) -> None:
    """queue-child-close <parent-key> <child-key>"""
    mgr = QueueManager(state_db, reminders_api)
    mgr.close_child(args.parent_key, args.child_key)
    print(f"Queue child completed: {args.parent_key}/{args.child_key}")


def cmd_queue_list(args: Any, state_db: Any, reminders_api: Any) -> None:
    """queue-list"""
    mgr = QueueManager(state_db, reminders_api)
    items = mgr.list_items()
    if not items:
        print("(empty queue)")
        return
    for item in items:
        blocked_tag = " [blocked]" if item.blocked else ""
        print(f"  {item.key}: {item.title}{blocked_tag} (cloud={item.cloud_id or '-'})")
        for ck, child in item.children.items():
            print(f"    {ck}: {child.title}")


def _parse_checklist_line(line: str) -> ChecklistItem:
    """Parse a checklist line: '[ ] text', '[x] text', '[!] text', '[~] text'."""
    m = re.match(r'^\[([x!~ ])\]\s+(.+)$', line.strip())
    if not m:
        raise ValueError(
            f"Invalid checklist item {line!r}; expected e.g. '[ ] text' or '[x] text'"
        )
    return ChecklistItem(marker=f"[{m.group(1)}]", text=m.group(2).strip())


if __name__ == "__main__":

    class _FakeDB:
        def __init__(self) -> None: self._store: dict = {}
        def get_queue_item(self, k): return self._store.get(k)
        def set_queue_item(self, k, v): self._store[k] = v
        def delete_queue_item(self, k): self._store.pop(k, None)
        def list_queue_items(self): return list(self._store.values())

    class _FakeAPI:
        def __init__(self) -> None: self._r: dict = {}; self._n = 0
        def _id(self): self._n += 1; return f"FAKE-{self._n:04d}"
        def create_reminder(self, title, list_name, **kw):
            rid = self._id(); self._r[rid] = {"id": rid, "title": title}
            print(f"  [API] create({title!r}) -> {rid}"); return {"id": rid}
        def edit_reminder(self, g, **kw): self._r.setdefault(g, {}).update(kw); print(f"  [API] edit({g})"); return {}
        def complete_reminder(self, g): print(f"  [API] complete({g})"); return {}
        def delete_reminder(self, g): self._r.pop(g, None); print(f"  [API] delete({g})"); return {}
        def get_reminders(self, _): return list(self._r.values())

    db, api = _FakeDB(), _FakeAPI()
    mgr = QueueManager(db, api, list_name="Sebastian")

    # upsert creates reminder and renders notes
    item = mgr.upsert("seb/test", "My Task", priority=5,
        status_line="0.5 / 2h", checklist=[ChecklistItem("[ ]", "step"), ChecklistItem("[x]", "done")],
        hours_budget=2.0, tokens_budget=1_000_000, blocked=False)
    assert item.cloud_id, "cloud_id missing"
    assert item.notes == "0.5 / 2h\n[ ] step\n[x] done", repr(item.notes)

    # second upsert hits edit path, cloud_id stable
    item2 = mgr.upsert("seb/test", "My Task", status_line="1 / 2h")
    assert item2.cloud_id == item.cloud_id, "cloud_id drifted"

    # blocked suffix applied to title
    b = mgr.upsert("seb/test", "My Task", blocked=True)
    assert b.title == "My Task [blocked]", b.title

    # child create/close
    child = mgr.upsert_child("seb/test", "c1", "Sub-task", due="2026-04-01")
    assert child.cloud_id
    mgr.close_child("seb/test", "c1")
    assert "c1" not in mgr._require_item("seb/test").children

    # list and close
    assert len(mgr.list_items()) == 1
    mgr.close("seb/test", complete=True)
    assert not mgr.list_items()

    # standalone render_notes
    s = QueueItem(key="x", title="X", status_line="s", checklist=[ChecklistItem("[!]", "u")])
    assert render_notes(s) == "s\n[!] u"

    # checklist parser
    assert _parse_checklist_line("[ ] go") == ChecklistItem("[ ]", "go")
    assert _parse_checklist_line("[x] done") == ChecklistItem("[x]", "done")
    assert _parse_checklist_line("[!] warn") == ChecklistItem("[!]", "warn")
    assert _parse_checklist_line("[~] skip") == ChecklistItem("[~]", "skip")

    print("\nAll smoke tests passed.")
