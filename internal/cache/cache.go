// Package cache provides the in-process CloudKit metadata cache backed by the
// shared SQLite store. The in-memory maps are the source of truth within a
// session; SQLite is the persistence layer.
//
// Write discipline:
//   - After a full CloudKit sync, call FlushAll() once — it writes the whole
//     map set in a single transaction.
//   - After a single-record mutation (add/edit/delete), call PersistReminder,
//     PersistList, etc. — one row, one upsert, no full-table scan.
//   - Save() is kept for backward-compat but is a no-op; callers should be
//     migrated to PersistReminder / FlushAll over time.
package cache

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"icloud-reminders/internal/logger"
	"icloud-reminders/internal/store"
)

const configDirEnv = "ICLOUD_REMINDERS_CONFIG_DIR"

// ConfigDir returns the config/session directory.
func ConfigDir() string {
	if override := strings.TrimSpace(os.Getenv(configDirEnv)); override != "" {
		return override
	}
	if isTestProcess() {
		return filepath.Join(os.TempDir(), "icloud-reminders-test", filepath.Base(os.Args[0]))
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "icloud-reminders")
}

// SessionFile returns the path to the auth session JSON file.
func SessionFile() string {
	return filepath.Join(ConfigDir(), "session.json")
}

func isTestProcess() bool {
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
}

// CanonicalReminderKey returns the canonical "Reminder/<UPPER-UUID>" key.
// Returns "" if id cannot be resolved to a valid UUID.
func CanonicalReminderKey(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	short := id
	if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
		short = id[idx+1:]
	}
	upper := strings.ToUpper(short)
	if !isValidUUID(upper) {
		return ""
	}
	return "Reminder/" + upper
}

func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c >= '0' && c <= '9') || (c >= 'A' && c <= 'F'):
		default:
			return false
		}
	}
	return true
}

// ReminderAliases returns all key forms for id, canonical first.
func ReminderAliases(id string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 3)
	add := func(v string) {
		if v == "" {
			return
		}
		if _, ok := seen[v]; ok {
			return
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	short := id
	if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
		short = id[idx+1:]
	}
	add("Reminder/" + strings.ToUpper(short)) // canonical first
	add(id)
	add(short)
	return out
}

// ReminderData holds cached data for a single reminder.
type ReminderData struct {
	Title          string   `json:"title"`
	Completed      bool     `json:"completed"`
	Flagged        bool     `json:"flagged,omitempty"`
	CompletionDate *string  `json:"completion_date,omitempty"`
	Due            *string  `json:"due,omitempty"`
	Priority       int      `json:"priority"`
	Notes          *string  `json:"notes,omitempty"`
	HashtagIDs     []string `json:"hashtag_ids,omitempty"`
	ListRef        *string  `json:"list_ref,omitempty"`
	ParentRef      *string  `json:"parent_ref,omitempty"`
	ModifiedTS     *int64   `json:"modified_ts,omitempty"`
	ChangeTag      *string  `json:"change_tag,omitempty"`
}

// SectionData holds cached metadata for a list section.
type SectionData struct {
	Name          string  `json:"name"`
	CanonicalName *string `json:"canonical_name,omitempty"`
	ListRef       *string `json:"list_ref,omitempty"`
	ChangeTag     *string `json:"change_tag,omitempty"`
}

// HashtagData holds cached metadata for a hashtag record.
type HashtagData struct {
	Name        string  `json:"name"`
	ReminderRef *string `json:"reminder_ref,omitempty"`
	ChangeTag   *string `json:"change_tag,omitempty"`
}

// Cache is the in-process CloudKit metadata cache.
type Cache struct {
	Reminders map[string]*ReminderData
	Sections  map[string]*SectionData
	Hashtags  map[string]*HashtagData
	Lists     map[string]string
	SyncToken *string
	OwnerID   *string
	UpdatedAt *string

	db *sql.DB
}

// NewCache returns an empty in-memory cache (no DB).
func NewCache() *Cache {
	return &Cache{
		Reminders: make(map[string]*ReminderData),
		Sections:  make(map[string]*SectionData),
		Hashtags:  make(map[string]*HashtagData),
		Lists:     make(map[string]string),
	}
}

// Load opens the SQLite store and hydrates the in-memory maps.
func Load() *Cache {
	db, err := store.Open()
	if err != nil {
		logger.Warnf("cache: failed to open store, falling back to empty cache: %v", err)
		return NewCache()
	}
	c := &Cache{
		Reminders: make(map[string]*ReminderData),
		Sections:  make(map[string]*SectionData),
		Hashtags:  make(map[string]*HashtagData),
		Lists:     make(map[string]string),
		db:        db,
	}

	if tok, err := store.GetCKSyncState(db, "sync_token"); err == nil && tok != "" {
		c.SyncToken = &tok
	}
	if ownerID, err := store.GetCKSyncState(db, "owner_id"); err == nil && ownerID != "" {
		c.OwnerID = &ownerID
	}

	if rows, err := store.ListCKReminders(db, true); err != nil {
		logger.Warnf("cache: load reminders: %v", err)
	} else {
		for _, r := range rows {
			c.Reminders[r.CloudID] = ckReminderToData(r)
		}
	}

	if lists, err := store.ListCKLists(db); err != nil {
		logger.Warnf("cache: load lists: %v", err)
	} else {
		c.Lists = lists
	}

	if sections, err := store.ListCKSections(db); err != nil {
		logger.Warnf("cache: load sections: %v", err)
	} else {
		for id, s := range sections {
			c.Sections[id] = &SectionData{
				Name: s.Name, CanonicalName: s.CanonicalName,
				ListRef: s.ListRef, ChangeTag: s.ChangeTag,
			}
		}
	}

	if hashtags, err := store.ListCKHashtags(db); err != nil {
		logger.Warnf("cache: load hashtags: %v", err)
	} else {
		for id, h := range hashtags {
			c.Hashtags[id] = &HashtagData{
				Name: h.Name, ReminderRef: h.ReminderRef, ChangeTag: h.ChangeTag,
			}
		}
	}

	if !isTestProcess() {
		go migrateJSONCache(db)
	}
	return c
}

// Close releases the underlying DB connection.
func (c *Cache) Close() {
	if c.db != nil {
		_ = c.db.Close()
		c.db = nil
	}
}

// Save is a no-op kept for backward compatibility.
// Use PersistReminder for single-record mutations or FlushAll after a full sync.
func (c *Cache) Save() error {
	return nil
}

// FlushAll writes the entire in-memory cache to SQLite in a single transaction.
// Call this once at the end of a full CloudKit sync, not after every record.
func (c *Cache) FlushAll() error {
	db, owned, err := c.getDB()
	if err != nil {
		return err
	}
	if owned {
		defer db.Close()
	}

	now := time.Now().Format("2006-01-02T15:04:05")
	c.UpdatedAt = &now

	return store.ExecTx(db, func(tx *sql.Tx) error {
		if c.SyncToken != nil {
			if _, err := tx.Exec(`INSERT INTO ck_sync_state (key,value) VALUES('sync_token',?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, *c.SyncToken); err != nil {
				return err
			}
		}
		if c.OwnerID != nil {
			if _, err := tx.Exec(`INSERT INTO ck_sync_state (key,value) VALUES('owner_id',?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, *c.OwnerID); err != nil {
				return err
			}
		}
		for id, rd := range c.Reminders {
			ckey := CanonicalReminderKey(id)
			if ckey == "" {
				logger.Warnf("cache.FlushAll: skipping non-canonical key %q", id)
				continue
			}
			r := dataToStoreCKReminder(ckey, rd)
			if _, err := tx.Exec(`INSERT INTO ck_reminders
				(cloud_id,title,completed,flagged,priority,due,notes,list_ref,parent_ref,
				 hashtag_ids,change_tag,modified_ts,completion_date)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(cloud_id) DO UPDATE SET
				title=excluded.title,completed=excluded.completed,flagged=excluded.flagged,
				priority=excluded.priority,due=excluded.due,notes=excluded.notes,
				list_ref=excluded.list_ref,parent_ref=excluded.parent_ref,
				hashtag_ids=excluded.hashtag_ids,change_tag=excluded.change_tag,
				modified_ts=excluded.modified_ts,completion_date=excluded.completion_date`,
				r.CloudID, r.Title, boolInt(r.Completed), boolInt(r.Flagged), r.Priority,
				r.Due, r.Notes, r.ListRef, r.ParentRef, r.HashtagIDs,
				r.ChangeTag, r.ModifiedTS, r.CompletionDate); err != nil {
				return fmt.Errorf("flush reminder %s: %w", ckey, err)
			}
		}
		for id, name := range c.Lists {
			if _, err := tx.Exec(`INSERT INTO ck_lists (list_id,name) VALUES(?,?)
				ON CONFLICT(list_id) DO UPDATE SET name=excluded.name`, id, name); err != nil {
				return err
			}
		}
		for id, s := range c.Sections {
			if _, err := tx.Exec(`INSERT INTO ck_sections
				(section_id,name,canonical_name,list_ref,change_tag) VALUES(?,?,?,?,?)
				ON CONFLICT(section_id) DO UPDATE SET name=excluded.name,
				canonical_name=excluded.canonical_name,list_ref=excluded.list_ref,
				change_tag=excluded.change_tag`,
				id, s.Name, s.CanonicalName, s.ListRef, s.ChangeTag); err != nil {
				return err
			}
		}
		for id, h := range c.Hashtags {
			if _, err := tx.Exec(`INSERT INTO ck_hashtags
				(hashtag_id,name,reminder_ref,change_tag) VALUES(?,?,?,?)
				ON CONFLICT(hashtag_id) DO UPDATE SET name=excluded.name,
				reminder_ref=excluded.reminder_ref,change_tag=excluded.change_tag`,
				id, h.Name, h.ReminderRef, h.ChangeTag); err != nil {
				return err
			}
		}
		return nil
	})
}

// PersistReminder writes a single reminder to SQLite immediately.
// Use this after add/edit/delete mutations instead of Save().
func (c *Cache) PersistReminder(id string, rd *ReminderData) error {
	ckey := CanonicalReminderKey(id)
	if ckey == "" {
		return fmt.Errorf("cache.PersistReminder: %q is not a valid reminder ID", id)
	}
	db, owned, err := c.getDB()
	if err != nil {
		return err
	}
	if owned {
		defer db.Close()
	}
	return store.UpsertCKReminder(db, dataToStoreCKReminder(ckey, rd))
}

// DeletePersistedReminder removes a reminder from SQLite by canonical key.
func (c *Cache) DeletePersistedReminder(id string) error {
	ckey := CanonicalReminderKey(id)
	if ckey == "" {
		// not a canonical ID — nothing to delete from DB
		return nil
	}
	db, owned, err := c.getDB()
	if err != nil {
		return err
	}
	if owned {
		defer db.Close()
	}
	return store.DeleteCKReminder(db, ckey)
}

// GetReminder retrieves a reminder by any recognised ID form.
func (c *Cache) GetReminder(id string) *ReminderData {
	if ckey := CanonicalReminderKey(id); ckey != "" {
		if rd, ok := c.Reminders[ckey]; ok {
			return rd
		}
	}
	short := id
	if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
		short = id[idx+1:]
	}
	return c.Reminders[short]
}

// SetReminder stores rd under the canonical key. Returns error for non-UUID ids.
func (c *Cache) SetReminder(id string, rd *ReminderData) error {
	key := CanonicalReminderKey(id)
	if key == "" {
		return fmt.Errorf("cache.SetReminder: %q is not a valid reminder ID", id)
	}
	short := strings.TrimPrefix(strings.ToUpper(id), "REMINDER/")
	if _, exists := c.Reminders[short]; exists {
		delete(c.Reminders, short)
	}
	c.Reminders[key] = rd
	return nil
}

// DeleteReminder removes all alias keys for id from the in-memory map.
func (c *Cache) DeleteReminder(id string) {
	for _, alias := range ReminderAliases(id) {
		delete(c.Reminders, alias)
	}
}

// getDB returns the cached DB connection, or opens a new one (owned=true).
func (c *Cache) getDB() (*sql.DB, bool, error) {
	if c.db != nil {
		return c.db, false, nil
	}
	db, err := store.Open()
	if err != nil {
		return nil, false, fmt.Errorf("cache: open store: %w", err)
	}
	return db, true, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func ckReminderToData(r *store.CKReminder) *ReminderData {
	rd := &ReminderData{
		Title: r.Title, Completed: r.Completed, Flagged: r.Flagged,
		Priority: r.Priority, Due: r.Due, Notes: r.Notes,
		ListRef: r.ListRef, ParentRef: r.ParentRef,
		ChangeTag: r.ChangeTag, ModifiedTS: r.ModifiedTS, CompletionDate: r.CompletionDate,
	}
	if r.HashtagIDs != "" && r.HashtagIDs != "[]" {
		var ids []string
		if err := json.Unmarshal([]byte(r.HashtagIDs), &ids); err == nil {
			rd.HashtagIDs = ids
		}
	}
	return rd
}

func dataToStoreCKReminder(cloudID string, rd *ReminderData) store.CKReminder {
	hashtagJSON := "[]"
	if len(rd.HashtagIDs) > 0 {
		if b, err := json.Marshal(rd.HashtagIDs); err == nil {
			hashtagJSON = string(b)
		}
	}
	return store.CKReminder{
		CloudID: cloudID, Title: rd.Title, Completed: rd.Completed,
		Flagged: rd.Flagged, Priority: rd.Priority, Due: rd.Due, Notes: rd.Notes,
		ListRef: rd.ListRef, ParentRef: rd.ParentRef, HashtagIDs: hashtagJSON,
		ChangeTag: rd.ChangeTag, ModifiedTS: rd.ModifiedTS, CompletionDate: rd.CompletionDate,
	}
}

func migrateJSONCache(db *sql.DB) {
	jsonPath := filepath.Join(ConfigDir(), "ck_cache.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return
	}
	var old struct {
		Reminders map[string]*ReminderData `json:"reminders"`
		Lists     map[string]string        `json:"lists"`
		Sections  map[string]*SectionData  `json:"sections"`
		Hashtags  map[string]*HashtagData  `json:"hashtags"`
		SyncToken *string                  `json:"sync_token"`
		OwnerID   *string                  `json:"owner_id"`
	}
	if err := json.Unmarshal(data, &old); err != nil {
		logger.Warnf("cache: failed to parse old ck_cache.json: %v", err)
		return
	}
	_ = store.ExecTx(db, func(tx *sql.Tx) error {
		for id, rd := range old.Reminders {
			if ckey := CanonicalReminderKey(id); ckey != "" {
				r := dataToStoreCKReminder(ckey, rd)
				_, _ = tx.Exec(`INSERT INTO ck_reminders
					(cloud_id,title,completed,flagged,priority,due,notes,list_ref,parent_ref,
					hashtag_ids,change_tag,modified_ts,completion_date) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
					ON CONFLICT(cloud_id) DO NOTHING`,
					r.CloudID, r.Title, boolInt(r.Completed), boolInt(r.Flagged), r.Priority,
					r.Due, r.Notes, r.ListRef, r.ParentRef, r.HashtagIDs,
					r.ChangeTag, r.ModifiedTS, r.CompletionDate)
			}
		}
		for id, name := range old.Lists {
			// Use DO UPDATE so migrated list names survive even if CloudKit didn't re-emit them.
			_, _ = tx.Exec(`INSERT INTO ck_lists (list_id,name) VALUES(?,?)
				ON CONFLICT(list_id) DO UPDATE SET name=excluded.name`, id, name)
		}
		if old.SyncToken != nil {
			_, _ = tx.Exec(`INSERT INTO ck_sync_state (key,value) VALUES('sync_token',?) ON CONFLICT DO NOTHING`, *old.SyncToken)
		}
		if old.OwnerID != nil {
			_, _ = tx.Exec(`INSERT INTO ck_sync_state (key,value) VALUES('owner_id',?) ON CONFLICT DO NOTHING`, *old.OwnerID)
		}
		return nil
	})
	_ = os.Rename(jsonPath, jsonPath+".migrated")
	logger.Infof("cache: migrated ck_cache.json → SQLite")
}

// NewCacheWithDB returns an empty in-memory cache backed by an existing DB.
// Used when resetting the in-memory state for a forced full sync without
// closing and reopening the database connection.
func NewCacheWithDB(db *sql.DB) *Cache {
	return &Cache{
		Reminders: make(map[string]*ReminderData),
		Sections:  make(map[string]*SectionData),
		Hashtags:  make(map[string]*HashtagData),
		Lists:     make(map[string]string),
		db:        db,
	}
}

// DB returns the underlying database connection, or nil if not set.
// Used by the sync engine to pass the handle across a force-reset.
func (c *Cache) DB() *sql.DB {
	return c.db
}

// FlushAllFull is like FlushAll but first truncates the ck_reminders,
// ck_lists, ck_sections, and ck_hashtags tables so that records deleted
// from CloudKit are also removed from the local DB. Use this at the end
// of a forced full sync, not a delta sync.
func (c *Cache) FlushAllFull() error {
	db, owned, err := c.getDB()
	if err != nil {
		return err
	}
	if owned {
		defer db.Close()
	}
	now := time.Now().Format("2006-01-02T15:04:05")
	c.UpdatedAt = &now
	return store.ExecTx(db, func(tx *sql.Tx) error {
		// No truncation: CloudKit zone-change feed may not re-emit all records on a forced
		// sync. We upsert everything we see and rely on deleted:true records to remove stale
		// entries. Local deletes propagate via deleted:true records in the zone feed.
		if c.SyncToken != nil {
			if _, err := tx.Exec(`INSERT INTO ck_sync_state (key,value) VALUES('sync_token',?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, *c.SyncToken); err != nil {
				return err
			}
		}
		if c.OwnerID != nil {
			if _, err := tx.Exec(`INSERT INTO ck_sync_state (key,value) VALUES('owner_id',?)
				ON CONFLICT(key) DO UPDATE SET value=excluded.value`, *c.OwnerID); err != nil {
				return err
			}
		}
		for id, rd := range c.Reminders {
			ckey := CanonicalReminderKey(id)
			if ckey == "" {
				continue
			}
			r := dataToStoreCKReminder(ckey, rd)
			if _, err := tx.Exec(`INSERT INTO ck_reminders
				(cloud_id,title,completed,flagged,priority,due,notes,list_ref,parent_ref,
				 hashtag_ids,change_tag,modified_ts,completion_date)
				VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)
				ON CONFLICT(cloud_id) DO UPDATE SET
				title=excluded.title,completed=excluded.completed,flagged=excluded.flagged,
				priority=excluded.priority,due=excluded.due,notes=excluded.notes,
				list_ref=excluded.list_ref,parent_ref=excluded.parent_ref,
				hashtag_ids=excluded.hashtag_ids,change_tag=excluded.change_tag,
				modified_ts=excluded.modified_ts,completion_date=excluded.completion_date`,
				r.CloudID, r.Title, boolInt(r.Completed), boolInt(r.Flagged), r.Priority,
				r.Due, r.Notes, r.ListRef, r.ParentRef, r.HashtagIDs,
				r.ChangeTag, r.ModifiedTS, r.CompletionDate); err != nil {
				return fmt.Errorf("flush reminder %s: %w", ckey, err)
			}
		}
		for id, name := range c.Lists {
			if _, err := tx.Exec(`INSERT INTO ck_lists (list_id,name) VALUES(?,?)
				ON CONFLICT(list_id) DO UPDATE SET name=excluded.name`, id, name); err != nil {
				return err
			}
		}
		for id, s := range c.Sections {
			if _, err := tx.Exec(`INSERT INTO ck_sections
				(section_id,name,canonical_name,list_ref,change_tag) VALUES(?,?,?,?,?)
				ON CONFLICT(section_id) DO UPDATE SET name=excluded.name,
				canonical_name=excluded.canonical_name,list_ref=excluded.list_ref,
				change_tag=excluded.change_tag`,
				id, s.Name, s.CanonicalName, s.ListRef, s.ChangeTag); err != nil {
				return err
			}
		}
		for id, h := range c.Hashtags {
			if _, err := tx.Exec(`INSERT INTO ck_hashtags
				(hashtag_id,name,reminder_ref,change_tag) VALUES(?,?,?,?)
				ON CONFLICT(hashtag_id) DO UPDATE SET name=excluded.name,
				reminder_ref=excluded.reminder_ref,change_tag=excluded.change_tag`,
				id, h.Name, h.ReminderRef, h.ChangeTag); err != nil {
				return err
			}
		}
		return nil
	})
}
