package applebridge

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListRemindersUsesNonInteractiveSSHAndParsesJSON(t *testing.T) {
	tmpDir := t.TempDir()
	logPath := filepath.Join(tmpDir, "ssh-args.txt")
	stdinPath := filepath.Join(tmpDir, "ssh-stdin.txt")
	sshPath := filepath.Join(tmpDir, "ssh")
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" > " + testShellQuote(logPath) + "\n" +
		"cat > " + testShellQuote(stdinPath) + "\n" +
		"printf '%s' '[{\"id\":\"x-apple-reminder://ABC\",\"title\":\"Audit\",\"completed\":false,\"body\":\"clean\"}]'\n"
	if err := os.WriteFile(sshPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	oldSSH := sshBinary
	sshBinary = sshPath
	defer func() { sshBinary = oldSSH }()

	bridge := New(&Config{
		Host:         "example-host",
		User:         "tester",
		IdentityPath: "/tmp/key",
	})

	items, err := bridge.ListReminders("Sebastian")
	if err != nil {
		t.Fatalf("ListReminders error: %v", err)
	}
	if len(items) != 1 || items[0].UUID() != "ABC" || items[0].Title != "Audit" || items[0].Body != "clean" {
		t.Fatalf("unexpected reminders: %#v", items)
	}

	argsData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read ssh args: %v", err)
	}
	args := string(argsData)
	if !strings.Contains(args, "-T") {
		t.Fatalf("expected -T in ssh args, got %q", args)
	}
	if strings.Contains(args, "osascript\n-l\nJavaScript\n-") {
		t.Fatalf("expected remote python wrapper, got direct stdin osascript invocation: %q", args)
	}

	stdinData, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read ssh stdin: %v", err)
	}
	if len(stdinData) != 0 {
		t.Fatalf("expected no ssh stdin payload, got %q", string(stdinData))
	}
}

func TestGetReminderParsesJSON(t *testing.T) {
	tmpDir := t.TempDir()
	sshPath := filepath.Join(tmpDir, "ssh")
	script := "#!/bin/sh\nprintf '%s' '{\"id\":\"x-apple-reminder://ABC\",\"title\":\"Audit\",\"completed\":true,\"body\":\"clean\"}'\n"
	if err := os.WriteFile(sshPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	oldSSH := sshBinary
	sshBinary = sshPath
	defer func() { sshBinary = oldSSH }()

	bridge := New(&Config{Host: "example-host", User: "tester", IdentityPath: "/tmp/key"})
	item, err := bridge.GetReminder("x-apple-reminder://ABC")
	if err != nil {
		t.Fatalf("GetReminder error: %v", err)
	}
	if item == nil || item.UUID() != "ABC" || item.Title != "Audit" || item.Body != "clean" || !item.Completed {
		t.Fatalf("unexpected reminder: %#v", item)
	}
}

func TestActiveReminderUUIDsFromStoreParsesJSON(t *testing.T) {
	tmpDir := t.TempDir()
	sshPath := filepath.Join(tmpDir, "ssh")
	script := "#!/bin/sh\nprintf '%s' '[\"ABC\",\"DEF\"]'\n"
	if err := os.WriteFile(sshPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	oldSSH := sshBinary
	sshBinary = sshPath
	defer func() { sshBinary = oldSSH }()

	bridge := New(&Config{Host: "example-host", User: "tester", IdentityPath: "/tmp/key"})
	ids, err := bridge.ActiveReminderUUIDsFromStore("LIST-ID")
	if err != nil {
		t.Fatalf("ActiveReminderUUIDsFromStore error: %v", err)
	}
	if len(ids) != 2 || ids[0] != "ABC" || ids[1] != "DEF" {
		t.Fatalf("unexpected ids: %#v", ids)
	}
}

func TestCleanupEmptySectionsInStoreParsesJSON(t *testing.T) {
	tmpDir := t.TempDir()
	sshPath := filepath.Join(tmpDir, "ssh")
	script := "#!/bin/sh\nprintf '%s' '[\"Finance Systems\",\"People Admin\"]'\n"
	if err := os.WriteFile(sshPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake ssh: %v", err)
	}

	oldSSH := sshBinary
	sshBinary = sshPath
	defer func() { sshBinary = oldSSH }()

	bridge := New(&Config{Host: "example-host", User: "tester", IdentityPath: "/tmp/key"})
	sections, err := bridge.CleanupEmptySectionsInStore("LIST-ID")
	if err != nil {
		t.Fatalf("CleanupEmptySectionsInStore error: %v", err)
	}
	if len(sections) != 2 || sections[0] != "Finance Systems" || sections[1] != "People Admin" {
		t.Fatalf("unexpected sections: %#v", sections)
	}
}

func TestBuildStoreUpdatePythonScriptUsesDirectReplacement(t *testing.T) {
	title := "Morning"
	body := "0 / 1h, 0 / 10k tk."
	script := buildStoreUpdatePythonScript("ABC", &title, &body, "tester")
	if !strings.Contains(script, `ZTITLE = ?`) {
		t.Fatalf("expected title store update, got %q", script)
	}
	if !strings.Contains(script, `ZNOTES = ?`) {
		t.Fatalf("expected notes store update, got %q", script)
	}
	if strings.Contains(script, `set name of r`) || strings.Contains(script, `set body of r`) {
		t.Fatalf("expected store update script, got AppleScript-like text %q", script)
	}
	if !strings.Contains(script, `where ZCKIDENTIFIER = ?`) {
		t.Fatalf("expected exact reminder lookup, got %q", script)
	}
	if !strings.Contains(script, `username = "tester"`) {
		t.Fatalf("expected username binding, got %q", script)
	}
	if !strings.Contains(script, `f"/Users/{username}/Library/Group Containers/group.com.apple.reminders/Container_v1/Stores/Data-*.sqlite"`) {
		t.Fatalf("expected interpolated user path, got %q", script)
	}
	if strings.Contains(script, `"/Users/"tester"`) {
		t.Fatalf("expected valid Python string composition, got %q", script)
	}
}

func TestBuildUpdateReminderAppleScriptOnlyTouchesCompletion(t *testing.T) {
	completed := true
	script := buildUpdateReminderAppleScript("x-apple-reminder://ABC", nil, nil, &completed)
	if strings.Contains(script, "set name of r") || strings.Contains(script, "set body of r") {
		t.Fatalf("expected no text edits in AppleScript, got %q", script)
	}
	if !strings.Contains(script, "set completed of r to true") {
		t.Fatalf("expected completed update, got %q", script)
	}
}

func TestIsNotFoundError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		errText string
		want    bool
	}{
		{"apple bridge ssh: exit status 1: Can’t get reminder id \"x-apple-reminder://ABC\".", true},
		{"apple bridge ssh: exit status 1: not found", true},
		{"apple bridge ssh: exit status 1: invalid key form", true},
		{"apple bridge ssh: exit status 1: permission denied", false},
	}

	for _, tc := range cases {
		if got := IsNotFoundError(assertErr(tc.errText)); got != tc.want {
			t.Fatalf("IsNotFoundError(%q) = %v want %v", tc.errText, got, tc.want)
		}
	}
}

func TestIsRetryableBridgeError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		errText string
		want    bool
	}{
		{"apple bridge ssh: exit status 1: execution error: Error: Error: Can't get object. (-1728)", true},
		{"apple bridge ssh: exit status 1: connection reset by peer", true},
		{"apple bridge ssh: exit status 1: permission denied", false},
	}

	for _, tc := range cases {
		if got := isRetryableBridgeError(assertErr(tc.errText)); got != tc.want {
			t.Fatalf("isRetryableBridgeError(%q) = %v want %v", tc.errText, got, tc.want)
		}
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }

func testShellQuote(v string) string {
	return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'"
}
