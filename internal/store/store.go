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
	if err := migrateCKCache(db); err != nil {
		return err
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// CloudKit cache tables — replaces internal/cache ck_cache.json
// ---------------------------------------------------------------------------

// CKReminder is the DB row for a cached CloudKit reminder record.
type CKReminder struct {
	CloudID        string  // PRIMARY KEY, always "Reminder/<UPPER-UUID>"
	Title          string
	Completed      bool
	Flagged        bool
	Priority       int
	Due            *string
	Notes          *string
	ListRef        *string
	ParentRef      *string
	HashtagIDs     string  // JSON array
	ChangeTag      *string
	ModifiedTS     *int64
	CompletionDate *string
}

func migrateCKCache(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS ck_sync_state (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ck_reminders (
			cloud_id        TEXT PRIMARY KEY CHECK(cloud_id LIKE 'Reminder/%'),
			title           TEXT NOT NULL DEFAULT '',
			completed       INTEGER NOT NULL DEFAULT 0,
			flagged         INTEGER NOT NULL DEFAULT 0,
			priority        INTEGER NOT NULL DEFAULT 0,
			due             TEXT,
			notes           TEXT,
			list_ref        TEXT,
			parent_ref      TEXT,
			hashtag_ids     TEXT NOT NULL DEFAULT '[]',
			change_tag      TEXT,
			modified_ts     INTEGER,
			completion_date TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS ck_lists (
			list_id TEXT PRIMARY KEY,
			name    TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ck_sections (
			section_id     TEXT PRIMARY KEY,
			name           TEXT NOT NULL DEFAULT '',
			canonical_name TEXT,
			list_ref       TEXT,
			change_tag     TEXT
		)`,
		`CREATE TABLE IF NOT EXISTS ck_hashtags (
			hashtag_id   TEXT PRIMARY KEY,
			name         TEXT NOT NULL DEFAULT '',
			reminder_ref TEXT,
			change_tag   TEXT
		)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("migrateCKCache: %w", err)
		}
	}
	return nil
}

// UpsertCKReminder inserts or replaces a reminder row.
// cloud_id must be in canonical "Reminder/<UPPER-UUID>" form — the DB CHECK
// constraint enforces this.
func UpsertCKReminder(db *sql.DB, r CKReminder) error {
	_, err := db.Exec(`
		INSERT INTO ck_reminders
			(cloud_id, title, completed, flagged, priority, due, notes,
			 list_ref, parent_ref, hashtag_ids, change_tag, modified_ts, completion_date)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(cloud_id) DO UPDATE SET
			title=excluded.title, completed=excluded.completed,
			flagged=excluded.flagged, priority=excluded.priority,
			due=excluded.due, notes=excluded.notes,
			list_ref=excluded.list_ref, parent_ref=excluded.parent_ref,
			hashtag_ids=excluded.hashtag_ids, change_tag=excluded.change_tag,
			modified_ts=excluded.modified_ts, completion_date=excluded.completion_date`,
		r.CloudID, r.Title, boolInt(r.Completed), boolInt(r.Flagged),
		r.Priority, r.Due, r.Notes, r.ListRef, r.ParentRef,
		r.HashtagIDs, r.ChangeTag, r.ModifiedTS, r.CompletionDate,
	)
	return err
}

// DeleteCKReminder removes a reminder and all its aliases.
func DeleteCKReminder(db *sql.DB, cloudID string) error {
	_, err := db.Exec(`DELETE FROM ck_reminders WHERE cloud_id = ?`, cloudID)
	return err
}

// GetCKReminder fetches one reminder by canonical cloud_id.
func GetCKReminder(db *sql.DB, cloudID string) (*CKReminder, error) {
	row := db.QueryRow(`SELECT cloud_id,title,completed,flagged,priority,due,notes,
		list_ref,parent_ref,hashtag_ids,change_tag,modified_ts,completion_date
		FROM ck_reminders WHERE cloud_id=?`, cloudID)
	return scanCKReminder(row)
}

// ListCKReminders returns all non-completed or all reminders.
func ListCKReminders(db *sql.DB, includeCompleted bool) ([]*CKReminder, error) {
	q := `SELECT cloud_id,title,completed,flagged,priority,due,notes,
		list_ref,parent_ref,hashtag_ids,change_tag,modified_ts,completion_date
		FROM ck_reminders`
	if !includeCompleted {
		q += ` WHERE completed=0`
	}
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*CKReminder
	for rows.Next() {
		r, err := scanCKReminder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanCKReminder(row rowScanner) (*CKReminder, error) {
	var r CKReminder
	var completed, flagged int
	err := row.Scan(&r.CloudID, &r.Title, &completed, &flagged, &r.Priority,
		&r.Due, &r.Notes, &r.ListRef, &r.ParentRef, &r.HashtagIDs,
		&r.ChangeTag, &r.ModifiedTS, &r.CompletionDate)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	r.Completed = completed != 0
	r.Flagged = flagged != 0
	return &r, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// UpsertCKList inserts or replaces a list name.
func UpsertCKList(db *sql.DB, listID, name string) error {
	_, err := db.Exec(`INSERT INTO ck_lists (list_id,name) VALUES(?,?)
		ON CONFLICT(list_id) DO UPDATE SET name=excluded.name`, listID, name)
	return err
}

// DeleteCKList removes a list.
func DeleteCKList(db *sql.DB, listID string) error {
	_, err := db.Exec(`DELETE FROM ck_lists WHERE list_id=?`, listID)
	return err
}

// ListCKLists returns all lists as id→name map.
func ListCKLists(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT list_id, name FROM ck_lists`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// CKSection mirrors SectionData.
type CKSection struct {
	SectionID     string
	Name          string
	CanonicalName *string
	ListRef       *string
	ChangeTag     *string
}

// UpsertCKSection inserts or replaces a section.
func UpsertCKSection(db *sql.DB, s CKSection) error {
	_, err := db.Exec(`INSERT INTO ck_sections (section_id,name,canonical_name,list_ref,change_tag)
		VALUES(?,?,?,?,?)
		ON CONFLICT(section_id) DO UPDATE SET
			name=excluded.name, canonical_name=excluded.canonical_name,
			list_ref=excluded.list_ref, change_tag=excluded.change_tag`,
		s.SectionID, s.Name, s.CanonicalName, s.ListRef, s.ChangeTag)
	return err
}

// DeleteCKSection removes a section.
func DeleteCKSection(db *sql.DB, sectionID string) error {
	_, err := db.Exec(`DELETE FROM ck_sections WHERE section_id=?`, sectionID)
	return err
}

// ListCKSections returns all sections as id→CKSection map.
func ListCKSections(db *sql.DB) (map[string]*CKSection, error) {
	rows, err := db.Query(`SELECT section_id,name,canonical_name,list_ref,change_tag FROM ck_sections`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*CKSection{}
	for rows.Next() {
		var s CKSection
		if err := rows.Scan(&s.SectionID, &s.Name, &s.CanonicalName, &s.ListRef, &s.ChangeTag); err != nil {
			return nil, err
		}
		out[s.SectionID] = &s
	}
	return out, rows.Err()
}

// CKHashtag mirrors HashtagData.
type CKHashtag struct {
	HashtagID   string
	Name        string
	ReminderRef *string
	ChangeTag   *string
}

// UpsertCKHashtag inserts or replaces a hashtag.
func UpsertCKHashtag(db *sql.DB, h CKHashtag) error {
	_, err := db.Exec(`INSERT INTO ck_hashtags (hashtag_id,name,reminder_ref,change_tag)
		VALUES(?,?,?,?)
		ON CONFLICT(hashtag_id) DO UPDATE SET
			name=excluded.name, reminder_ref=excluded.reminder_ref,
			change_tag=excluded.change_tag`,
		h.HashtagID, h.Name, h.ReminderRef, h.ChangeTag)
	return err
}

// DeleteCKHashtag removes a hashtag.
func DeleteCKHashtag(db *sql.DB, hashtagID string) error {
	_, err := db.Exec(`DELETE FROM ck_hashtags WHERE hashtag_id=?`, hashtagID)
	return err
}

// ListCKHashtags returns all hashtags as id→CKHashtag map.
func ListCKHashtags(db *sql.DB) (map[string]*CKHashtag, error) {
	rows, err := db.Query(`SELECT hashtag_id,name,reminder_ref,change_tag FROM ck_hashtags`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*CKHashtag{}
	for rows.Next() {
		var h CKHashtag
		if err := rows.Scan(&h.HashtagID, &h.Name, &h.ReminderRef, &h.ChangeTag); err != nil {
			return nil, err
		}
		out[h.HashtagID] = &h
	}
	return out, rows.Err()
}

// GetCKSyncState fetches a sync state value by key ("sync_token", "owner_id").
func GetCKSyncState(db *sql.DB, key string) (string, error) {
	var val string
	err := db.QueryRow(`SELECT value FROM ck_sync_state WHERE key=?`, key).Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetCKSyncState upserts a sync state value.
func SetCKSyncState(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO ck_sync_state (key,value) VALUES(?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}
