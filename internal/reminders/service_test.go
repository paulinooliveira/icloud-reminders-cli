package reminders

import (
	"fmt"
	"strings"
	"testing"

	"icloud-reminders/pkg/models"
)

func TestPaginateDeclaresTruncation(t *testing.T) {
	items := make([]*models.Reminder, 100)
	for i := range items {
		items[i] = &models.Reminder{ID: fmt.Sprintf("Reminder/%03d", i)}
	}
	page := paginate(items, 10, 0)
	if len(page.Items) != 10 || page.TotalCount != 100 || !page.HasMore || page.Limit != 10 || page.Offset != 0 {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestPaginateCapsLimitAndOffset(t *testing.T) {
	items := make([]*models.Reminder, MaxLimit+20)
	page := paginate(items, MaxLimit+999, 10)
	if page.Limit != MaxLimit || len(page.Items) != MaxLimit || !page.HasMore {
		t.Fatalf("unexpected capped page: %+v", page)
	}
	page = paginate(items, 1, -10)
	if page.Offset != 0 {
		t.Fatalf("negative offset was not normalized: %+v", page)
	}
}

func TestUniqueReminderRejectsAmbiguousPrefix(t *testing.T) {
	items := []*models.Reminder{{ID: "abc-one"}, {ID: "abc-two"}}
	_, err := uniqueReminder(items, "Work", "abc")
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous-prefix error, got %v", err)
	}
}

func TestUniqueReminderPrefersExactID(t *testing.T) {
	items := []*models.Reminder{{ID: "abc"}, {ID: "abc-two"}}
	item, err := uniqueReminder(items, "Work", "abc")
	if err != nil || item.ID != "abc" {
		t.Fatalf("expected exact match, got item=%+v err=%v", item, err)
	}
}

func TestDefaultServiceUsesInProcessEventKit(t *testing.T) {
	service, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if _, ok := any(service).(*EventKitService); !ok {
		t.Fatalf("default service is %T, want *EventKitService", service)
	}
}
