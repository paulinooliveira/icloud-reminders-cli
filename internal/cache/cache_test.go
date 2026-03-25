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
	if got, want := CacheFile(), filepath.Join(tmpDir, "ck_cache.json"); got != want {
		t.Fatalf("CacheFile() = %q, want %q", got, want)
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

func TestSaveAndLoadUseConfiguredPath(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv(configDirEnv, tmpDir)

	c := NewCache()
	c.Lists["list-1"] = "Delegate"
	if err := c.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmpDir, "ck_cache.json")); err != nil {
		t.Fatalf("stat configured cache file: %v", err)
	}
	loaded := Load()
	if got, want := loaded.Lists["list-1"], "Delegate"; got != want {
		t.Fatalf("Load().Lists[list-1] = %q, want %q", got, want)
	}
}
