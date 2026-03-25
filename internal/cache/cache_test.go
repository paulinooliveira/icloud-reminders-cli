package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPathsRespectConfigDirEnv(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(configDirEnv, tmpDir)

	if got, want := ConfigDir(), tmpDir; got != want {
		t.Fatalf("ConfigDir() = %q, want %q", got, want)
	}
	if got, want := SessionFile(), filepath.Join(tmpDir, "session.json"); got != want {
		t.Fatalf("SessionFile() = %q, want %q", got, want)
	}
}

func TestConfigDirDefaultsToTestIsolatedPath(t *testing.T) {
	t.Setenv(configDirEnv, "")

	got := ConfigDir()
	if !strings.Contains(got, filepath.Join("icloud-reminders-test", filepath.Base(os.Args[0]))) {
		t.Fatalf("ConfigDir() = %q, want test-isolated path", got)
	}
}

func TestSaveAndLoadRoundTripViaSQLite(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(configDirEnv, tmpDir)
	t.Setenv("ICLOUD_REMINDERS_DB_PATH", filepath.Join(tmpDir, "state.db"))

	c := NewCache()
	c.Lists["List/AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"] = "Delegate"
	tok := "tok-123"
	c.SyncToken = &tok

	if err := c.FlushAll(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	loaded := Load()
	if got, want := loaded.Lists["List/AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"], "Delegate"; got != want {
		t.Fatalf("Load().Lists = %q, want %q", got, want)
	}
	if loaded.SyncToken == nil || *loaded.SyncToken != tok {
		t.Fatalf("Load().SyncToken = %v, want %q", loaded.SyncToken, tok)
	}
}

func TestSetReminderEnforcesCanonicalKey(t *testing.T) {
	c := NewCache()
	rd := &ReminderData{Title: "test"}

	// bare UUID — should be stored under canonical key
	if err := c.SetReminder("b267cc2e-e156-4cce-9ded-e1e5576f4911", rd); err != nil {
		t.Fatalf("SetReminder bare: %v", err)
	}
	const canonical = "Reminder/B267CC2E-E156-4CCE-9DED-E1E5576F4911"
	if c.Reminders[canonical] == nil {
		t.Fatalf("expected entry under canonical key %q", canonical)
	}

	// invalid — should error
	if err := c.SetReminder("not-a-uuid", rd); err == nil {
		t.Fatal("expected error for non-UUID id")
	}
}

func TestGetReminderFindsBareAlias(t *testing.T) {
	c := NewCache()
	rd := &ReminderData{Title: "aliased"}
	// simulate old code that stored under bare UUID
	c.Reminders["B267CC2E-E156-4CCE-9DED-E1E5576F4911"] = rd

	got := c.GetReminder("Reminder/B267CC2E-E156-4CCE-9DED-E1E5576F4911")
	if got == nil {
		t.Fatal("GetReminder should find entry stored under bare UUID")
	}
}

func TestReminderAliasesCanonicalFirst(t *testing.T) {
	aliases := ReminderAliases("b267cc2e-e156-4cce-9ded-e1e5576f4911")
	if len(aliases) == 0 {
		t.Fatal("expected aliases")
	}
	if aliases[0] != "Reminder/B267CC2E-E156-4CCE-9DED-E1E5576F4911" {
		t.Fatalf("first alias should be canonical, got %q", aliases[0])
	}
}
