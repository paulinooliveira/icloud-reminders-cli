package writer

import (
	"testing"

	"icloud-reminders/internal/cache"
	iclouddsync "icloud-reminders/internal/sync"
	"icloud-reminders/internal/utils"
	"icloud-reminders/pkg/models"
)

func TestBuildReminderFieldsPreservesExistingState(t *testing.T) {
	notes := "Body\n\nhttps://example.com"
	due := "2026-03-12"
	completionDate := "2026-03-11"
	listRef := "List/abc"
	parentRef := "Reminder/parent"

	fields, err := buildReminderFields(&cache.ReminderData{
		Title:          "Original title",
		Completed:      true,
		CompletionDate: &completionDate,
		Due:            &due,
		Priority:       models.PriorityMap["medium"],
		Notes:          &notes,
		ListRef:        &listRef,
		ParentRef:      &parentRef,
	}, ReminderChanges{})
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}

	titleDoc := fields["TitleDocument"].(map[string]interface{})["value"].(string)
	if got := utils.ExtractTitle(titleDoc); got != "Original title" {
		t.Fatalf("title mismatch: got %q", got)
	}

	notesDoc := fields["NotesDocument"].(map[string]interface{})["value"].(string)
	if got := utils.ExtractTitle(notesDoc); got != notes {
		t.Fatalf("notes mismatch: got %q", got)
	}

	if got := fields["Completed"].(map[string]interface{})["value"]; got != 1 {
		t.Fatalf("completed mismatch: got %#v", got)
	}
	if got := fields["CompletionDate"].(map[string]interface{})["value"]; got == nil {
		t.Fatal("expected completion date to be preserved")
	}
	if got := fields["Priority"].(map[string]interface{})["value"]; got != models.PriorityMap["medium"] {
		t.Fatalf("priority mismatch: got %#v", got)
	}
	if got := fields["DueDate"].(map[string]interface{})["value"]; got == nil {
		t.Fatal("expected due date to be preserved")
	}
	if got := fields["List"].(map[string]interface{})["value"].(map[string]interface{})["recordName"]; got != listRef {
		t.Fatalf("list ref mismatch: got %#v", got)
	}
	if got := fields["ParentReminder"].(map[string]interface{})["value"].(map[string]interface{})["recordName"]; got != parentRef {
		t.Fatalf("parent ref mismatch: got %#v", got)
	}
}

func TestBuildReminderFieldsAppliesClearsAndReopen(t *testing.T) {
	notes := "Old notes"
	due := "2026-03-12"
	completionDate := "2026-03-11"

	clear := ""
	incomplete := false

	fields, err := buildReminderFields(&cache.ReminderData{
		Title:          "Original title",
		Completed:      true,
		CompletionDate: &completionDate,
		Due:            &due,
		Priority:       models.PriorityMap["high"],
		Notes:          &notes,
	}, ReminderChanges{
		DueDate:   &clear,
		Notes:     &clear,
		Priority:  intPtr(models.PriorityMap["none"]),
		Completed: &incomplete,
	})
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}

	if _, ok := fields["DueDate"]; ok {
		t.Fatal("expected due date to be cleared")
	}
	if _, ok := fields["NotesDocument"]; ok {
		t.Fatal("expected notes to be cleared")
	}
	if _, ok := fields["CompletionDate"]; ok {
		t.Fatal("expected completion date to be cleared")
	}
	if got := fields["Completed"].(map[string]interface{})["value"]; got != 0 {
		t.Fatalf("completed mismatch: got %#v", got)
	}
	if got := fields["Priority"].(map[string]interface{})["value"]; got != models.PriorityMap["none"] {
		t.Fatalf("priority mismatch: got %#v", got)
	}
}

func TestBuildReminderFieldsRejectsEmptyTitle(t *testing.T) {
	clear := ""
	_, err := buildReminderFields(&cache.ReminderData{Title: "Original"}, ReminderChanges{Title: &clear})
	if err == nil {
		t.Fatal("expected empty title error")
	}
}

func TestBuildCreateListOpUsesListSchema(t *testing.T) {
	op, recordName := buildCreateListOp("Sebastian")

	if recordName == "" {
		t.Fatal("expected recordName")
	}
	if got := op["operationType"]; got != "create" {
		t.Fatalf("operationType mismatch: got %#v", got)
	}

	record, ok := op["record"].(map[string]interface{})
	if !ok {
		t.Fatal("expected record payload")
	}
	if got := record["recordType"]; got != "List" {
		t.Fatalf("recordType mismatch: got %#v", got)
	}
	if got := record["recordName"]; got != recordName {
		t.Fatalf("recordName mismatch: got %#v", got)
	}
	fields, ok := record["fields"].(map[string]interface{})
	if !ok {
		t.Fatal("expected fields payload")
	}
	if got := fields["Name"].(map[string]interface{})["value"]; got != "Sebastian" {
		t.Fatalf("name mismatch: got %#v", got)
	}
}

func TestFindListByNameAcceptsShortID(t *testing.T) {
	engine := &iclouddsync.Engine{
		Cache: &cache.Cache{
			Lists: map[string]string{
				"List/4400A74B-9D82-4F9D-8CB8-392C72BF856A": "Sebastia",
			},
		},
	}

	if got := engine.FindListByName("4400A74B-9D82-4F9D-8CB8-392C72BF856A"); got != "List/4400A74B-9D82-4F9D-8CB8-392C72BF856A" {
		t.Fatalf("full id mismatch: got %q", got)
	}
	if got := engine.FindListByName("Sebastia"); got != "List/4400A74B-9D82-4F9D-8CB8-392C72BF856A" {
		t.Fatalf("name lookup mismatch: got %q", got)
	}
}

func intPtr(v int) *int {
	return &v
}
