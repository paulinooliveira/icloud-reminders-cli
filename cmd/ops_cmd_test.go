package cmd

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"icloud-reminders/internal/store"
)

func TestCompactSupersededOperationsMarksOlderOutstandingRows(t *testing.T) {
	db := openTestStoreDB(t)

	older := store.Operation{
		ID:          "older-op",
		Kind:        "queue-upsert",
		TargetType:  "queue",
		TargetKey:   "schedule/morning-day-briefing",
		DesiredJSON: `{"title":"older"}`,
		Status:      "pending",
		CreatedAt:   "2026-03-24T18:00:00Z",
		UpdatedAt:   "2026-03-24T18:00:00Z",
	}
	newer := store.Operation{
		ID:          "newer-op",
		Kind:        "queue-upsert",
		TargetType:  "queue",
		TargetKey:   "schedule/morning-day-briefing",
		DesiredJSON: `{"title":"newer"}`,
		Status:      "verified",
		CreatedAt:   "2026-03-24T18:05:00Z",
		UpdatedAt:   "2026-03-24T18:05:00Z",
		VerifiedAt:  "2026-03-24T18:05:01Z",
	}
	if err := store.ExecTx(db, func(tx *sql.Tx) error {
		if err := store.InsertOperation(tx, older); err != nil {
			return err
		}
		return store.InsertOperation(tx, newer)
	}); err != nil {
		t.Fatalf("insert operations: %v", err)
	}

	if err := compactSupersededOperations(db); err != nil {
		t.Fatalf("compactSupersededOperations: %v", err)
	}

	reloadedOlder, err := store.GetOperation(db, older.ID)
	if err != nil {
		t.Fatalf("get older op: %v", err)
	}
	if reloadedOlder == nil {
		t.Fatalf("older operation missing after compaction")
	}
	if reloadedOlder.Status != "superseded" {
		t.Fatalf("expected older op to be superseded, got %q", reloadedOlder.Status)
	}
	if reloadedOlder.VerifiedAt == "" {
		t.Fatalf("expected superseded op to get verified timestamp")
	}

	reloadedNewer, err := store.GetOperation(db, newer.ID)
	if err != nil {
		t.Fatalf("get newer op: %v", err)
	}
	if reloadedNewer == nil || reloadedNewer.Status != "verified" {
		t.Fatalf("expected newer op to remain verified, got %#v", reloadedNewer)
	}
}

func TestCompactSupersededOperationsKeepsLatestOutstandingRow(t *testing.T) {
	db := openTestStoreDB(t)

	ops := []store.Operation{
		{
			ID:          "older-pending",
			Kind:        "queue-upsert",
			TargetType:  "queue",
			TargetKey:   "schedule/steward-daily-review",
			DesiredJSON: `{}`,
			Status:      "pending",
			CreatedAt:   "2026-03-24T18:00:00Z",
			UpdatedAt:   "2026-03-24T18:00:00Z",
		},
		{
			ID:          "middle-failed",
			Kind:        "queue-upsert",
			TargetType:  "queue",
			TargetKey:   "schedule/steward-daily-review",
			DesiredJSON: `{}`,
			Status:      "failed",
			CreatedAt:   "2026-03-24T18:02:00Z",
			UpdatedAt:   "2026-03-24T18:02:00Z",
			ErrorText:   "old failure",
		},
		{
			ID:          "latest-pending",
			Kind:        "queue-upsert",
			TargetType:  "queue",
			TargetKey:   "schedule/steward-daily-review",
			DesiredJSON: `{}`,
			Status:      "pending",
			CreatedAt:   "2026-03-24T18:04:00Z",
			UpdatedAt:   "2026-03-24T18:04:00Z",
		},
	}
	if err := store.ExecTx(db, func(tx *sql.Tx) error {
		for _, op := range ops {
			if err := store.InsertOperation(tx, op); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("insert operations: %v", err)
	}

	if err := compactSupersededOperations(db); err != nil {
		t.Fatalf("compactSupersededOperations: %v", err)
	}

	for _, opID := range []string{"older-pending", "middle-failed"} {
		op, err := store.GetOperation(db, opID)
		if err != nil {
			t.Fatalf("get %s: %v", opID, err)
		}
		if op == nil || op.Status != "superseded" {
			t.Fatalf("expected %s to be superseded, got %#v", opID, op)
		}
	}
	latest, err := store.GetOperation(db, "latest-pending")
	if err != nil {
		t.Fatalf("get latest op: %v", err)
	}
	if latest == nil || latest.Status != "pending" {
		t.Fatalf("expected latest op to remain pending, got %#v", latest)
	}
}

func TestPruneHistoricalOperationsRemovesOlderVerifiedAndSupersededRows(t *testing.T) {
	db := openTestStoreDB(t)

	ops := []store.Operation{
		{
			ID:          "verified-newest",
			Kind:        "queue-upsert",
			TargetType:  "queue",
			TargetKey:   "schedule/morning-day-briefing",
			DesiredJSON: `{}`,
			Status:      "verified",
			CreatedAt:   "2026-03-24T18:04:00Z",
			UpdatedAt:   "2026-03-24T18:04:00Z",
			VerifiedAt:  "2026-03-24T18:04:01Z",
		},
		{
			ID:          "verified-older",
			Kind:        "queue-upsert",
			TargetType:  "queue",
			TargetKey:   "schedule/morning-day-briefing",
			DesiredJSON: `{}`,
			Status:      "verified",
			CreatedAt:   "2026-03-24T18:02:00Z",
			UpdatedAt:   "2026-03-24T18:02:00Z",
			VerifiedAt:  "2026-03-24T18:02:01Z",
		},
		{
			ID:          "superseded-old",
			Kind:        "queue-upsert",
			TargetType:  "queue",
			TargetKey:   "schedule/morning-day-briefing",
			DesiredJSON: `{}`,
			Status:      "superseded",
			CreatedAt:   "2026-03-24T18:01:00Z",
			UpdatedAt:   "2026-03-24T18:01:00Z",
			VerifiedAt:  "2026-03-24T18:01:01Z",
		},
		{
			ID:          "other-target-verified",
			Kind:        "delete",
			TargetType:  "reminder",
			TargetKey:   "A0D3AAC1-8AD6-449B-83A5-07EA5A36EEA7",
			DesiredJSON: `{}`,
			Status:      "verified",
			CreatedAt:   "2026-03-24T18:03:00Z",
			UpdatedAt:   "2026-03-24T18:03:00Z",
			VerifiedAt:  "2026-03-24T18:03:01Z",
		},
	}
	if err := store.ExecTx(db, func(tx *sql.Tx) error {
		for _, op := range ops {
			if err := store.InsertOperation(tx, op); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("insert operations: %v", err)
	}

	if err := pruneHistoricalOperations(db); err != nil {
		t.Fatalf("pruneHistoricalOperations: %v", err)
	}

	if op, err := store.GetOperation(db, "verified-older"); err != nil {
		t.Fatalf("get verified-older: %v", err)
	} else if op != nil {
		t.Fatalf("expected verified-older to be deleted, got %#v", op)
	}
	if op, err := store.GetOperation(db, "superseded-old"); err != nil {
		t.Fatalf("get superseded-old: %v", err)
	} else if op != nil {
		t.Fatalf("expected superseded-old to be deleted, got %#v", op)
	}
	if op, err := store.GetOperation(db, "verified-newest"); err != nil {
		t.Fatalf("get verified-newest: %v", err)
	} else if op == nil || op.Status != "verified" {
		t.Fatalf("expected verified-newest to remain, got %#v", op)
	}
	if op, err := store.GetOperation(db, "other-target-verified"); err != nil {
		t.Fatalf("get other-target-verified: %v", err)
	} else if op == nil || op.Status != "verified" {
		t.Fatalf("expected other-target-verified to remain, got %#v", op)
	}
}

func openTestStoreDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("ICLOUD_REMINDERS_DB_PATH", filepath.Join(t.TempDir(), "state.db"))
	db, err := store.Open()
	if err != nil {
		t.Fatalf("open test store: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if err := db.Ping(); err != nil {
		t.Fatalf("ping test store: %v", err)
	}
	// Give SQLite a moment to settle WAL setup in the temp dir on slower runners.
	time.Sleep(10 * time.Millisecond)
	return db
}
