package writer

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
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
	tagIDs := []string{"CEED02C9-FCBF-4A14-9F51-CA3409B265CE"}

	fields, err := buildReminderFields(&cache.ReminderData{
		Title:          "Original title",
		Completed:      true,
		CompletionDate: &completionDate,
		Due:            &due,
		Priority:       models.PriorityMap["medium"],
		Notes:          &notes,
		HashtagIDs:     tagIDs,
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
	hashtagField := fields["HashtagIDs"].(map[string]interface{})
	if got := hashtagField["type"]; got != "STRING_LIST" {
		t.Fatalf("hashtag type mismatch: got %#v", got)
	}
	values := hashtagField["value"].([]interface{})
	if len(values) != 1 || values[0] != tagIDs[0] {
		t.Fatalf("hashtag ids mismatch: got %#v", values)
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

func TestBuildReminderFieldsCanClearTags(t *testing.T) {
	fields, err := buildReminderFields(&cache.ReminderData{
		Title:      "Original title",
		HashtagIDs: []string{"ABC", "DEF"},
	}, ReminderChanges{
		HashtagIDs: &[]string{},
	})
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}
	hashtagField := fields["HashtagIDs"].(map[string]interface{})
	if got := hashtagField["type"]; got != "EMPTY_LIST" {
		t.Fatalf("hashtag clear type mismatch: got %#v", got)
	}
	if got := len(hashtagField["value"].([]interface{})); got != 0 {
		t.Fatalf("expected empty hashtag list, got %d", got)
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

func TestFindReminderByTitleFiltersByListAndTopLevel(t *testing.T) {
	engine := &iclouddsync.Engine{
		Cache: &cache.Cache{
			Reminders: map[string]*cache.ReminderData{
				"Reminder/top-a": {
					Title:   "explorer",
					ListRef: strPtr("List/a"),
				},
				"Reminder/child-a": {
					Title:     "explorer",
					ListRef:   strPtr("List/a"),
					ParentRef: strPtr("Reminder/top-a"),
				},
				"Reminder/top-b": {
					Title:   "explorer",
					ListRef: strPtr("List/b"),
				},
			},
		},
	}

	if got := engine.FindReminderByTitle("explorer", "List/a", true); got != "Reminder/top-a" {
		t.Fatalf("top-level list-filtered lookup mismatch: got %q", got)
	}
	if got := engine.FindReminderByTitle("explorer", "List/b", true); got != "Reminder/top-b" {
		t.Fatalf("list-b lookup mismatch: got %q", got)
	}
}

func TestResolveParentRefAcceptsTitle(t *testing.T) {
	engine := &iclouddsync.Engine{
		Cache: &cache.Cache{
			Reminders: map[string]*cache.ReminderData{
				"Reminder/explorer-top": {
					Title:   "explorer",
					ListRef: strPtr("List/sebastian"),
				},
			},
		},
	}

	if got := resolveParentRef(engine, "explorer", "List/sebastian"); got != "Reminder/explorer-top" {
		t.Fatalf("title parent resolution mismatch: got %q", got)
	}
}

func TestChecksumHex(t *testing.T) {
	raw := []byte("hello")
	sum := sha512.Sum512(raw)
	want := hex.EncodeToString(sum[:])
	if got := checksumHex(raw); got != want {
		t.Fatalf("checksumHex mismatch: got %s want %s", got, want)
	}
}

func TestBumpListResolutionTokenMap(t *testing.T) {
	fields := map[string]interface{}{
		"ResolutionTokenMap": map[string]interface{}{
			"value": `{"map":{"membershipsOfRemindersInSectionsChecksum":{"replicaID":"RID","counter":3,"modificationTime":10},"name":{"replicaID":"X","counter":1,"modificationTime":20}}}`,
		},
	}
	out := bumpListResolutionTokenMap(fields, "membershipsOfRemindersInSectionsChecksum")
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("json: %v", err)
	}
	m := payload["map"].(map[string]interface{})
	entry := m["membershipsOfRemindersInSectionsChecksum"].(map[string]interface{})
	if got := int(entry["counter"].(float64)); got != 4 {
		t.Fatalf("counter mismatch: got %d", got)
	}
	if got := entry["replicaID"].(string); got != "RID" {
		t.Fatalf("replicaID mismatch: got %q", got)
	}
	if got := entry["modificationTime"].(float64); got <= 10 {
		t.Fatalf("modificationTime not bumped: got %v", got)
	}
}

func TestNormalizeTagNames(t *testing.T) {
	got := normalizeTagNames([]string{" #p-manager ", "explorer", "#p-manager", "", "Reader"})
	want := []string{"p-manager", "explorer", "Reader"}
	if len(got) != len(want) {
		t.Fatalf("length mismatch: got %#v want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("normalizeTagNames mismatch: got %#v want %#v", got, want)
		}
	}
}

func intPtr(v int) *int {
	return &v
}

func strPtr(v string) *string {
	return &v
}
