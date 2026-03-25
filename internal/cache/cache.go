// Package cache provides the in-process CloudKit metadata cache backed by the
// shared SQLite store. It replaces the old ck_cache.json file and eliminates
// the dual-cache key-form confusion by enforcing canonical "Reminder/UUID"
// keys at the database CHECK-constraint level.
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
// All map mutations are written through to SQLite via Save(); Load() hydrates
// from SQLite. Direct map writes are still supported for backward compatibility
// but callers should prefer SetReminder/GetReminder for reminders.
type Cache struct {
	Reminders map[string]*ReminderData
	Sections  map[string]*SectionData
	Hashtags  map[string]*HashtagData
	Lists     map[string]string
	SyncToken *string
	OwnerID   *string
	UpdatedAt *string

	db *sql.DB // nil in tests that don't call Load/NewCache
}

// NewCache returns an empty in-memory cache (no DB).
// Used in tests and for a forced full-sync reset.
func NewCache() *Cache {
	return &Cache{
		Reminders: make(map[string]*ReminderData),
		Sections:  make(map[string]*SectionData),
		Hashtags:  make(map[string]*HashtagData),
		Lists:     make(map[string]string),
	}
}

// Load opens the SQLite store and hydrates the cache.
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

	// sync state
	if tok, err := store.GetCKSyncState(db, "sync_token"); err == nil && tok != "" {
		c.SyncToken = &tok
	}
	if ownerID, err := store.GetCKSyncState(db, "owner_id"); err == nil && ownerID != "" {
		c.OwnerID = &ownerID
	}

	// reminders
	rows, err := store.ListCKReminders(db, true)
	if err != nil {
		logger.Warnf("cache: load reminders: %v", err)
	}
	for _, r := range rows {
		c.Reminders[r.CloudID] = ckReminderToData(r)
	}

	// lists
	lists, err := store.ListCKLists(db)
	if err != nil {
		logger.Warnf("cache: load lists: %v", err)
	} else {
		c.Lists = lists
	}

	// sections
	sections, err := store.ListCKSections(db)
	if err != nil {
		logger.Warnf("cache: load sections: %v", err)
	}
	for id, s := range sections {
		c.Sections[id] = &SectionData{
			Name:          s.Name,
			CanonicalName: s.CanonicalName,
			ListRef:       s.ListRef,
			ChangeTag:     s.ChangeTag,
		}
	}

	// hashtags
	hashtags, err := store.ListCKHashtags(db)
	if err != nil {
		logger.Warnf("cache: load hashtags: %v", err)
	}
	for id, h := range hashtags {
		c.Hashtags[id] = &HashtagData{
			Name:        h.Name,
			ReminderRef: h.ReminderRef,
			ChangeTag:   h.ChangeTag,
		}
	}

	// migrate any stale ck_cache.json (skip in test processes to avoid goroutine leaks)
	if !isTestProcess() {
		go migrateJSONCache(db)
	}

	return c
}

// Save persists the in-memory cache to SQLite.
func (c *Cache) Save() error {
	ownedDB := false
	db := c.db
	if db == nil {
		var err error
		db, err = store.Open()
		if err != nil {
			return fmt.Errorf("cache.Save: open store: %w", err)
		}
		ownedDB = true
	}
	if ownedDB {
		defer db.Close()
	}

	now := time.Now().Format("2006-01-02T15:04:05")
	c.UpdatedAt = &now

	if c.SyncToken != nil {
		if err := store.SetCKSyncState(db, "sync_token", *c.SyncToken); err != nil {
			return fmt.Errorf("cache.Save sync_token: %w", err)
		}
	}
	if c.OwnerID != nil {
		if err := store.SetCKSyncState(db, "owner_id", *c.OwnerID); err != nil {
			return fmt.Errorf("cache.Save owner_id: %w", err)
		}
	}

	for id, rd := range c.Reminders {
		ckey := CanonicalReminderKey(id)
		if ckey == "" {
			logger.Warnf("cache.Save: skipping non-canonical reminder key %q", id)
			continue
		}
		if err := store.UpsertCKReminder(db, dataToStoreCKReminder(ckey, rd)); err != nil {
			return fmt.Errorf("cache.Save reminder %s: %w", ckey, err)
		}
	}

	for id, name := range c.Lists {
		if err := store.UpsertCKList(db, id, name); err != nil {
			return fmt.Errorf("cache.Save list %s: %w", id, err)
		}
	}

	for id, s := range c.Sections {
		if err := store.UpsertCKSection(db, store.CKSection{
			SectionID:     id,
			Name:          s.Name,
			CanonicalName: s.CanonicalName,
			ListRef:       s.ListRef,
			ChangeTag:     s.ChangeTag,
		}); err != nil {
			return fmt.Errorf("cache.Save section %s: %w", id, err)
		}
	}

	for id, h := range c.Hashtags {
		if err := store.UpsertCKHashtag(db, store.CKHashtag{
			HashtagID:   id,
			Name:        h.Name,
			ReminderRef: h.ReminderRef,
			ChangeTag:   h.ChangeTag,
		}); err != nil {
			return fmt.Errorf("cache.Save hashtag %s: %w", id, err)
		}
	}

	return nil
}

// GetReminder retrieves a reminder by any recognised ID form.
// Close releases the underlying database connection held by this Cache.
// Call this when the Cache is no longer needed (e.g. end of command).
func (c *Cache) Close() {
	if c.db != nil {
		_ = c.db.Close()
		c.db = nil
	}
}

func (c *Cache) GetReminder(id string) *ReminderData {
	if ckey := CanonicalReminderKey(id); ckey != "" {
		if rd, ok := c.Reminders[ckey]; ok {
			return rd
		}
	}
	// bare UUID fallback for stale entries
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
	// clean up any old bare-UUID alias
	short := strings.TrimPrefix(id, "Reminder/")
	if short != key {
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

// --- helpers ----------------------------------------------------------------

func ckReminderToData(r *store.CKReminder) *ReminderData {
	rd := &ReminderData{
		Title:          r.Title,
		Completed:      r.Completed,
		Flagged:        r.Flagged,
		Priority:       r.Priority,
		Due:            r.Due,
		Notes:          r.Notes,
		ListRef:        r.ListRef,
		ParentRef:      r.ParentRef,
		ChangeTag:      r.ChangeTag,
		ModifiedTS:     r.ModifiedTS,
		CompletionDate: r.CompletionDate,
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
		CloudID:        cloudID,
		Title:          rd.Title,
		Completed:      rd.Completed,
		Flagged:        rd.Flagged,
		Priority:       rd.Priority,
		Due:            rd.Due,
		Notes:          rd.Notes,
		ListRef:        rd.ListRef,
		ParentRef:      rd.ParentRef,
		HashtagIDs:     hashtagJSON,
		ChangeTag:      rd.ChangeTag,
		ModifiedTS:     rd.ModifiedTS,
		CompletionDate: rd.CompletionDate,
	}
}

// migrateJSONCache imports data from the old ck_cache.json if it still exists,
// then removes it. Runs in a goroutine on first Load(); harmless if already gone.
func migrateJSONCache(db *sql.DB) {
	jsonPath := filepath.Join(ConfigDir(), "ck_cache.json")
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		return // already gone or never existed
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
		logger.Warnf("cache: failed to parse old ck_cache.json for migration: %v", err)
		return
	}
	for id, rd := range old.Reminders {
		ckey := CanonicalReminderKey(id)
		if ckey == "" {
			continue
		}
		_ = store.UpsertCKReminder(db, dataToStoreCKReminder(ckey, rd))
	}
	for id, name := range old.Lists {
		_ = store.UpsertCKList(db, id, name)
	}
	for id, s := range old.Sections {
		_ = store.UpsertCKSection(db, store.CKSection{
			SectionID:     id,
			Name:          s.Name,
			CanonicalName: s.CanonicalName,
			ListRef:       s.ListRef,
			ChangeTag:     s.ChangeTag,
		})
	}
	for id, h := range old.Hashtags {
		_ = store.UpsertCKHashtag(db, store.CKHashtag{
			HashtagID:   id,
			Name:        h.Name,
			ReminderRef: h.ReminderRef,
			ChangeTag:   h.ChangeTag,
		})
	}
	if old.SyncToken != nil {
		_ = store.SetCKSyncState(db, "sync_token", *old.SyncToken)
	}
	if old.OwnerID != nil {
		_ = store.SetCKSyncState(db, "owner_id", *old.OwnerID)
	}
	// rename rather than delete so nothing is lost
	_ = os.Rename(jsonPath, jsonPath+".migrated")
	logger.Infof("cache: migrated ck_cache.json → SQLite, file renamed to .migrated")
}
