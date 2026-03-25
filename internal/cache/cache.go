// Package cache manages the local JSON cache for iCloud Reminders.
package cache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"icloud-reminders/internal/logger"
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

// CacheFile returns the path to the reminders JSON cache file.
func CacheFile() string {
	return filepath.Join(ConfigDir(), "ck_cache.json")
}

// SessionFile returns the path to the auth session JSON file.
func SessionFile() string {
	return filepath.Join(ConfigDir(), "session.json")
}

func isTestProcess() bool {
	return strings.HasSuffix(filepath.Base(os.Args[0]), ".test")
}

// ReminderData holds raw cached data for a single reminder.
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

// HashtagData holds cached metadata for a reminder hashtag record.
type HashtagData struct {
	Name        string  `json:"name"`
	ReminderRef *string `json:"reminder_ref,omitempty"`
	ChangeTag   *string `json:"change_tag,omitempty"`
}

// Cache holds the local cache of reminders and lists.
type Cache struct {
	Reminders map[string]*ReminderData `json:"reminders"`
	Sections  map[string]*SectionData  `json:"sections"`
	Hashtags  map[string]*HashtagData  `json:"hashtags"`
	Lists     map[string]string        `json:"lists"`
	SyncToken *string                  `json:"sync_token,omitempty"`
	OwnerID   *string                  `json:"owner_id,omitempty"`
	UpdatedAt *string                  `json:"updated_at,omitempty"`
}

// NewCache returns an empty Cache.
func NewCache() *Cache {
	return &Cache{
		Reminders: make(map[string]*ReminderData),
		Sections:  make(map[string]*SectionData),
		Hashtags:  make(map[string]*HashtagData),
		Lists:     make(map[string]string),
	}
}

// Load loads the cache from disk; returns empty cache on error.
func Load() *Cache {
	cacheFile := CacheFile()
	data, err := os.ReadFile(cacheFile)
	if err != nil {
		// Only a missing file is expected; other read errors should be visible.
		if !os.IsNotExist(err) {
			logger.Warnf("cache read failed (%s): %v", cacheFile, err)
		}
		return NewCache()
	}
	var c Cache
	if err := json.Unmarshal(data, &c); err != nil {
		// Quarantine corrupted cache so the failure can't silently masquerade as
		// an empty state.
		ts := time.Now().Format("20060102T150405")
		quarantine := cacheFile + ".corrupt." + ts
		if renameErr := os.Rename(cacheFile, quarantine); renameErr != nil {
			logger.Warnf("cache json decode failed (%s): %v (also failed to quarantine: %v)", cacheFile, err, renameErr)
		} else {
			logger.Warnf("cache json decode failed (%s): %v (moved to %s)", cacheFile, err, quarantine)
		}
		return NewCache()
	}
	if c.Reminders == nil {
		c.Reminders = make(map[string]*ReminderData)
	}
	if c.Sections == nil {
		c.Sections = make(map[string]*SectionData)
	}
	if c.Hashtags == nil {
		c.Hashtags = make(map[string]*HashtagData)
	}
	if c.Lists == nil {
		c.Lists = make(map[string]string)
	}
	return &c
}

// Save writes the cache to disk.
func (c *Cache) Save() error {
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		return err
	}
	now := time.Now().Format("2006-01-02T15:04:05")
	c.UpdatedAt = &now
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(CacheFile(), data, 0600)
}

// ReminderAliases returns the reminder cache keys that may refer to the same
// CloudKit reminder record.
func ReminderAliases(id string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 2)
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
	add(id)
	add(short)
	add("Reminder/" + short)
	return out
}
