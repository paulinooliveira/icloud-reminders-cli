package remotepolicy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeDoc(t *testing.T, path string, keys []Policy) {
	t.Helper()
	data, err := json.Marshal(document{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStoreAuthenticatesAndRevokes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	writeDoc(t, path, []Policy{{ID: "agent", KeyHash: HashToken("correct"), Lists: []string{"Work"}, Enabled: true}})
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	policy, ok := store.Authenticate("correct")
	if !ok || !policy.AllowsList("work") || policy.Write {
		t.Fatalf("unexpected policy: %+v ok=%v", policy, ok)
	}
	if _, ok := store.Authenticate("wrong"); ok {
		t.Fatal("wrong token authenticated")
	}
	time.Sleep(10 * time.Millisecond)
	writeDoc(t, path, []Policy{{ID: "agent", KeyHash: HashToken("correct"), Lists: []string{"Work"}, Enabled: false}})
	if _, ok := store.Authenticate("correct"); ok {
		t.Fatal("revoked token authenticated")
	}
}

func TestStoreRejectsUnsafeOrMalformedFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	writeDoc(t, path, []Policy{{ID: "agent", KeyHash: HashToken("token"), Lists: []string{"Work"}, Enabled: true}})
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(path); err == nil {
		t.Fatal("0644 key file was accepted")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, path, []Policy{{ID: "agent", KeyHash: "not-a-hash", Lists: []string{"Work"}, Enabled: true}})
	if _, err := NewStore(path); err == nil {
		t.Fatal("malformed hash was accepted")
	}
}

func TestStoreFailsClosedWhenLoadedFileBecomesMissingOrUnsafe(t *testing.T) {
	for _, test := range []struct {
		name      string
		breakFile func(string) error
	}{
		{name: "missing", breakFile: os.Remove},
		{name: "unsafe permissions", breakFile: func(path string) error { return os.Chmod(path, 0o644) }},
		{name: "malformed", breakFile: func(path string) error { return os.WriteFile(path, []byte("{"), 0o600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "keys.json")
			writeDoc(t, path, []Policy{{ID: "agent", KeyHash: HashToken("correct"), Lists: []string{"Work"}, Enabled: true}})
			store, err := NewStore(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := store.Authenticate("correct"); !ok {
				t.Fatal("valid token did not authenticate")
			}
			time.Sleep(10 * time.Millisecond)
			if err := test.breakFile(path); err != nil {
				t.Fatal(err)
			}
			if _, ok := store.Authenticate("correct"); ok {
				t.Fatal("token authenticated after key store became invalid")
			}
		})
	}
}

func TestStoreReloadsAtomicReplacementWithPreservedMtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	writeDoc(t, path, []Policy{{ID: "agent", KeyHash: HashToken("old"), Lists: []string{"Work"}, Enabled: true}})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement.json")
	writeDoc(t, replacement, []Policy{{ID: "agent", KeyHash: HashToken("new"), Lists: []string{"Work"}, Enabled: true}})
	if err := os.Chtimes(replacement, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("old"); ok {
		t.Fatal("old token survived atomic replacement")
	}
	if _, ok := store.Authenticate("new"); !ok {
		t.Fatal("new token was not loaded after atomic replacement")
	}
}

func TestStoreReloadsInPlaceRewriteWithPreservedMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")
	oldHash, newHash := HashToken("old"), HashToken("new")
	writeDoc(t, path, []Policy{{ID: "agent", KeyHash: oldHash, Lists: []string{"Work"}, Enabled: true}})
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(document{Keys: []Policy{{ID: "agent", KeyHash: newHash, Lists: []string{"Work"}, Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate("old"); ok {
		t.Fatal("old token survived in-place rewrite")
	}
	if _, ok := store.Authenticate("new"); !ok {
		t.Fatal("new token was not loaded after in-place rewrite")
	}
}
