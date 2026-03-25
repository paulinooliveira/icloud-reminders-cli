package cmd

import (
	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/queue"
	"icloud-reminders/internal/sync"
	"icloud-reminders/internal/writer"
	"testing"
)

func TestUpsertQueueChildRecreatesOnTitleChange(t *testing.T) {
	due := "2026-03-25T10:00:00Z"
	state := &queue.State{
		Items: map[string]queue.StateItem{
			"parent": {
				Title:   "Parent",
				CloudID: "11111111-1111-1111-1111-111111111111",
				Children: map[string]queue.ChildStateItem{
					"child": {
						Key:      "child",
						Title:    "Old title",
						CloudID:  "22222222-2222-2222-2222-222222222222",
						Due:      &due,
						Priority: 9,
						Flagged:  true,
					},
				},
			},
		},
	}
	oldAdd := queueChildAddReminder
	oldDelete := queueChildDeleteReminder
	oldEdit := queueChildEditReminder
	oldSync := syncEngine
	syncEngine = &sync.Engine{Cache: cache.NewCache()}
	syncEngine.Cache.Reminders["11111111-1111-1111-1111-111111111111"] = &cache.ReminderData{
		ListRef: pointer("List/SEB"),
	}

	addCalls := 0
	deleteCalls := 0
	var editedID string
	var editedTitle string

	queueChildAddReminder = func(_, _, _, _, _, _ string) (map[string]interface{}, error) {
		addCalls++
		return map[string]interface{}{}, nil
	}
	queueChildDeleteReminder = func(string) (map[string]interface{}, error) {
		deleteCalls++
		return map[string]interface{}{}, nil
	}
	queueChildEditReminder = func(reminderID string, changes writer.ReminderChanges) (map[string]interface{}, error) {
		if changes.Title != nil {
			editedID = reminderID
			editedTitle = *changes.Title
		}
		return map[string]interface{}{}, nil
	}
	t.Cleanup(func() {
		queueChildAddReminder = oldAdd
		queueChildDeleteReminder = oldDelete
		queueChildEditReminder = oldEdit
		syncEngine = oldSync
	})

	if err := upsertQueueChild(state, "parent", "child", "New title", nil, nil, nil); err != nil {
		t.Fatalf("upsertQueueChild: %v", err)
	}

	// In-place edit: no add or delete calls, only an edit preserving the original cloud ID.
	if addCalls != 0 {
		t.Fatalf("expected no add calls on title change, got %d", addCalls)
	}
	if deleteCalls != 0 {
		t.Fatalf("expected no delete calls on title change, got %d", deleteCalls)
	}
	if editedID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("expected in-place edit on original cloud id, got %q", editedID)
	}
	if editedTitle != "New title" {
		t.Fatalf("expected edited title %q, got %q", "New title", editedTitle)
	}

	parent := state.Items["parent"]
	child := parent.Children["child"]
	// Cloud ID must be preserved — no new record was created.
	if child.CloudID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("expected original cloud id preserved, got %q", child.CloudID)
	}
	if child.Title != "New title" {
		t.Fatalf("expected updated child title, got %q", child.Title)
	}
}

func TestUpsertQueueChildUpdatesDueWithoutRecreate(t *testing.T) {
	state := &queue.State{
		Items: map[string]queue.StateItem{
			"parent": {
				Title:   "Parent",
				CloudID: "11111111-1111-1111-1111-111111111111",
				Children: map[string]queue.ChildStateItem{
					"child": {
						Key:     "child",
						Title:   "Child",
						CloudID: "22222222-2222-2222-2222-222222222222",
					},
				},
			},
		},
	}
	oldAdd := queueChildAddReminder
	oldDelete := queueChildDeleteReminder
	oldEdit := queueChildEditReminder
	oldSync := syncEngine
	syncEngine = &sync.Engine{Cache: cache.NewCache()}
	syncEngine.Cache.Reminders["11111111-1111-1111-1111-111111111111"] = &cache.ReminderData{
		ListRef: pointer("List/SEB"),
	}

	addCalls := 0
	deleteCalls := 0
	editCalls := 0

	queueChildAddReminder = func(_, _, _, _, _, _ string) (map[string]interface{}, error) {
		addCalls++
		return map[string]interface{}{}, nil
	}
	queueChildDeleteReminder = func(string) (map[string]interface{}, error) {
		deleteCalls++
		return map[string]interface{}{}, nil
	}
	var due string
	queueChildEditReminder = func(_ string, changes writer.ReminderChanges) (map[string]interface{}, error) {
		editCalls++
		if dueVal := changes.DueDate; dueVal != nil {
			due = *dueVal
		} else {
			t.Fatalf("expected due change, got nil")
		}
		return map[string]interface{}{}, nil
	}
	t.Cleanup(func() {
		queueChildAddReminder = oldAdd
		queueChildDeleteReminder = oldDelete
		queueChildEditReminder = oldEdit
		syncEngine = oldSync
	})

	newDue := "2026-03-25T12:00:00Z"
	if err := upsertQueueChild(state, "parent", "child", "Child", &newDue, nil, nil); err != nil {
		t.Fatalf("upsertQueueChild: %v", err)
	}
	if addCalls != 0 {
		t.Fatalf("expected no add on due-only update, got %d", addCalls)
	}
	if deleteCalls != 0 {
		t.Fatalf("expected no delete on due-only update, got %d", deleteCalls)
	}
	if editCalls != 1 {
		t.Fatalf("expected one edit call, got %d", editCalls)
	}
	if due != newDue {
		t.Fatalf("expected due %q, got %q", newDue, due)
	}
}

func TestUpsertQueueChildRequiresChildReminderIdFromAdd(t *testing.T) {
	state := &queue.State{
		Items: map[string]queue.StateItem{
			"parent": {
				Title:    "Parent",
				CloudID:  "11111111-1111-1111-1111-111111111111",
				Children: map[string]queue.ChildStateItem{},
			},
		},
	}
	oldAdd := queueChildAddReminder
	oldDelete := queueChildDeleteReminder
	oldEdit := queueChildEditReminder
	oldSync := syncEngine
	syncEngine = &sync.Engine{Cache: cache.NewCache()}
	syncEngine.Cache.Reminders["11111111-1111-1111-1111-111111111111"] = &cache.ReminderData{
		ListRef: pointer("List/SEB"),
	}

	queueChildAddReminder = func(_, _, _, _, _, _ string) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}
	queueChildDeleteReminder = func(string) (map[string]interface{}, error) { return map[string]interface{}{}, nil }
	queueChildEditReminder = func(string, writer.ReminderChanges) (map[string]interface{}, error) {
		return map[string]interface{}{}, nil
	}
	t.Cleanup(func() {
		queueChildAddReminder = oldAdd
		queueChildDeleteReminder = oldDelete
		queueChildEditReminder = oldEdit
		syncEngine = oldSync
	})

	if err := upsertQueueChild(state, "parent", "child", "Child", nil, nil, nil); err == nil {
		t.Fatalf("expected upsert to fail without add result id")
	}
}

func pointer[T any](value T) *T {
	return &value
}
