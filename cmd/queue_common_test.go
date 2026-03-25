package cmd

import (
	"testing"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/queue"
)

type fakeQueueValidator struct {
	gets    []*applebridge.Reminder
	getErr  error
	updates []fakeQueueUpdate
}

type fakeQueueUpdate struct {
	appleID string
	title   *string
	body    *string
}

func (f *fakeQueueValidator) GetReminder(appleID string) (*applebridge.Reminder, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	if len(f.gets) == 0 {
		return nil, nil
	}
	item := f.gets[0]
	f.gets = f.gets[1:]
	return item, nil
}

func (f *fakeQueueValidator) UpdateReminder(appleID string, title, body *string, completed *bool) error {
	f.updates = append(f.updates, fakeQueueUpdate{appleID: appleID, title: title, body: body})
	return nil
}

func TestRepairQueueReminderValidatorTextNoopsWhenAlreadyMatched(t *testing.T) {
	bridge := &fakeQueueValidator{
		gets: []*applebridge.Reminder{
			{AppleID: "x-apple-reminder://ABC", Title: "Morning", Body: "clean body"},
		},
	}
	notes := "clean body"
	if err := repairQueueReminderValidatorText(bridge, "x-apple-reminder://ABC", "Morning", &notes); err != nil {
		t.Fatalf("repairQueueReminderValidatorText: %v", err)
	}
	if len(bridge.updates) != 0 {
		t.Fatalf("expected no validator update, got %#v", bridge.updates)
	}
}

func TestRepairQueueReminderValidatorTextRepairsMismatch(t *testing.T) {
	bridge := &fakeQueueValidator{
		gets: []*applebridge.Reminder{
			{AppleID: "x-apple-reminder://ABC", Title: "MorningMorning", Body: "bad bodybad body"},
			{AppleID: "x-apple-reminder://ABC", Title: "Morning", Body: "clean body"},
		},
	}
	notes := "clean body"
	if err := repairQueueReminderValidatorText(bridge, "x-apple-reminder://ABC", "Morning", &notes); err != nil {
		t.Fatalf("repairQueueReminderValidatorText: %v", err)
	}
	if len(bridge.updates) != 1 {
		t.Fatalf("expected one validator update, got %#v", bridge.updates)
	}
	if bridge.updates[0].title == nil || *bridge.updates[0].title != "Morning" {
		t.Fatalf("unexpected title repair payload: %#v", bridge.updates[0].title)
	}
	if bridge.updates[0].body == nil || *bridge.updates[0].body != "clean body" {
		t.Fatalf("unexpected body repair payload: %#v", bridge.updates[0].body)
	}
}

func TestShouldQueryQueueValidatorList(t *testing.T) {
	if !shouldQueryQueueValidatorList(queue.StateItem{}) {
		t.Fatalf("expected empty state item to require validator list query")
	}
	if shouldQueryQueueValidatorList(queue.StateItem{AppleID: "x-apple-reminder://ABC"}) {
		t.Fatalf("expected known apple id to skip validator list query")
	}
	if shouldQueryQueueValidatorList(queue.StateItem{CloudID: "Reminder/ABC"}) {
		t.Fatalf("expected known cloud id to skip validator list query")
	}
}

func TestCanProceedWithoutQueueSync(t *testing.T) {
	if canProceedWithoutQueueSync(queue.StateItem{}) {
		t.Fatalf("expected empty state item to require sync")
	}
	if !canProceedWithoutQueueSync(queue.StateItem{CloudID: "Reminder/ABC"}) {
		t.Fatalf("expected known cloud id to skip sync gate")
	}
	if !canProceedWithoutQueueSync(queue.StateItem{CloudID: "ABCDEF12-3456-7890-ABCD-EF1234567890"}) {
		t.Fatalf("expected known reminder uuid to skip sync gate")
	}
	if !canProceedWithoutQueueSync(queue.StateItem{AppleID: "x-apple-reminder://ABC"}) {
		t.Fatalf("expected known apple id to skip sync gate")
	}
}
