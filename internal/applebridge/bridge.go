package applebridge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	Host              string `json:"host"`
	User              string `json:"user"`
	IdentityPath      string `json:"identity_path"`
	SebastianListName string `json:"sebastian_list_name"`
	SebastianListID   string `json:"sebastian_list_id"`
	TimeoutSeconds    int    `json:"timeout_seconds"`
}

type Reminder struct {
	AppleID   string `json:"id"`
	Title     string `json:"title"`
	Completed bool   `json:"completed"`
	Body      string `json:"body"`
}

func (r Reminder) UUID() string {
	return strings.TrimPrefix(r.AppleID, "x-apple-reminder://")
}

func ConfigPath() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "icloud-reminders", "apple-bridge.json")
}

func LoadConfig() (*Config, error) {
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if cfg.Host == "" || cfg.User == "" || cfg.IdentityPath == "" {
		return nil, fmt.Errorf("apple bridge config is incomplete")
	}
	return &cfg, nil
}

type Bridge struct {
	cfg *Config
}

var sshBinary = "ssh"
var scriptRetryBackoff = []time.Duration{0, 1 * time.Second, 2 * time.Second}

func New(cfg *Config) *Bridge {
	return &Bridge{cfg: cfg}
}

func (b *Bridge) ListReminders(listName string) ([]Reminder, error) {
	out, err := b.runJXA(fmt.Sprintf(`
const app = Application("Reminders");
const target = app.lists.byName(%s);
const items = target.reminders().map(r => ({
  id: String(r.id()),
  title: String(r.name()),
  completed: Boolean(r.completed()),
  body: String(r.body())
}));
JSON.stringify(items);
`, jsString(listName)))
	if err != nil {
		return nil, err
	}
	var reminders []Reminder
	if err := json.Unmarshal([]byte(out), &reminders); err != nil {
		return nil, fmt.Errorf("apple bridge list parse: %w", err)
	}
	return reminders, nil
}

func (b *Bridge) GetReminder(appleID string) (*Reminder, error) {
	out, err := b.runJXA(fmt.Sprintf(`
const app = Application("Reminders");
const r = app.reminders.byId(%s);
JSON.stringify({
  id: String(r.id()),
  title: String(r.name()),
  completed: Boolean(r.completed()),
  body: String(r.body())
});
`, jsString(appleID)))
	if err != nil {
		return nil, err
	}
	var reminder Reminder
	if err := json.Unmarshal([]byte(out), &reminder); err != nil {
		return nil, fmt.Errorf("apple bridge get parse: %w", err)
	}
	return &reminder, nil
}

func (b *Bridge) DeleteReminder(appleID string) error {
	_, err := b.runAppleScript(fmt.Sprintf(`
tell application "Reminders"
  delete reminder id %s
end tell
`, appleScriptString(appleID)))
	return err
}

func IsNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.ReplaceAll(err.Error(), "’", "'"))
	return strings.Contains(msg, "can't get reminder") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "invalid key form")
}

func (b *Bridge) UpdateReminder(appleID string, title, body *string, completed *bool) error {
	var statements []string
	statements = append(statements, fmt.Sprintf("set r to reminder id %s", appleScriptString(appleID)))
	if title != nil {
		statements = append(statements, fmt.Sprintf("set name of r to %s", appleScriptString(*title)))
	}
	if body != nil {
		statements = append(statements, fmt.Sprintf("set body of r to %s", appleScriptString(*body)))
	}
	if completed != nil {
		if *completed {
			statements = append(statements, "set completed of r to true")
		} else {
			statements = append(statements, "set completed of r to false")
		}
	}
	if len(statements) == 1 {
		return nil
	}
	_, err := b.runAppleScript("tell application \"Reminders\"\n  " + strings.Join(statements, "\n  ") + "\nend tell\n")
	return err
}

func (b *Bridge) runJXA(script string) (string, error) {
	return b.runScript("JavaScript", script)
}

func (b *Bridge) runAppleScript(script string) (string, error) {
	return b.runScript("", script)
}

func (b *Bridge) runScript(language, script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout())
	defer cancel()

	python := `import base64,subprocess,sys; language=sys.argv[1]; script=base64.b64decode(sys.argv[2]).decode("utf-8"); cmd=["/usr/bin/osascript"]; cmd += ["-l", language] if language else []; cmd += ["-"]; res=subprocess.run(cmd,input=script,text=True,capture_output=True); sys.stdout.write(res.stdout); sys.stderr.write(res.stderr); raise SystemExit(res.returncode)`
	remoteCmd := strings.Join([]string{
		"/usr/bin/python3",
		"-c",
		shellQuote(python),
		shellQuote(language),
		shellQuote(base64.StdEncoding.EncodeToString([]byte(script))),
	}, " ")

	args := []string{
		"-T",
		"-i", b.cfg.IdentityPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=no",
		"-o", "ConnectTimeout=8",
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=1",
		fmt.Sprintf("%s@%s", b.cfg.User, b.cfg.Host),
		remoteCmd,
	}

	var lastErr error
	for idx, delay := range scriptRetryBackoff {
		if delay > 0 {
			time.Sleep(delay)
		}
		cmd := exec.CommandContext(ctx, sshBinary, args...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			if ctx.Err() == context.DeadlineExceeded {
				return "", fmt.Errorf("apple bridge ssh timeout after %s", b.timeout())
			}
			lastErr = fmt.Errorf("apple bridge ssh: %w: %s", err, strings.TrimSpace(stderr.String()))
			if idx == len(scriptRetryBackoff)-1 || !isRetryableBridgeError(lastErr) {
				return "", lastErr
			}
			continue
		}
		return strings.TrimSpace(stdout.String()), nil
	}
	if lastErr != nil {
		return "", lastErr
	}
	return "", fmt.Errorf("apple bridge ssh: failed without output")
}

func (b *Bridge) timeout() time.Duration {
	if b != nil && b.cfg != nil && b.cfg.TimeoutSeconds > 0 {
		return time.Duration(b.cfg.TimeoutSeconds) * time.Second
	}
	return 20 * time.Second
}

func jsString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
}

func appleScriptString(s string) string {
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func isRetryableBridgeError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(strings.ReplaceAll(err.Error(), "’", "'"))
	return strings.Contains(msg, "can't get object") ||
		strings.Contains(msg, "server overloaded") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "broken pipe")
}
