package cmd

import (
	"testing"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/queue"
	"icloud-reminders/pkg/models"
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

func (f *fakeQueueValidator) DeleteReminder(appleID string) error {
	return nil
}

func TestVerifyQueueReminderValidatorTextNoopsWhenAlreadyMatched(t *testing.T) {
	bridge := &fakeQueueValidator{
		gets: []*applebridge.Reminder{
			{AppleID: "x-apple-reminder://ABC", Title: "Morning", Body: "clean body"},
		},
	}
	notes := "clean body"
	if err := verifyQueueReminderValidatorText(bridge, "x-apple-reminder://ABC", "Morning", &notes); err != nil {
		t.Fatalf("verifyQueueReminderValidatorText: %v", err)
	}
	if len(bridge.updates) != 0 {
		t.Fatalf("expected no validator update, got %#v", bridge.updates)
	}
}

func TestVerifyQueueReminderValidatorTextDetectsMismatch(t *testing.T) {
	bridge := &fakeQueueValidator{
		gets: []*applebridge.Reminder{
			{AppleID: "x-apple-reminder://ABC", Title: "MorningMorning", Body: "bad bodybad body"},
		},
	}
	notes := "clean body"
	if err := verifyQueueReminderValidatorText(bridge, "x-apple-reminder://ABC", "Morning", &notes); err == nil {
		t.Fatalf("expected mismatch error, got nil")
	}
	if len(bridge.updates) != 0 {
		t.Fatalf("expected no validator update, got %#v", bridge.updates)
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

func TestChooseCanonicalCloudMatchPrefersPreferredID(t *testing.T) {
	a := &models.Reminder{ID: "Reminder/A", Title: "X"}
	b := &models.Reminder{ID: "Reminder/B", Title: "X"}
	matches := []*models.Reminder{a, b}
	keep := chooseCanonicalCloudMatch(matches, "Reminder/B")
	if keep == nil || keep.ID != "Reminder/B" {
		t.Fatalf("expected preferred cloud id to win, got %#v", keep)
	}
}

func TestChooseCanonicalCloudMatchPicksNewestModifiedTS(t *testing.T) {
	ts1 := int64(100)
	ts2 := int64(200)
	a := &models.Reminder{ID: "Reminder/A", Title: "X", ModifiedTS: &ts1}
	b := &models.Reminder{ID: "Reminder/B", Title: "X", ModifiedTS: &ts2}
	matches := []*models.Reminder{a, b}
	keep := chooseCanonicalCloudMatch(matches, "")
	if keep == nil || keep.ID != "Reminder/B" {
		t.Fatalf("expected newest modified to win, got %#v", keep)
	}
}
