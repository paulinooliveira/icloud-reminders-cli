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

func TestBuildUpdateReminderAppleScriptClearsTextBeforeSetting(t *testing.T) {
	title := "Morning"
	body := "0 / 1h, 0 / 10k tk."
	script := buildUpdateReminderAppleScript("x-apple-reminder://ABC", &title, &body, nil)
	if !strings.Contains(script, "set name of r to \"\"") {
		t.Fatalf("expected title clear step, got %q", script)
	}
	if !strings.Contains(script, "set name of r to \"Morning\"") {
		t.Fatalf("expected title set step, got %q", script)
	}
	if !strings.Contains(script, "set body of r to \"\"") {
		t.Fatalf("expected body clear step, got %q", script)
	}
	if !strings.Contains(script, "set body of r to \"0 / 1h, 0 / 10k tk.\"") {
		t.Fatalf("expected body set step, got %q", script)
	}
	if strings.Index(script, "set name of r to \"\"") > strings.Index(script, "set name of r to \"Morning\"") {
		t.Fatalf("expected title clear before set, got %q", script)
	}
	if strings.Index(script, "set body of r to \"\"") > strings.Index(script, "set body of r to \"0 / 1h, 0 / 10k tk.\"") {
		t.Fatalf("expected body clear before set, got %q", script)
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
