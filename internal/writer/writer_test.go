package writer

import (
	"bytes"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"icloud-reminders/internal/applebridge"
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

func TestBuildReminderFieldsPreservesDocumentUUIDOnTextEdit(t *testing.T) {
	titleDoc, err := utils.EncodeTitle("Original title")
	if err != nil {
		t.Fatalf("encode title: %v", err)
	}
	notesDoc, err := utils.EncodeTextDocument("Original notes", nil)
	if err != nil {
		t.Fatalf("encode notes: %v", err)
	}
	titleUUID, ok := utils.ExtractDocumentUUID(titleDoc)
	if !ok {
		t.Fatal("expected title uuid")
	}
	notesUUID, ok := utils.ExtractDocumentUUID(notesDoc)
	if !ok {
		t.Fatal("expected notes uuid")
	}

	fields, err := buildReminderFields(
		&cache.ReminderData{
			Title: "Original title",
			Notes: strPtr("Original notes"),
		},
		ReminderChanges{
			Title: strPtr("Updated title"),
			Notes: strPtr("Updated notes"),
		},
		map[string]interface{}{
			"TitleDocument": map[string]interface{}{
				"value": titleDoc,
			},
			"NotesDocument": map[string]interface{}{
				"value": notesDoc,
			},
		},
	)
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}
	rewrittenTitleDoc := fields["TitleDocument"].(map[string]interface{})["value"].(string)
	rewrittenTitleUUID, ok := utils.ExtractDocumentUUID(rewrittenTitleDoc)
	if !ok {
		t.Fatal("expected rewritten title uuid")
	}
	if !bytes.Equal(titleUUID, rewrittenTitleUUID) {
		t.Fatalf("title uuid changed: %x vs %x", titleUUID, rewrittenTitleUUID)
	}
	if got := utils.ExtractTitle(rewrittenTitleDoc); got != "Updated title" {
		t.Fatalf("updated title mismatch: got %q", got)
	}

	rewrittenNotesDoc := fields["NotesDocument"].(map[string]interface{})["value"].(string)
	rewrittenNotesUUID, ok := utils.ExtractDocumentUUID(rewrittenNotesDoc)
	if !ok {
		t.Fatal("expected rewritten notes uuid")
	}
	if !bytes.Equal(notesUUID, rewrittenNotesUUID) {
		t.Fatalf("notes uuid changed: %x vs %x", notesUUID, rewrittenNotesUUID)
	}
	if got := utils.ExtractTitle(rewrittenNotesDoc); got != "Updated notes" {
		t.Fatalf("updated notes mismatch: got %q", got)
	}
}

func TestBuildReminderFieldsAppliesClearsAndReopen(t *testing.T) {
	notes := "Old notes"
	due := "2026-03-12"
	completionDate := "2026-03-11"

	clear := ""
	incomplete := false
	flagged := true

	fields, err := buildReminderFields(&cache.ReminderData{
		Title:          "Original title",
		Completed:      true,
		Flagged:        false,
		CompletionDate: &completionDate,
		Due:            &due,
		Priority:       models.PriorityMap["high"],
		Notes:          &notes,
	}, ReminderChanges{
		DueDate:   &clear,
		Notes:     &clear,
		Priority:  intPtr(models.PriorityMap["none"]),
		Completed: &incomplete,
		Flagged:   &flagged,
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
	if got := fields["Flagged"].(map[string]interface{})["value"]; got != 1 {
		t.Fatalf("flagged mismatch: got %#v", got)
	}
	if got := fields["Priority"].(map[string]interface{})["value"]; got != models.PriorityMap["none"] {
		t.Fatalf("priority mismatch: got %#v", got)
	}
}

func TestBuildReminderFieldsPreservesExistingFlaggedState(t *testing.T) {
	fields, err := buildReminderFields(&cache.ReminderData{
		Title:   "Original title",
		Flagged: true,
	}, ReminderChanges{})
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}
	if got := fields["Flagged"].(map[string]interface{})["value"]; got != 1 {
		t.Fatalf("flagged mismatch: got %#v", got)
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

func TestBuildReminderFieldsCanReassignParent(t *testing.T) {
	newParent := "Reminder/new-parent"
	fields, err := buildReminderFields(&cache.ReminderData{
		Title:     "Original title",
		ParentRef: strPtr("Reminder/old-parent"),
	}, ReminderChanges{
		ParentRef: &newParent,
	})
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}
	if got := fields["ParentReminder"].(map[string]interface{})["value"].(map[string]interface{})["recordName"]; got != newParent {
		t.Fatalf("parent ref mismatch: got %#v", got)
	}

	clear := ""
	fields, err = buildReminderFields(&cache.ReminderData{
		Title:     "Original title",
		ParentRef: strPtr("Reminder/old-parent"),
	}, ReminderChanges{
		ParentRef: &clear,
	})
	if err != nil {
		t.Fatalf("buildReminderFields clear: %v", err)
	}
	if _, ok := fields["ParentReminder"]; ok {
		t.Fatal("expected parent reminder to be cleared")
	}
}

func TestBuildReminderFieldsBumpsReminderResolutionTokens(t *testing.T) {
	rawMap := `{"map":{"titleDocument":{"replicaID":"RID-1","counter":2,"modificationTime":10},"notesDocument":{"replicaID":"RID-2","counter":5,"modificationTime":20},"lastModifiedDate":{"replicaID":"RID-3","counter":7,"modificationTime":30}}}`
	title := "Original title"
	notes := "Old notes"
	fields, err := buildReminderFields(
		&cache.ReminderData{
			Title: title,
			Notes: &notes,
		},
		ReminderChanges{
			Title: strPtr("Updated title"),
			Notes: strPtr("Updated notes"),
		},
		map[string]interface{}{
			"ResolutionTokenMap": map[string]interface{}{
				"value": rawMap,
			},
		},
	)
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}

	if _, ok := fields["LastModifiedDate"]; !ok {
		t.Fatal("expected LastModifiedDate to be included")
	}
	rawResolution := fields["ResolutionTokenMap"].(map[string]interface{})["value"].(string)
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(rawResolution), &payload); err != nil {
		t.Fatalf("resolution token map json: %v", err)
	}
	m := payload["map"].(map[string]interface{})
	if got := int(m["titleDocument"].(map[string]interface{})["counter"].(float64)); got != 3 {
		t.Fatalf("titleDocument counter mismatch: got %d", got)
	}
	if got := int(m["notesDocument"].(map[string]interface{})["counter"].(float64)); got != 6 {
		t.Fatalf("notesDocument counter mismatch: got %d", got)
	}
	if got := int(m["lastModifiedDate"].(map[string]interface{})["counter"].(float64)); got != 8 {
		t.Fatalf("lastModifiedDate counter mismatch: got %d", got)
	}
}

func TestBuildReminderFieldsPreservesLiveTextDocumentsOnNonTextEdit(t *testing.T) {
	titleDoc, err := utils.EncodeTitle("Morning Day Briefing [daily 8:30am]")
	if err != nil {
		t.Fatalf("encode title: %v", err)
	}
	notesText := "0 / 1.5h, 0 / 160k tk. Act. 10:14p\n[ ] Refresh p-reader evidence pack"
	notesDoc, err := utils.EncodeTitle(notesText)
	if err != nil {
		t.Fatalf("encode notes: %v", err)
	}
	fields, err := buildReminderFields(
		&cache.ReminderData{
			Title:    "Morning Day Briefing [daily 8:30am]",
			Notes:    &notesText,
			Priority: models.PriorityMap["low"],
		},
		ReminderChanges{
			Priority: intPtr(models.PriorityMap["medium"]),
		},
		map[string]interface{}{
			"TitleDocument": map[string]interface{}{
				"value": titleDoc,
			},
			"NotesDocument": map[string]interface{}{
				"value": notesDoc,
			},
			"ResolutionTokenMap": map[string]interface{}{
				"value": `{"map":{"priority":{"replicaID":"RID-1","counter":2,"modificationTime":10},"lastModifiedDate":{"replicaID":"RID-2","counter":3,"modificationTime":20}}}`,
			},
		},
	)
	if err != nil {
		t.Fatalf("buildReminderFields: %v", err)
	}
	if got := fields["TitleDocument"].(map[string]interface{})["value"].(string); got != titleDoc {
		t.Fatalf("expected live title document to be preserved")
	}
	if got := fields["NotesDocument"].(map[string]interface{})["value"].(string); got != notesDoc {
		t.Fatalf("expected live notes document to be preserved")
	}
	if got := fields["Priority"].(map[string]interface{})["value"]; got != models.PriorityMap["medium"] {
		t.Fatalf("priority mismatch: got %#v", got)
	}
}

func TestReferencedChildRecordNames(t *testing.T) {
	fields := map[string]interface{}{
		"AlarmIDs": map[string]interface{}{
			"type":  "STRING_LIST",
			"value": []interface{}{"10D6B475-F334-4912-BCA0-CEC1F3CE0B42"},
		},
		"AssignmentIDs": map[string]interface{}{
			"type":  "EMPTY_LIST",
			"value": []interface{}{},
		},
		"HashtagIDs": map[string]interface{}{
			"type":  "STRING_LIST",
			"value": []interface{}{"37F1680B-7F3D-4CD3-B501-30337A9CEDAD"},
		},
		"RecurrenceRuleIDs": map[string]interface{}{
			"type":  "STRING_LIST",
			"value": []interface{}{"ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB"},
		},
		"TriggerID": map[string]interface{}{
			"type":  "STRING",
			"value": "E974B8A8-ED00-41EE-BB2F-9DE92764C3A3",
		},
	}

	got := referencedChildRecordNames(fields)
	want := map[string]struct{}{
		"Alarm/10D6B475-F334-4912-BCA0-CEC1F3CE0B42":          {},
		"AlarmTrigger/E974B8A8-ED00-41EE-BB2F-9DE92764C3A3":   {},
		"Hashtag/37F1680B-7F3D-4CD3-B501-30337A9CEDAD":        {},
		"RecurrenceRule/ABCDEFAB-CDEF-ABCD-EFAB-CDEFABCDEFAB": {},
	}

	if len(got) != len(want) {
		t.Fatalf("unexpected child record count: got %d want %d (%v)", len(got), len(want), got)
	}
	for _, recordName := range got {
		if _, ok := want[recordName]; !ok {
			t.Fatalf("unexpected child record name %q", recordName)
		}
		delete(want, recordName)
	}
	if len(want) != 0 {
		t.Fatalf("missing child record names: %v", want)
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

func TestBuildCreateOpTimedDueAddsAlarmArtifacts(t *testing.T) {
	ops, recordName, err := buildCreateOp("_owner", "Timed reminder", "List/abc", "", "2026-03-24T16:30", models.PriorityMap["medium"], "notes")
	if err != nil {
		t.Fatalf("buildCreateOp: %v", err)
	}
	if recordName == "" {
		t.Fatal("expected recordName")
	}
	if len(ops) != 3 {
		t.Fatalf("expected 3 ops for timed due, got %d", len(ops))
	}

	reminder := ops[0]["record"].(map[string]interface{})
	fields := reminder["fields"].(map[string]interface{})
	if got := fields["TimeZone"].(map[string]interface{})["value"]; got == "" {
		t.Fatalf("expected timezone, got %#v", got)
	}
	alarmIDs := fields["AlarmIDs"].(map[string]interface{})
	if got := alarmIDs["type"]; got != "STRING_LIST" {
		t.Fatalf("expected STRING_LIST alarm ids, got %#v", got)
	}

	alarm := ops[1]["record"].(map[string]interface{})
	alarmFields := alarm["fields"].(map[string]interface{})
	if got := alarmFields["Reminder"].(map[string]interface{})["value"].(map[string]interface{})["recordName"]; got != recordName {
		t.Fatalf("alarm reminder ref mismatch: got %#v", got)
	}

	trigger := ops[2]["record"].(map[string]interface{})
	triggerFields := trigger["fields"].(map[string]interface{})
	if got := triggerFields["Type"].(map[string]interface{})["value"]; got != "Date" {
		t.Fatalf("trigger type mismatch: got %#v", got)
	}
}

func TestBuildReminderEditPlanPreservesTimedDueArtifacts(t *testing.T) {
	due := "2026-03-24T16:30"
	changeTag := "ct-1"
	liveFields := map[string]interface{}{
		"AlarmIDs": map[string]interface{}{
			"type":  "STRING_LIST",
			"value": []interface{}{"ALARM-1"},
		},
		"TimeZone": map[string]interface{}{
			"value": "America/Sao_Paulo",
		},
	}

	ops, cleanup, err := buildReminderEditPlan("_owner", "Reminder/abc", &cache.ReminderData{
		Title:     "Timed reminder",
		Due:       &due,
		ChangeTag: &changeTag,
	}, ReminderChanges{
		Notes: strPtr("updated"),
	}, liveFields)
	if err != nil {
		t.Fatalf("buildReminderEditPlan: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected one replace op, got %d", len(ops))
	}
	if len(cleanup) != 0 {
		t.Fatalf("expected no cleanup, got %v", cleanup)
	}

	fields := ops[0]["record"].(map[string]interface{})["fields"].(map[string]interface{})
	if got := fields["TimeZone"].(map[string]interface{})["value"]; got != "America/Sao_Paulo" {
		t.Fatalf("timezone mismatch: got %#v", got)
	}
	alarmIDs := fields["AlarmIDs"].(map[string]interface{})["value"].([]interface{})
	if len(alarmIDs) != 1 || alarmIDs[0] != "ALARM-1" {
		t.Fatalf("alarm ids mismatch: got %#v", alarmIDs)
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

func TestSafeTextEditReminderUsesAppleBridgeAndUpdatesCache(t *testing.T) {
	t.Setenv("ICLOUD_REMINDERS_CONFIG_DIR", t.TempDir())

	tmpDir := t.TempDir()
	sshPath := filepath.Join(tmpDir, "ssh")
	callFile := filepath.Join(tmpDir, "calls")
	script := "#!/bin/sh\n" +
		"count=0\n" +
		"if [ -f " + writerShellQuote(callFile) + " ]; then count=$(cat " + writerShellQuote(callFile) + "); fi\n" +
		"count=$((count+1))\n" +
		"printf '%s' \"$count\" > " + writerShellQuote(callFile) + "\n" +
		"if [ \"$count\" -eq 1 ]; then printf '%s' '{\"ok\":true}'; else printf '%s' '{\"id\":\"x-apple-reminder://ABC\",\"title\":\"Updated title\",\"completed\":false,\"body\":\"Updated body\"}'; fi\n"
	if err := os.WriteFile(sshPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer os.Setenv("PATH", oldPath)

	engine := &iclouddsync.Engine{
		Cache: &cache.Cache{
			Reminders: map[string]*cache.ReminderData{
				"Reminder/ABC": {
					Title: "Original title",
					Notes: strPtr("Original body"),
				},
			},
			Sections: map[string]*cache.SectionData{},
			Hashtags: map[string]*cache.HashtagData{},
			Lists:    map[string]string{},
		},
	}
	w := &Writer{Sync: engine}
	bridge := applebridge.New(&applebridge.Config{Host: "example-host", User: "tester", IdentityPath: "/tmp/key"})

	result, err := w.SafeTextEditReminder("ABC", strPtr("Updated title"), strPtr("Updated body"), bridge)
	if err != nil {
		t.Fatalf("SafeTextEditReminder error: %v", err)
	}
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("unexpected result error: %s", errMsg)
	}
	rd := w.Sync.Cache.Reminders["Reminder/ABC"]
	if rd == nil {
		t.Fatal("expected updated cache entry")
	}
	if rd.Title != "Updated title" {
		t.Fatalf("title mismatch: got %q", rd.Title)
	}
	if rd.Notes == nil || *rd.Notes != "Updated body" {
		t.Fatalf("notes mismatch: %#v", rd.Notes)
	}
	if alias := w.Sync.Cache.Reminders["ABC"]; alias == nil || alias.Title != "Updated title" {
		t.Fatalf("expected alias cache update, got %#v", alias)
	}
}

func TestRepairVisibleTextStateUsesValidatorStoreRepair(t *testing.T) {
	t.Setenv("ICLOUD_REMINDERS_CONFIG_DIR", t.TempDir())

	tmpDir := t.TempDir()
	sshPath := filepath.Join(tmpDir, "ssh")
	callFile := filepath.Join(tmpDir, "calls")
	script := "#!/bin/sh\n" +
		"count=0\n" +
		"if [ -f " + writerShellQuote(callFile) + " ]; then count=$(cat " + writerShellQuote(callFile) + "); fi\n" +
		"count=$((count+1))\n" +
		"printf '%s' \"$count\" > " + writerShellQuote(callFile) + "\n" +
		"if [ \"$count\" -eq 1 ]; then printf '%s' '1'; else printf '%s' '{\"id\":\"x-apple-reminder://ABC\",\"title\":\"Updated title\",\"completed\":false,\"body\":\"Updated body\"}'; fi\n"
	if err := os.WriteFile(sshPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}
	oldPath := os.Getenv("PATH")
	if err := os.Setenv("PATH", tmpDir+string(os.PathListSeparator)+oldPath); err != nil {
		t.Fatalf("set PATH: %v", err)
	}
	defer os.Setenv("PATH", oldPath)

	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_TYPE", "apple-ssh")
	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_HOST", "example-host")
	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_USER", "tester")
	t.Setenv("ICLOUD_REMINDERS_VALIDATOR_IDENTITY_PATH", "/tmp/key")

	notes := "Updated body"
	engine := &iclouddsync.Engine{
		Cache: &cache.Cache{
			Reminders: map[string]*cache.ReminderData{
				"Reminder/ABC": {
					Title: "Updated title",
					Notes: &notes,
				},
			},
			Sections: map[string]*cache.SectionData{},
			Hashtags: map[string]*cache.HashtagData{},
			Lists:    map[string]string{},
		},
	}
	w := &Writer{Sync: engine}
	if err := w.repairVisibleTextState("Reminder/ABC", engine.Cache.Reminders["Reminder/ABC"], ReminderChanges{
		Title: strPtr("Updated title"),
		Notes: strPtr("Updated body"),
	}); err != nil {
		t.Fatalf("repairVisibleTextState error: %v", err)
	}
}

func intPtr(v int) *int {
	return &v
}

func strPtr(v string) *string {
	return &v
}

func writerShellQuote(v string) string {
	return "'" + filepath.ToSlash(v) + "'"
}
