"""
state.py -- SQLite state layer for icloud-reminders (Python side).

Mirrors internal/store + internal/queue tables. Shares the DB at
~/.config/icloud-reminders/state.db with the Go binary; all DDL is
idempotent so running migrations against an existing DB is safe.
"""

import fcntl
import json
import os
import sqlite3
import uuid
from datetime import datetime, timezone

_DEFAULT_DB_PATH = os.path.join(os.path.expanduser("~"), ".config", "icloud-reminders", "state.db")
_LOCK_PATH = os.path.join(os.path.expanduser("~"), ".config", "icloud-reminders", "mutations.lock")


def _now_utc() -> str:
    return datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


_MIGRATIONS = [
    """CREATE TABLE IF NOT EXISTS operations (
        op_id TEXT PRIMARY KEY, kind TEXT NOT NULL, target_type TEXT NOT NULL,
        target_key TEXT NOT NULL, desired_json TEXT NOT NULL, backend_json TEXT,
        status TEXT NOT NULL, error_text TEXT, attempt_count INTEGER NOT NULL DEFAULT 0,
        created_at TEXT NOT NULL, updated_at TEXT NOT NULL, applied_at TEXT,
        verified_at TEXT, validator_status TEXT, validator_checked_at TEXT)""",
    "CREATE INDEX IF NOT EXISTS idx_operations_status_created_at ON operations(status, created_at DESC)",
    """CREATE TABLE IF NOT EXISTS targets (
        target_type TEXT NOT NULL, target_key TEXT NOT NULL, cloud_id TEXT,
        apple_id TEXT, list_id TEXT, title TEXT, projection_json TEXT,
        last_cloud_verify_at TEXT, PRIMARY KEY (target_type, target_key))""",
    """CREATE TABLE IF NOT EXISTS queue_items (
        queue_key TEXT PRIMARY KEY, title TEXT NOT NULL, cloud_id TEXT, apple_id TEXT,
        section_name TEXT, tags_json TEXT, priority_value INTEGER NOT NULL DEFAULT 0,
        updated_at TEXT, due_at TEXT, status_line TEXT, checklist_json TEXT,
        hours_budget REAL NOT NULL DEFAULT 0, tokens_budget INTEGER NOT NULL DEFAULT 0,
        hours_spent_settled REAL NOT NULL DEFAULT 0, tokens_spent_settled INTEGER NOT NULL DEFAULT 0,
        last_model TEXT, executor TEXT, blocked INTEGER NOT NULL DEFAULT 0,
        legacy_notes TEXT, last_hammer TEXT)""",
    """CREATE TABLE IF NOT EXISTS queue_children (
        parent_key TEXT NOT NULL, child_key TEXT NOT NULL, title TEXT NOT NULL,
        cloud_id TEXT, apple_id TEXT, updated_at TEXT, due_at TEXT,
        priority_value INTEGER NOT NULL DEFAULT 0, flagged INTEGER NOT NULL DEFAULT 0,
        PRIMARY KEY (parent_key, child_key),
        FOREIGN KEY (parent_key) REFERENCES queue_items(queue_key) ON DELETE CASCADE)""",
    """CREATE TABLE IF NOT EXISTS queue_bindings (
        session_key TEXT PRIMARY KEY, queue_key TEXT NOT NULL,
        FOREIGN KEY (queue_key) REFERENCES queue_items(queue_key) ON DELETE CASCADE)""",
            """CREATE TABLE IF NOT EXISTS pending_deletes (
            cloud_id TEXT PRIMARY KEY,
            created_at TEXT NOT NULL DEFAULT (datetime('now'))
        )""",
        """CREATE TABLE IF NOT EXISTS queue_leases (
        queue_key TEXT NOT NULL, session_key TEXT NOT NULL, agent_id TEXT,
        started_at TEXT NOT NULL, last_seen_at TEXT, model_name TEXT,
        token_count INTEGER NOT NULL DEFAULT 0,
        PRIMARY KEY (queue_key, session_key),
        FOREIGN KEY (queue_key) REFERENCES queue_items(queue_key) ON DELETE CASCADE)""",
    "CREATE TABLE IF NOT EXISTS ck_sync_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
    """CREATE TABLE IF NOT EXISTS ck_reminders (
        cloud_id TEXT PRIMARY KEY, title TEXT NOT NULL DEFAULT '',
        completed INTEGER NOT NULL DEFAULT 0, flagged INTEGER NOT NULL DEFAULT 0,
        priority INTEGER NOT NULL DEFAULT 0, due TEXT, notes TEXT, list_ref TEXT,
        parent_ref TEXT, hashtag_ids TEXT NOT NULL DEFAULT '[]', change_tag TEXT,
        modified_ts INTEGER, completion_date TEXT)""",
    "CREATE TABLE IF NOT EXISTS ck_lists (list_id TEXT PRIMARY KEY, name TEXT NOT NULL)",
    """CREATE TABLE IF NOT EXISTS ck_sections (
        section_id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '',
        canonical_name TEXT, list_ref TEXT, change_tag TEXT)""",
    """CREATE TABLE IF NOT EXISTS ck_hashtags (
        hashtag_id TEXT PRIMARY KEY, name TEXT NOT NULL DEFAULT '',
        reminder_ref TEXT, change_tag TEXT)""",
    "CREATE TABLE IF NOT EXISTS kv_state (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
]


class _FileLock:
    """Exclusive flock -- mirrors Go syscall.Flock(LOCK_EX) on mutations.lock."""

    def __init__(self, path: str):
        os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
        self._path = path

    def __enter__(self):
        self._f = open(self._path, "a+")
        fcntl.flock(self._f.fileno(), fcntl.LOCK_EX)
        return self

    def __exit__(self, *_):
        fcntl.flock(self._f.fileno(), fcntl.LOCK_UN)
        self._f.close()


class StateDB:
    def __init__(self, db_path: str | None = None):
        """Open/create the DB, run idempotent migrations."""
        path = db_path or os.environ.get("ICLOUD_REMINDERS_DB_PATH") or _DEFAULT_DB_PATH
        os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
        self._conn = sqlite3.connect(path, check_same_thread=False)
        self._conn.row_factory = sqlite3.Row
        self._conn.execute("PRAGMA journal_mode = WAL")
        self._conn.execute("PRAGMA foreign_keys = ON")
        self._conn.execute("PRAGMA busy_timeout = 5000")
        with self._conn:
            for stmt in _MIGRATIONS:
                self._conn.execute(stmt)

    def _lock(self):
        return _FileLock(_LOCK_PATH)

    def upsert_queue_item(self, key: str, title: str, **kwargs) -> dict:
        """Insert or replace a queue item; kwargs map directly to column names."""
        kwargs.setdefault("updated_at", _now_utc())
        for col in ("tags_json", "checklist_json"):
            if col in kwargs and not isinstance(kwargs[col], str):
                kwargs[col] = json.dumps(kwargs[col])
        cols = ["queue_key", "title"] + list(kwargs.keys())
        vals = [key, title] + list(kwargs.values())
        ph = ", ".join(["?"] * len(vals))
        updates = ", ".join(f"{c} = excluded.{c}" for c in cols if c != "queue_key")
        with self._lock(), self._conn:
            self._conn.execute(
                f"INSERT INTO queue_items ({', '.join(cols)}) VALUES ({ph})"
                f" ON CONFLICT(queue_key) DO UPDATE SET {updates}",
                vals,
            )
        return self.get_queue_item(key)

    def get_queue_item(self, key: str) -> "dict | None":
        row = self._conn.execute("SELECT * FROM queue_items WHERE queue_key = ?", (key,)).fetchone()
        return dict(row) if row else None

    def list_queue_items(self) -> list:
        return [dict(r) for r in self._conn.execute(
            "SELECT * FROM queue_items ORDER BY priority_value DESC, queue_key").fetchall()]

    def delete_queue_item(self, key: str):
        with self._lock(), self._conn:
            self._conn.execute("DELETE FROM queue_items WHERE queue_key = ?", (key,))

    def upsert_queue_child(self, parent_key: str, child_key: str, title: str, **kwargs) -> dict:
        kwargs.setdefault("updated_at", _now_utc())
        cols = ["parent_key", "child_key", "title"] + list(kwargs.keys())
        vals = [parent_key, child_key, title] + list(kwargs.values())
        ph = ", ".join(["?"] * len(vals))
        pks = {"parent_key", "child_key"}
        updates = ", ".join(f"{c} = excluded.{c}" for c in cols if c not in pks)
        with self._lock(), self._conn:
            self._conn.execute(
                f"INSERT INTO queue_children ({', '.join(cols)}) VALUES ({ph})"
                f" ON CONFLICT(parent_key, child_key) DO UPDATE SET {updates}",
                vals,
            )
        return dict(self._conn.execute(
            "SELECT * FROM queue_children WHERE parent_key = ? AND child_key = ?",
            (parent_key, child_key)).fetchone())

    def list_queue_children(self, parent_key: str) -> list:
        return [dict(r) for r in self._conn.execute(
            "SELECT * FROM queue_children WHERE parent_key = ?"
            " ORDER BY priority_value DESC, child_key", (parent_key,)).fetchall()]

    def delete_queue_child(self, parent_key: str, child_key: str):
        with self._lock(), self._conn:
            self._conn.execute(
                "DELETE FROM queue_children WHERE parent_key = ? AND child_key = ?",
                (parent_key, child_key))

    def record_operation(self, op_id: "str | None", kind: str, target_type: str,
                         target_key: str, desired_json: "str | dict",
                         status: str = "pending") -> dict:
        if op_id is None:
            op_id = str(uuid.uuid4())
        if not isinstance(desired_json, str):
            desired_json = json.dumps(desired_json)
        now = _now_utc()
        with self._lock(), self._conn:
            self._conn.execute(
                "INSERT INTO operations (op_id, kind, target_type, target_key, desired_json,"
                " status, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
                (op_id, kind, target_type, target_key, desired_json, status, now, now))
        return dict(self._conn.execute(
            "SELECT * FROM operations WHERE op_id = ?", (op_id,)).fetchone())

    def update_operation(self, op_id: str, **kwargs):
        """Update mutable fields on an operation row."""
        kwargs["updated_at"] = _now_utc()
        if "backend_json" in kwargs and not isinstance(kwargs["backend_json"], str):
            kwargs["backend_json"] = json.dumps(kwargs["backend_json"])
        set_clause = ", ".join(f"{k} = ?" for k in kwargs)
        with self._lock(), self._conn:
            self._conn.execute(
                f"UPDATE operations SET {set_clause} WHERE op_id = ?",
                list(kwargs.values()) + [op_id])

    def list_operations(self, statuses: "list | None" = None) -> list:
        if statuses:
            ph = ", ".join(["?"] * len(statuses))
            rows = self._conn.execute(
                f"SELECT * FROM operations WHERE status IN ({ph}) ORDER BY created_at DESC",
                statuses).fetchall()
        else:
            rows = self._conn.execute(
                "SELECT * FROM operations ORDER BY created_at DESC").fetchall()
        return [dict(r) for r in rows]

    def get_state(self, key: str) -> "str | None":
        row = self._conn.execute("SELECT value FROM kv_state WHERE key = ?", (key,)).fetchone()
        return row[0] if row else None

    def set_state(self, key: str, value: str):
        with self._conn:
            self._conn.execute(
                "INSERT INTO kv_state (key, value) VALUES (?, ?)"
                " ON CONFLICT(key) DO UPDATE SET value = excluded.value", (key, value))

    def add_pending_delete(self, cloud_id):
        if not cloud_id: return
        with self._lock():
            self._conn.execute(
                "INSERT OR IGNORE INTO pending_deletes (cloud_id) VALUES (?)",
                (cloud_id,))
            self._conn.commit()

    def list_pending_deletes(self):
        rows = self._conn.execute("SELECT cloud_id FROM pending_deletes").fetchall()
        return [r[0] for r in rows]

    def clear_pending_delete(self, cloud_id):
        with self._lock():
            self._conn.execute("DELETE FROM pending_deletes WHERE cloud_id=?", (cloud_id,))
            self._conn.commit()

    def close(self):
        self._conn.close()

    def __enter__(self):
        return self

    def __exit__(self, *_):
        self.close()


# Smoke test

if __name__ == "__main__":
    import tempfile

    with tempfile.TemporaryDirectory() as tmp:
        with StateDB(db_path=os.path.join(tmp, "state.db")) as db:
            inserted = db.upsert_queue_item(
                "task-1", "Write state.py",
                tags_json=["python", "sqlite"], hours_budget=2.0,
                tokens_budget=50000, status_line="in progress", priority_value=10)
            assert inserted["queue_key"] == "task-1"
            assert inserted["hours_budget"] == 2.0

            assert db.get_queue_item("task-1")["priority_value"] == 10
            assert len(db.list_queue_items()) == 1

            child = db.upsert_queue_child("task-1", "subtask-a", "Review schema")
            assert child["parent_key"] == "task-1"
            assert len(db.list_queue_children("task-1")) == 1

            op = db.record_operation(None, "upsert", "reminder", "task-1", {"title": "Write state.py"})
            assert op["status"] == "pending"
            db.update_operation(op["op_id"], status="applied", attempt_count=1)
            ops = db.list_operations(statuses=["applied"])
            assert len(ops) == 1 and ops[0]["attempt_count"] == 1

            db.set_state("sync_token", "tok-abc")
            assert db.get_state("sync_token") == "tok-abc"

            db.delete_queue_child("task-1", "subtask-a")
            assert db.list_queue_children("task-1") == []
            db.delete_queue_item("task-1")
            assert db.get_queue_item("task-1") is None

    print("OK -- all smoke tests passed")
