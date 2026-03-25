package queue

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/pkg/models"
)

func TestChooseCanonicalPrefersCloudBackedItem(t *testing.T) {
	matches := []applebridge.Reminder{
		{AppleID: "x-apple-reminder://BD2D24AE-32B5-4305-8051-9BAF885FC8C7", Title: "Audit", Body: "dupdupdup"},
		{AppleID: "x-apple-reminder://E579B50E-6720-4839-9197-93D6AC00BB89", Title: "Audit", Body: "clean"},
	}
	cloud := map[string]*models.Reminder{
		"E579B50E-6720-4839-9197-93D6AC00BB89": {ID: "E579B50E-6720-4839-9197-93D6AC00BB89"},
	}
	chosen := ChooseCanonical(matches, cloud, StateItem{}, strPtr("clean"))
	if chosen.Keep == nil || chosen.Keep.UUID() != "E579B50E-6720-4839-9197-93D6AC00BB89" {
		t.Fatalf("unexpected keep: %#v", chosen.Keep)
	}
	if len(chosen.Delete) != 1 || chosen.Delete[0].UUID() != "BD2D24AE-32B5-4305-8051-9BAF885FC8C7" {
		t.Fatalf("unexpected deletes: %#v", chosen.Delete)
	}
}

func TestChooseCanonicalPrefersStateMappedItem(t *testing.T) {
	matches := []applebridge.Reminder{
		{AppleID: "x-apple-reminder://AAA", Title: "Audit", Body: "short"},
		{AppleID: "x-apple-reminder://BBB", Title: "Audit", Body: "short"},
	}
	chosen := ChooseCanonical(matches, map[string]*models.Reminder{}, StateItem{AppleID: "x-apple-reminder://BBB"}, nil)
	if chosen.Keep == nil || chosen.Keep.AppleID != "x-apple-reminder://BBB" {
		t.Fatalf("unexpected keep: %#v", chosen.Keep)
	}
}

func TestWaitForSingleVisibleRetriesUntilConverged(t *testing.T) {
	t.Parallel()

	calls := 0
	fetch := func() ([]applebridge.Reminder, error) {
		calls++
		switch calls {
		case 1:
			return []applebridge.Reminder{
				{AppleID: "x-apple-reminder://A", Title: "Audit"},
				{AppleID: "x-apple-reminder://B", Title: "Audit"},
			}, nil
		default:
			return []applebridge.Reminder{
				{AppleID: "x-apple-reminder://B", Title: "Audit"},
			}, nil
		}
	}

	items, err := WaitForSingleVisible(fetch, "Audit", 3, 0)
	if err != nil {
		t.Fatalf("WaitForSingleVisible error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if len(items) != 1 || items[0].UUID() != "B" {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestWaitForVisibleCountHandlesZero(t *testing.T) {
	t.Parallel()

	calls := 0
	fetch := func() ([]applebridge.Reminder, error) {
		calls++
		if calls == 1 {
			return []applebridge.Reminder{
				{AppleID: "x-apple-reminder://A", Title: "Audit"},
			}, nil
		}
		return nil, nil
	}

	items, err := WaitForVisibleCount(fetch, "Audit", 0, 3, 0)
	if err != nil {
		t.Fatalf("WaitForVisibleCount error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
	if len(items) != 0 {
		t.Fatalf("unexpected items: %#v", items)
	}
}

func TestWaitForSingleVisibleReturnsLastError(t *testing.T) {
	t.Parallel()

	wantErr := fmt.Errorf("boom")
	calls := 0
	fetch := func() ([]applebridge.Reminder, error) {
		calls++
		return nil, wantErr
	}

	_, err := WaitForSingleVisible(fetch, "Audit", 2, 0)
	if err == nil || err.Error() != wantErr.Error() {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected 2 calls, got %d", calls)
	}
}

func TestRenderNotesPrefersStructuredStateOverLegacyNotes(t *testing.T) {
	t.Parallel()

	legacy := "old freeform body"
	now := time.Date(2026, 3, 24, 16, 32, 0, 0, time.FixedZone("BRT", -3*60*60))
	item := StateItem{
		LegacyNotes:  &legacy,
		StatusLine:   "checking parsed vs missing docs",
		Checklist:    []ChecklistItem{{Marker: " ", Text: "map parsed vs missing docs"}},
		HoursBudget:  3,
		TokensBudget: 40000,
		LastModel:    "gpt-5.4",
	}

	got := RenderNotes(item, now, now.Location())
	if got == legacy {
		t.Fatalf("expected structured rendering, got legacy notes %q", got)
	}
	if !containsAll(got, []string{
		"0 / 3h, 0 / 40k tk. Act. 4:32p gpt-5.4",
		"checking parsed vs missing docs",
		"[ ] map parsed vs missing docs",
	}) {
		t.Fatalf("unexpected rendered notes:\n%s", got)
	}
}

func TestRenderNotesKeepsLegacyNotesWithoutStructuredState(t *testing.T) {
	t.Parallel()

	legacy := "plain old body"
	item := StateItem{LegacyNotes: &legacy}
	got := RenderNotes(item, time.Now(), time.Local)
	if got != legacy {
		t.Fatalf("expected legacy notes, got %q", got)
	}
}

func TestUpdateStateItemClearsLegacyNotesWhenStructuredStateArrives(t *testing.T) {
	t.Parallel()

	legacy := "plain old body"
	st := &State{
		Items: map[string]StateItem{
			"finance-audit": {Key: "finance-audit", LegacyNotes: &legacy},
		},
	}
	status := "checking parsed vs missing docs"
	UpdateStateItemAt(st, Spec{
		Key:         "finance-audit",
		Title:       "Audit finance statement coverage and parsing",
		StatusLine:  &status,
		HoursBudget: floatPtrForTest(3),
	}, "", "", time.Now())

	if got := st.Items["finance-audit"].LegacyNotes; got != nil {
		t.Fatalf("expected legacy notes to be cleared, got %#v", *got)
	}
}

func strPtr(v string) *string { return &v }

func floatPtrForTest(v float64) *float64 { return &v }

func containsAll(haystack string, needles []string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}

func TestComputeBurnAggregatesSettledAndActiveLease(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 24, 16, 30, 0, 0, time.UTC)
	item := StateItem{
		HoursBudget:        2,
		TokensBudget:       10000,
		HoursSpentSettled:  0.5,
		TokensSpentSettled: 1200,
		ActiveLeases: map[string]ActiveLease{
			"sess-1": {
				SessionKey: "sess-1",
				StartedAt:  now.Add(-30 * time.Minute).Format(time.RFC3339),
				LastSeenAt: now.Format(time.RFC3339),
				Tokens:     800,
			},
		},
	}

	burn := ComputeBurn(item, now)
	if burn.HoursSpent != 1 {
		t.Fatalf("unexpected hours spent: %#v", burn)
	}
	if burn.TokensSpent != 2000 {
		t.Fatalf("unexpected tokens spent: %#v", burn)
	}
	if burn.Hammer != "green" {
		t.Fatalf("unexpected hammer: %#v", burn)
	}
}

func TestHammerThresholds(t *testing.T) {
	t.Parallel()

	cases := []struct {
		ratio float64
		want  string
	}{
		{0.59, "green"},
		{0.60, "yellow"},
		{0.80, "red"},
		{0.95, "critical"},
		{1.00, "hard-stop"},
	}
	for _, tc := range cases {
		if got := HammerFromRatio(tc.ratio); got != tc.want {
			t.Fatalf("ratio %.2f -> %s, want %s", tc.ratio, got, tc.want)
		}
	}
}

func TestRenderNotesUsesDeterministicFirstLine(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 3, 24, 16, 32, 0, 0, loc)
	item := StateItem{
		HoursBudget:  2,
		TokensBudget: 2_000_000,
		LastModel:    "gpt-5.4",
		StatusLine:   "collecting missing docs",
		Checklist: []ChecklistItem{
			{Marker: " ", Text: "a regular task"},
			{Marker: "!", Text: "important task"},
		},
	}

	got := RenderNotes(item, now, loc)
	want := "0 / 2h, 0 / 2M tk. Act. 4:32p gpt-5.4\ncollecting missing docs\n[ ] a regular task\n[!] important task"
	if got != want {
		t.Fatalf("unexpected notes:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderNotesOmitsUnknownModelPlaceholderWhenModelMissing(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 3, 24, 16, 32, 0, 0, loc)
	item := StateItem{
		HoursBudget:  1.5,
		TokensBudget: 160000,
		StatusLine:   "next run Wed 08:30; last run ok 10m",
	}

	got := RenderNotes(item, now, loc)
	want := "0 / 1.5h, 0 / 160k tk. Act. 4:32p\nnext run Wed 08:30; last run ok 10m"
	if got != want {
		t.Fatalf("unexpected notes without model:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderNotesOmitsInheritedModelLabel(t *testing.T) {
	t.Parallel()

	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatalf("load location: %v", err)
	}
	now := time.Date(2026, 3, 24, 16, 32, 0, 0, loc)
	item := StateItem{
		HoursBudget:  1.5,
		TokensBudget: 160000,
		LastModel:    "inherit",
		Checklist: []ChecklistItem{
			{Marker: "x", Text: "refresh p-reader evidence pack"},
		},
	}

	got := RenderNotes(item, now, loc)
	want := "0 / 1.5h, 0 / 160k tk. Act. 4:32p\n[x] refresh p-reader evidence pack"
	if got != want {
		t.Fatalf("unexpected notes with inherited model:\n%s\nwant:\n%s", got, want)
	}
}

func TestSettleLeaseRollsIntoSettledBurn(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 24, 16, 30, 0, 0, time.UTC)
	item := StateItem{
		ActiveLeases: map[string]ActiveLease{
			"sess-1": {
				SessionKey: "sess-1",
				StartedAt:  now.Add(-45 * time.Minute).Format(time.RFC3339),
				Tokens:     2500,
				Model:      "gpt-5.4",
			},
		},
	}

	SettleLease(&item, "sess-1", now, 0)
	if item.HoursSpentSettled != 0.8 {
		t.Fatalf("unexpected settled hours: %#v", item)
	}
	if item.TokensSpentSettled != 2500 {
		t.Fatalf("unexpected settled tokens: %#v", item)
	}
	if item.LastModel != "gpt-5.4" {
		t.Fatalf("unexpected model: %#v", item)
	}
	if len(item.ActiveLeases) != 0 {
		t.Fatalf("expected lease cleanup, got %#v", item.ActiveLeases)
	}
}
