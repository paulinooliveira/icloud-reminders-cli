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
	editCalls := 0

	queueChildAddReminder = func(title, listName, dueDate, priority, notes, parentID string) (map[string]interface{}, error) {
		addCalls++
		if title != "New title" {
			t.Fatalf("expected new child title, got %q", title)
		}
		if parentID != "11111111-1111-1111-1111-111111111111" {
			t.Fatalf("expected parent cloud id parent, got %q", parentID)
		}
		if listName != "List/SEB" {
			t.Fatalf("expected parent list id, got %q", listName)
		}
		return map[string]interface{}{"id": "33333333-3333-3333-3333-333333333333"}, nil
	}
	queueChildDeleteReminder = func(reminderID string) (map[string]interface{}, error) {
		deleteCalls++
		if reminderID != "22222222-2222-2222-2222-222222222222" && reminderID != "33333333-3333-3333-3333-333333333333" {
			t.Fatalf("unexpected delete id %q", reminderID)
		}
		return map[string]interface{}{}, nil
	}
	queueChildEditReminder = func(reminderID string, _ writer.ReminderChanges) (map[string]interface{}, error) {
		editCalls++
		if reminderID != "33333333-3333-3333-3333-333333333333" {
			t.Fatalf("unexpected edited reminder %q", reminderID)
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

	parent := state.Items["parent"]
	child := parent.Children["child"]
	if child.CloudID != "33333333-3333-3333-3333-333333333333" {
		t.Fatalf("expected recreated child cloud id, got %q", child.CloudID)
	}
	if child.Title != "New title" {
		t.Fatalf("expected updated child title, got %q", child.Title)
	}
	if child.AppleID != "x-apple-reminder://33333333-3333-3333-3333-333333333333" {
		t.Fatalf("expected updated apple id, got %q", child.AppleID)
	}
	if addCalls != 1 || deleteCalls != 1 || editCalls == 0 {
		t.Fatalf("expected add=1, delete=1, edit>=1 got add=%d delete=%d edit=%d", addCalls, deleteCalls, editCalls)
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
