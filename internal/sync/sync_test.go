package sync

import (
	"testing"

	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/utils"
)

const testUUID = "B267CC2E-E156-4CCE-9DED-E1E5576F4911"
const testCanonical = "B267CC2E-E156-4CCE-9DED-E1E5576F4911"

func TestProcessRecordsDeletesReminderAliases(t *testing.T) {
	engine := &Engine{Cache: cache.NewCache()}
	engine.Cache.Reminders[testUUID] = &cache.ReminderData{Title: "bare"}
	engine.Cache.Reminders[testCanonical] = &cache.ReminderData{Title: "canonical"}

	engine.processRecords([]interface{}{
		map[string]interface{}{
			"recordType": "Reminder",
			"recordName": testUUID,
			"deleted":    true,
		},
	})

	if got := engine.Cache.Reminders[testUUID]; got != nil {
		t.Fatalf("bare alias survived delete: %#v", got)
	}
	if got := engine.Cache.Reminders[testCanonical]; got != nil {
		t.Fatalf("canonical alias survived delete: %#v", got)
	}
}

func TestProcessRecordsMergesPartialReminderUpdates(t *testing.T) {
	engine := &Engine{Cache: cache.NewCache()}
	encodedNotes, err := utils.EncodeTitle("updated notes")
	if err != nil {
		t.Fatalf("encode notes: %v", err)
	}
	// Seed under canonical key.
	engine.Cache.Reminders[testCanonical] = &cache.ReminderData{
		Title:      "before",
		HashtagIDs: []string{"tag-1", "tag-2"},
		Priority:   5,
	}

	engine.processRecords([]interface{}{
		map[string]interface{}{
			"recordType": "Reminder",
			"recordName": testUUID,
			"fields": map[string]interface{}{
				"NotesDocument": map[string]interface{}{
					"value": encodedNotes,
				},
			},
		},
	})

	// processRecords writes under canonical key.
	got := engine.Cache.Reminders[testCanonical]
	if got == nil {
		t.Fatal("expected entry under canonical key after processRecords")
	}
	if got.Title != "before" {
		t.Fatalf("title changed unexpectedly: %q", got.Title)
	}
	if len(got.HashtagIDs) != 2 {
		t.Fatalf("hashtag ids changed unexpectedly: %v", got.HashtagIDs)
	}
	if got.Priority != 5 {
		t.Fatalf("priority changed unexpectedly: %d", got.Priority)
	}
	if got.Notes == nil || *got.Notes != "updated notes" {
		t.Fatalf("notes were not updated: %v", got.Notes)
	}
}
