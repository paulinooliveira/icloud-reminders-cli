package sync

import (
	"testing"

	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/utils"
)

func TestProcessRecordsDeletesReminderAliases(t *testing.T) {
	engine := &Engine{Cache: cache.NewCache()}
	engine.Cache.Reminders["ABC-123"] = &cache.ReminderData{Title: "short"}
	engine.Cache.Reminders["Reminder/ABC-123"] = &cache.ReminderData{Title: "prefixed"}

	engine.processRecords([]interface{}{
		map[string]interface{}{
			"recordType": "Reminder",
			"recordName": "ABC-123",
			"deleted":    true,
		},
	})

	if got := engine.Cache.Reminders["ABC-123"]; got != nil {
		t.Fatalf("short reminder alias survived delete: %#v", got)
	}
	if got := engine.Cache.Reminders["Reminder/ABC-123"]; got != nil {
		t.Fatalf("prefixed reminder alias survived delete: %#v", got)
	}
}

func TestProcessRecordsMergesPartialReminderUpdates(t *testing.T) {
	engine := &Engine{Cache: cache.NewCache()}
	encodedNotes, err := utils.EncodeTitle("updated notes")
	if err != nil {
		t.Fatalf("encode notes: %v", err)
	}
	engine.Cache.Reminders["ABC-123"] = &cache.ReminderData{
		Title:      "before",
		HashtagIDs: []string{"tag-1", "tag-2"},
		Priority:   5,
	}

	engine.processRecords([]interface{}{
		map[string]interface{}{
			"recordType": "Reminder",
			"recordName": "ABC-123",
			"fields": map[string]interface{}{
				"NotesDocument": map[string]interface{}{
					"value": encodedNotes,
				},
			},
		},
	})

	got := engine.Cache.Reminders["ABC-123"]
	if got == nil {
		t.Fatal("expected reminder in cache")
	}
	if got.Title != "before" {
		t.Fatalf("title changed unexpectedly: %#v", got.Title)
	}
	if len(got.HashtagIDs) != 2 || got.HashtagIDs[0] != "tag-1" || got.HashtagIDs[1] != "tag-2" {
		t.Fatalf("hashtag ids changed unexpectedly: %#v", got.HashtagIDs)
	}
	if got.Priority != 5 {
		t.Fatalf("priority changed unexpectedly: %#v", got.Priority)
	}
	if got.Notes == nil || *got.Notes != "updated notes" {
		t.Fatalf("notes were not updated: %#v", got.Notes)
	}
}
