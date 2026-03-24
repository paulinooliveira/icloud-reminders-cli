package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

var mutationMu sync.Mutex

func ConfigDir() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "icloud-reminders")
}

func DBPath() string {
	if override := os.Getenv("ICLOUD_REMINDERS_DB_PATH"); override != "" {
		return override
	}
	return filepath.Join(ConfigDir(), "state.db")
}

func LockPath() string {
	return filepath.Join(filepath.Dir(DBPath()), "mutations.lock")
}

func Open() (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(DBPath()), 0700); err != nil {
		return nil, err
	}
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)", DBPath())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA journal_mode = WAL`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func WithMutationLock(fn func() error) error {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	if err := os.MkdirAll(filepath.Dir(LockPath()), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(LockPath(), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func ExecTx(db *sql.DB, fn func(*sql.Tx) error) error {
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

type Operation struct {
	ID               string
	Kind             string
	TargetType       string
	TargetKey        string
	DesiredJSON      string
	BackendJSON      string
	Status           string
	ErrorText        string
	AttemptCount     int
	CreatedAt        string
	UpdatedAt        string
	AppliedAt        string
	VerifiedAt       string
	ValidatorStatus  string
	ValidatorChecked string
}

func InsertOperation(tx *sql.Tx, op Operation) error {
	now := time.Now().UTC().Format(time.RFC3339)
	if op.CreatedAt == "" {
		op.CreatedAt = now
	}
	if op.UpdatedAt == "" {
		op.UpdatedAt = now
	}
	_, err := tx.Exec(`
		INSERT INTO operations (
			op_id, kind, target_type, target_key, desired_json, backend_json,
			status, error_text, attempt_count, created_at, updated_at,
			applied_at, verified_at, validator_status, validator_checked_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, op.ID, op.Kind, op.TargetType, op.TargetKey, op.DesiredJSON, nullIfEmpty(op.BackendJSON),
		op.Status, nullIfEmpty(op.ErrorText), op.AttemptCount, op.CreatedAt, op.UpdatedAt,
		nullIfEmpty(op.AppliedAt), nullIfEmpty(op.VerifiedAt), nullIfEmpty(op.ValidatorStatus), nullIfEmpty(op.ValidatorChecked))
	return err
}

func UpdateOperation(db *sql.DB, op Operation) error {
	op.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(`
		UPDATE operations
		SET backend_json = ?, status = ?, error_text = ?, attempt_count = ?,
		    updated_at = ?, applied_at = ?, verified_at = ?, validator_status = ?,
		    validator_checked_at = ?
		WHERE op_id = ?
	`, nullIfEmpty(op.BackendJSON), op.Status, nullIfEmpty(op.ErrorText), op.AttemptCount,
		op.UpdatedAt, nullIfEmpty(op.AppliedAt), nullIfEmpty(op.VerifiedAt),
		nullIfEmpty(op.ValidatorStatus), nullIfEmpty(op.ValidatorChecked), op.ID)
	return err
}

func ListOperations(db *sql.DB, statuses ...string) ([]Operation, error) {
	query := `SELECT op_id, kind, target_type, target_key, COALESCE(desired_json,''), COALESCE(backend_json,''),
		status, COALESCE(error_text,''), attempt_count, COALESCE(created_at,''), COALESCE(updated_at,''),
		COALESCE(applied_at,''), COALESCE(verified_at,''), COALESCE(validator_status,''), COALESCE(validator_checked_at,'')
		FROM operations`
	args := []any{}
	if len(statuses) > 0 {
		query += ` WHERE status IN (` + placeholders(len(statuses)) + `)`
		for _, status := range statuses {
			args = append(args, status)
		}
	}
	query += ` ORDER BY created_at DESC`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ops []Operation
	for rows.Next() {
		var op Operation
		if err := rows.Scan(&op.ID, &op.Kind, &op.TargetType, &op.TargetKey, &op.DesiredJSON, &op.BackendJSON,
			&op.Status, &op.ErrorText, &op.AttemptCount, &op.CreatedAt, &op.UpdatedAt,
			&op.AppliedAt, &op.VerifiedAt, &op.ValidatorStatus, &op.ValidatorChecked); err != nil {
			return nil, err
		}
		ops = append(ops, op)
	}
	return ops, rows.Err()
}

func GetOperation(db *sql.DB, opID string) (*Operation, error) {
	row := db.QueryRow(`SELECT op_id, kind, target_type, target_key, COALESCE(desired_json,''), COALESCE(backend_json,''),
		status, COALESCE(error_text,''), attempt_count, COALESCE(created_at,''), COALESCE(updated_at,''),
		COALESCE(applied_at,''), COALESCE(verified_at,''), COALESCE(validator_status,''), COALESCE(validator_checked_at,'')
		FROM operations WHERE op_id = ?`, opID)
	var op Operation
	if err := row.Scan(&op.ID, &op.Kind, &op.TargetType, &op.TargetKey, &op.DesiredJSON, &op.BackendJSON,
		&op.Status, &op.ErrorText, &op.AttemptCount, &op.CreatedAt, &op.UpdatedAt,
		&op.AppliedAt, &op.VerifiedAt, &op.ValidatorStatus, &op.ValidatorChecked); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &op, nil
}

func SaveTargetProjection(db *sql.DB, targetType, targetKey, cloudID, appleID, listID, title, projectionJSON string) error {
	_, err := db.Exec(`
		INSERT INTO targets (target_type, target_key, cloud_id, apple_id, list_id, title, projection_json, last_cloud_verify_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(target_type, target_key) DO UPDATE SET
			cloud_id = excluded.cloud_id,
			apple_id = excluded.apple_id,
			list_id = excluded.list_id,
			title = excluded.title,
			projection_json = excluded.projection_json,
			last_cloud_verify_at = excluded.last_cloud_verify_at
	`, targetType, targetKey, nullIfEmpty(cloudID), nullIfEmpty(appleID), nullIfEmpty(listID), nullIfEmpty(title), nullIfEmpty(projectionJSON), time.Now().UTC().Format(time.RFC3339))
	return err
}

func DeleteTargetProjection(db *sql.DB, targetType, targetKey string) error {
	_, err := db.Exec(`DELETE FROM targets WHERE target_type = ? AND target_key = ?`, targetType, targetKey)
	return err
}

func DeleteOperations(tx *sql.Tx, opIDs []string) error {
	if len(opIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(opIDs))
	for _, opID := range opIDs {
		args = append(args, opID)
	}
	_, err := tx.Exec(`DELETE FROM operations WHERE op_id IN (`+placeholders(len(opIDs))+`)`, args...)
	return err
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	out := "?"
	for i := 1; i < n; i++ {
		out += ", ?"
	}
	return out
}

func nullIfEmpty(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS operations (
			op_id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			target_type TEXT NOT NULL,
			target_key TEXT NOT NULL,
			desired_json TEXT NOT NULL,
			backend_json TEXT,
			status TEXT NOT NULL,
			error_text TEXT,
			attempt_count INTEGER NOT NULL DEFAULT 0,
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			applied_at TEXT,
			verified_at TEXT,
			validator_status TEXT,
			validator_checked_at TEXT
		)`,
		`CREATE INDEX IF NOT EXISTS idx_operations_status_created_at ON operations(status, created_at DESC)`,
		`CREATE TABLE IF NOT EXISTS targets (
			target_type TEXT NOT NULL,
			target_key TEXT NOT NULL,
			cloud_id TEXT,
			apple_id TEXT,
			list_id TEXT,
			title TEXT,
			projection_json TEXT,
			last_cloud_verify_at TEXT,
			PRIMARY KEY (target_type, target_key)
		)`,
		`CREATE TABLE IF NOT EXISTS queue_items (
			queue_key TEXT PRIMARY KEY,
			title TEXT NOT NULL,
			cloud_id TEXT,
			apple_id TEXT,
			section_name TEXT,
			tags_json TEXT,
			priority_value INTEGER NOT NULL DEFAULT 0,
			updated_at TEXT,
			due_at TEXT,
			status_line TEXT,
			checklist_json TEXT,
			hours_budget REAL NOT NULL DEFAULT 0,
			tokens_budget INTEGER NOT NULL DEFAULT 0,
			hours_spent_settled REAL NOT NULL DEFAULT 0,
			tokens_spent_settled INTEGER NOT NULL DEFAULT 0,
			last_model TEXT,
			executor TEXT,
			blocked INTEGER NOT NULL DEFAULT 0,
			legacy_notes TEXT,
			last_hammer TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS queue_children (
			parent_key TEXT NOT NULL,
			child_key TEXT NOT NULL,
			title TEXT NOT NULL,
			cloud_id TEXT,
			apple_id TEXT,
			updated_at TEXT,
			due_at TEXT,
			priority_value INTEGER NOT NULL DEFAULT 0,
			flagged INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (parent_key, child_key),
			FOREIGN KEY (parent_key) REFERENCES queue_items(queue_key) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS queue_bindings (
			session_key TEXT PRIMARY KEY,
			queue_key TEXT NOT NULL,
			FOREIGN KEY (queue_key) REFERENCES queue_items(queue_key) ON DELETE CASCADE
		)`,
		`CREATE TABLE IF NOT EXISTS queue_leases (
			queue_key TEXT NOT NULL,
			session_key TEXT NOT NULL,
			agent_id TEXT,
			started_at TEXT NOT NULL,
			last_seen_at TEXT,
			model_name TEXT,
			token_count INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (queue_key, session_key),
			FOREIGN KEY (queue_key) REFERENCES queue_items(queue_key) ON DELETE CASCADE
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}
