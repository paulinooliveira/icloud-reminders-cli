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

func (b *Bridge) ActiveReminderUUIDsFromStore(listID string) ([]string, error) {
	out, err := b.runRemotePython(fmt.Sprintf(`
import glob, json, sqlite3
list_id = %s
username = %s
paths = sorted(glob.glob(f"/Users/{username}/Library/Group Containers/group.com.apple.reminders/Container_v1/Stores/Data-*.sqlite"))
active = set()
for path in paths:
    con = sqlite3.connect(path)
    rows = con.execute(
        """
        select r.ZCKIDENTIFIER
        from ZREMCDREMINDER r
        join ZREMCDBASELIST l on l.Z_PK = r.ZLIST
        where l.ZCKIDENTIFIER = ?
          and ifnull(r.ZMARKEDFORDELETION, 0) = 0
          and ifnull(r.ZCOMPLETED, 0) = 0
        """,
        (list_id,),
    ).fetchall()
    for (rid,) in rows:
        if rid:
            active.add(str(rid).upper())
print(json.dumps(sorted(active)))
`, pyString(listID), pyString(b.cfg.User)))
	if err != nil {
		return nil, err
	}
	var ids []string
	if err := json.Unmarshal([]byte(out), &ids); err != nil {
		return nil, fmt.Errorf("apple bridge store parse: %w", err)
	}
	return ids, nil
}

func (b *Bridge) CleanupEmptySectionsInStore(listID string) ([]string, error) {
	out, err := b.runRemotePython(fmt.Sprintf(`
import glob, gzip, hashlib, json, sqlite3
list_id = %s
username = %s
paths = sorted(glob.glob(f"/Users/{username}/Library/Group Containers/group.com.apple.reminders/Container_v1/Stores/Data-*.sqlite"))
deleted = set()
for path in paths:
    con = sqlite3.connect(path)
    con.row_factory = sqlite3.Row
    list_row = con.execute(
        "select Z_PK, ZMEMBERSHIPSOFREMINDERSINSECTIONSASDATA from ZREMCDBASELIST where ZCKIDENTIFIER = ?",
        (list_id,),
    ).fetchone()
    if not list_row:
        continue
    active = {
        str(rid).upper()
        for (rid,) in con.execute(
            """
            select ZCKIDENTIFIER
            from ZREMCDREMINDER
            where ZLIST = ?
              and ZCKIDENTIFIER is not null
              and ifnull(ZMARKEDFORDELETION, 0) = 0
              and ifnull(ZCOMPLETED, 0) = 0
            """,
            (list_row["Z_PK"],),
        )
        if rid
    }
    raw = bytes(list_row["ZMEMBERSHIPSOFREMINDERSINSECTIONSASDATA"] or b"")
    memberships_payload = {"memberships": []}
    if raw:
        decoded = gzip.decompress(raw) if raw[:2] == b"\x1f\x8b" else raw
        memberships_payload = json.loads(decoded.decode("utf-8"))
    memberships = memberships_payload.get("memberships") or []
    filtered = [m for m in memberships if str(m.get("memberID", "")).upper() in active]
    live_group_ids = {str(m.get("groupID", "")).upper() for m in filtered if m.get("groupID")}
    if filtered != memberships:
        memberships_payload["memberships"] = filtered
        encoded = json.dumps(memberships_payload, separators=(",", ":")).encode("utf-8")
        checksum = hashlib.sha512(encoded).hexdigest()
        con.execute(
            """
            update ZREMCDBASELIST
               set ZMEMBERSHIPSOFREMINDERSINSECTIONSASDATA = ?,
                   ZMEMBERSHIPSOFREMINDERSINSECTIONSCHECKSUM = ?
             where Z_PK = ?
            """,
            (encoded, checksum, list_row["Z_PK"]),
        )
    section_rows = list(
        con.execute(
            """
            select Z_PK, ZDISPLAYNAME, ZCKIDENTIFIER
            from ZREMCDBASESECTION
            where ZLIST = ?
              and ifnull(ZMARKEDFORDELETION, 0) = 0
            """,
            (list_row["Z_PK"],),
        )
    )
    for row in section_rows:
        section_id = str(row["ZCKIDENTIFIER"] or "").upper()
        if section_id in live_group_ids:
            continue
        con.execute("update ZREMCDBASESECTION set ZMARKEDFORDELETION = 1 where Z_PK = ?", (row["Z_PK"],))
        deleted.add(str(row["ZDISPLAYNAME"] or row["ZCKIDENTIFIER"] or row["Z_PK"]))
    con.commit()
print(json.dumps(sorted(deleted)))
`, pyString(listID), pyString(b.cfg.User)))
	if err != nil {
		return nil, err
	}
	var sections []string
	if err := json.Unmarshal([]byte(out), &sections); err != nil {
		return nil, fmt.Errorf("apple bridge cleanup parse: %w", err)
	}
	return sections, nil
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
	uuid := strings.ToUpper(strings.TrimSpace(strings.TrimPrefix(appleID, "x-apple-reminder://")))
	if uuid == "" {
		return fmt.Errorf("apple bridge update: empty reminder id")
	}

	if title != nil || body != nil {
		if err := b.updateReminderTextInStore(uuid, title, body); err != nil {
			return err
		}
	}

	if completed == nil {
		return nil
	}

	script := buildUpdateReminderAppleScript(appleID, nil, nil, completed)
	if strings.TrimSpace(script) == "" {
		return nil
	}
	_, err := b.runAppleScript(script)
	return err
}

func (b *Bridge) updateReminderTextInStore(reminderUUID string, title, body *string) error {
	script := buildStoreUpdatePythonScript(reminderUUID, title, body, b.cfg.User)
	out, err := b.runRemotePython(script)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "0" {
		return fmt.Errorf("apple bridge store update: reminder %s not found", reminderUUID)
	}
	return nil
}

func buildStoreUpdatePythonScript(reminderUUID string, title, body *string, username string) string {
	assignments := make([]string, 0, 2)
	if title != nil {
		assignments = append(assignments, `ZTITLE = ?`)
	}
	if body != nil {
		assignments = append(assignments, `ZNOTES = ?`)
	}

	args := make([]string, 0, 2)
	if title != nil {
		args = append(args, pyString(*title))
	}
	if body != nil {
		args = append(args, pyString(*body))
	}

	return fmt.Sprintf(`
import glob, sqlite3, sys
reminder_uuid = %s
username = %s
paths = sorted(glob.glob(f"/Users/{username}/Library/Group Containers/group.com.apple.reminders/Container_v1/Stores/Data-*.sqlite"))
updated = 0
for path in paths:
    if path.endswith("Data-local.sqlite"):
        continue
    con = sqlite3.connect(path)
    row = con.execute(
        """
        select Z_PK
        from ZREMCDREMINDER
        where ZCKIDENTIFIER = ?
          and ifnull(ZMARKEDFORDELETION, 0) = 0
        """,
        (reminder_uuid,),
    ).fetchone()
    if not row:
        continue
    params = [%s]
    params.append(row[0])
    con.execute(
        "update ZREMCDREMINDER set %s where Z_PK = ?",
        tuple(params),
    )
    con.commit()
    updated += con.total_changes
sys.stdout.write(str(updated))
`, pyString(reminderUUID), pyString(username), strings.Join(args, ", "), strings.Join(assignments, ", "))
}

func buildUpdateReminderAppleScript(appleID string, title, body *string, completed *bool) string {
	var statements []string
	statements = append(statements, fmt.Sprintf("set r to reminder id %s", appleScriptString(appleID)))
	if completed != nil {
		if *completed {
			statements = append(statements, "set completed of r to true")
		} else {
			statements = append(statements, "set completed of r to false")
		}
	}
	if len(statements) == 1 {
		return ""
	}
	return "tell application \"Reminders\"\n  " + strings.Join(statements, "\n  ") + "\nend tell\n"
}

func (b *Bridge) runJXA(script string) (string, error) {
	return b.runScript("JavaScript", script)
}

func (b *Bridge) runAppleScript(script string) (string, error) {
	return b.runScript("", script)
}

func (b *Bridge) runRemotePython(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), b.timeout())
	defer cancel()

	python := `import base64,subprocess,sys; script=base64.b64decode(sys.argv[1]).decode("utf-8"); res=subprocess.run(["/usr/bin/python3","-"],input=script,text=True,capture_output=True); sys.stdout.write(res.stdout); sys.stderr.write(res.stderr); raise SystemExit(res.returncode)`
	remoteCmd := strings.Join([]string{
		"/usr/bin/python3",
		"-c",
		shellQuote(python),
		shellQuote(base64.StdEncoding.EncodeToString([]byte(script))),
	}, " ")
	return b.runSSH(ctx, remoteCmd)
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
	return b.runSSH(ctx, remoteCmd)
}

func (b *Bridge) runSSH(ctx context.Context, remoteCmd string) (string, error) {
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

func pyString(s string) string {
	data, _ := json.Marshal(s)
	return string(data)
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
