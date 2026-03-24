package queue

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveAndLoadStateThroughSQLite(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	state := &State{
		Bindings: map[string]string{"session-a": "queue-a"},
		Items: map[string]StateItem{
			"queue-a": {
				Key:                "queue-a",
				Title:              "Queue A",
				CloudID:            "Reminder/AAA",
				AppleID:            "x-apple-reminder://AAA",
				Section:            "Finance",
				Tags:               []string{"main", "admin"},
				Priority:           1,
				UpdatedAt:          "2026-03-24T12:00:00Z",
				StatusLine:         "working",
				Checklist:          []ChecklistItem{{Marker: " ", Text: "task"}, {Marker: "!", Text: "urgent"}},
				HoursBudget:        2,
				TokensBudget:       40000,
				HoursSpentSettled:  0.5,
				TokensSpentSettled: 1200,
				LastModel:          "gpt-5.4",
				Executor:           "admin",
				Blocked:            true,
				LastHammer:         "yellow",
				Children: map[string]ChildStateItem{
					"child-a": {
						Key:       "child-a",
						Title:     "Child A",
						CloudID:   "Reminder/CHILD",
						AppleID:   "x-apple-reminder://CHILD",
						UpdatedAt: "2026-03-24T12:05:00Z",
						Priority:  5,
						Flagged:   true,
					},
				},
				ActiveLeases: map[string]ActiveLease{
					"session-a": {
						SessionKey: "session-a",
						AgentID:    "agent-1",
						StartedAt:  "2026-03-24T12:00:00Z",
						LastSeenAt: "2026-03-24T12:10:00Z",
						Model:      "gpt-5.4",
						Tokens:     900,
					},
				},
			},
		},
	}

	if err := state.Save(); err != nil {
		t.Fatalf("save state: %v", err)
	}

	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	if got := reloaded.Bindings["session-a"]; got != "queue-a" {
		t.Fatalf("binding mismatch: got %q", got)
	}
	item := reloaded.Items["queue-a"]
	if item.Title != "Queue A" || item.CloudID != "Reminder/AAA" || item.Executor != "admin" || !item.Blocked {
		t.Fatalf("item mismatch: %#v", item)
	}
	if len(item.Children) != 1 || item.Children["child-a"].Title != "Child A" {
		t.Fatalf("child mismatch: %#v", item.Children)
	}
	if len(item.ActiveLeases) != 1 || item.ActiveLeases["session-a"].Tokens != 900 {
		t.Fatalf("lease mismatch: %#v", item.ActiveLeases)
	}
}

func TestLoadStateMigratesLegacyJSONOnce(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacy := &State{
		Bindings: map[string]string{"session-b": "queue-b"},
		Items: map[string]StateItem{
			"queue-b": {
				Key:      "queue-b",
				Title:    "Queue B",
				CloudID:  "Reminder/BBB",
				Executor: "main",
			},
		},
	}
	if err := os.MkdirAll(filepath.Dir(StatePath()), 0700); err != nil {
		t.Fatalf("mkdir legacy dir: %v", err)
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal legacy: %v", err)
	}
	if err := os.WriteFile(StatePath(), data, 0600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	reloaded, err := LoadState()
	if err != nil {
		t.Fatalf("load migrated state: %v", err)
	}
	if got := reloaded.Items["queue-b"].Title; got != "Queue B" {
		t.Fatalf("expected migrated title, got %q", got)
	}
	if _, err := os.Stat(StatePath() + ".migrated.bak"); err != nil {
		t.Fatalf("expected migrated backup: %v", err)
	}
}
