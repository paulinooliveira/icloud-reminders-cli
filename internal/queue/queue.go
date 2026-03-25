package queue

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/store"
	"icloud-reminders/pkg/models"
)

type State struct {
	Bindings map[string]string    `json:"bindings,omitempty"`
	Items    map[string]StateItem `json:"items"`
}

type StateItem struct {
	Key                string                    `json:"key"`
	Title              string                    `json:"title"`
	CloudID            string                    `json:"cloud_id,omitempty"`
	AppleID            string                    `json:"apple_id,omitempty"`
	Children           map[string]ChildStateItem `json:"children,omitempty"`
	Section            string                    `json:"section,omitempty"`
	Tags               []string                  `json:"tags,omitempty"`
	Priority           int                       `json:"priority,omitempty"`
	UpdatedAt          string                    `json:"updated_at,omitempty"`
	Due                *string                   `json:"due,omitempty"`
	StatusLine         string                    `json:"status_line,omitempty"`
	Checklist          []ChecklistItem           `json:"checklist,omitempty"`
	HoursBudget        float64                   `json:"hours_budget,omitempty"`
	TokensBudget       int64                     `json:"tokens_budget,omitempty"`
	HoursSpentSettled  float64                   `json:"hours_spent_settled,omitempty"`
	TokensSpentSettled int64                     `json:"tokens_spent_settled,omitempty"`
	ActiveLeases       map[string]ActiveLease    `json:"active_leases,omitempty"`
	LastModel          string                    `json:"last_model,omitempty"`
	Executor           string                    `json:"executor,omitempty"`
	Blocked            bool                      `json:"blocked,omitempty"`
	LegacyNotes        *string                   `json:"legacy_notes,omitempty"`
	LastHammer         string                    `json:"last_hammer,omitempty"`
}

type ChildStateItem struct {
	Key       string  `json:"key"`
	Title     string  `json:"title"`
	CloudID   string  `json:"cloud_id,omitempty"`
	AppleID   string  `json:"apple_id,omitempty"`
	UpdatedAt string  `json:"updated_at,omitempty"`
	Due       *string `json:"due,omitempty"`
	Priority  int     `json:"priority,omitempty"`
	Flagged   bool    `json:"flagged,omitempty"`
}

type ChecklistItem struct {
	Marker string `json:"marker"`
	Text   string `json:"text"`
}

type ActiveLease struct {
	SessionKey string `json:"session_key"`
	AgentID    string `json:"agent_id,omitempty"`
	StartedAt  string `json:"started_at"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
	Model      string `json:"model,omitempty"`
	Tokens     int64  `json:"tokens,omitempty"`
}

type BurnSnapshot struct {
	HoursSpent  float64
	TokensSpent int64
	HoursRatio  float64
	TokensRatio float64
	WorstRatio  float64
	Hammer      string
}

type Spec struct {
	Key          string
	Title        string
	ListID       string
	ListName     string
	Section      string
	Tags         []string
	Priority     int
	Notes        *string
	Due          *string
	Flagged      *bool
	StatusLine   *string
	Checklist    []ChecklistItem
	HoursBudget  *float64
	TokensBudget *int64
	Executor     string
	Blocked      *bool
}

func SpecHasStructuredState(spec Spec) bool {
	return spec.HoursBudget != nil ||
		spec.TokensBudget != nil ||
		spec.StatusLine != nil ||
		spec.Checklist != nil ||
		spec.Executor != "" ||
		spec.Blocked != nil
}

type CanonicalChoice struct {
	Keep   *applebridge.Reminder
	Delete []applebridge.Reminder
	Reason string
}

func StatePath() string {
	return filepath.Join(os.Getenv("HOME"), ".config", "icloud-reminders", "sebastian-queue-state.json")
}

func LoadState() (*State, error) {
	db, err := store.Open()
	if err != nil {
		return nil, err
	}
	defer db.Close()
	if err := migrateLegacyJSONState(db); err != nil {
		return nil, err
	}
	return loadStateFromDB(db)
}

func (s *State) Save() error {
	if s.Bindings == nil {
		s.Bindings = map[string]string{}
	}
	if s.Items == nil {
		s.Items = map[string]StateItem{}
	}
	db, err := store.Open()
	if err != nil {
		return err
	}
	defer db.Close()
	return saveStateToDB(db, s)
}

func loadStateFromDB(db *sql.DB) (*State, error) {
	st := &State{
		Bindings: map[string]string{},
		Items:    map[string]StateItem{},
	}

	rows, err := db.Query(`
		SELECT queue_key, title, COALESCE(cloud_id,''), COALESCE(apple_id,''), COALESCE(section_name,''),
		       COALESCE(tags_json,'[]'), priority_value, COALESCE(updated_at,''), due_at,
		       COALESCE(status_line,''), COALESCE(checklist_json,'[]'),
		       hours_budget, tokens_budget, hours_spent_settled, tokens_spent_settled,
		       COALESCE(last_model,''), COALESCE(executor,''), blocked, legacy_notes, COALESCE(last_hammer,'')
		FROM queue_items
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			key, title, cloudID, appleID, section, tagsJSON, updatedAt, statusLine, checklistJSON string
			dueAt, legacyNotes                                                                    sql.NullString
			priority                                                                              int
			hoursBudget                                                                           float64
			tokensBudget, tokensSpent                                                             int64
			hoursSpent                                                                            float64
			lastModel, executor, lastHammer                                                       string
			blockedInt                                                                            int
		)
		if err := rows.Scan(&key, &title, &cloudID, &appleID, &section, &tagsJSON, &priority, &updatedAt, &dueAt,
			&statusLine, &checklistJSON, &hoursBudget, &tokensBudget, &hoursSpent, &tokensSpent,
			&lastModel, &executor, &blockedInt, &legacyNotes, &lastHammer); err != nil {
			return nil, err
		}
		item := StateItem{
			Key:                key,
			Title:              title,
			CloudID:            cloudID,
			AppleID:            appleID,
			Section:            section,
			Priority:           priority,
			UpdatedAt:          updatedAt,
			StatusLine:         statusLine,
			HoursBudget:        hoursBudget,
			TokensBudget:       tokensBudget,
			HoursSpentSettled:  hoursSpent,
			TokensSpentSettled: tokensSpent,
			LastModel:          lastModel,
			Executor:           executor,
			Blocked:            blockedInt != 0,
			LastHammer:         lastHammer,
			Children:           map[string]ChildStateItem{},
			ActiveLeases:       map[string]ActiveLease{},
		}
		if dueAt.Valid && strings.TrimSpace(dueAt.String) != "" {
			d := dueAt.String
			item.Due = &d
		}
		if legacyNotes.Valid {
			trimmed := strings.TrimSpace(legacyNotes.String)
			item.LegacyNotes = &trimmed
		}
		if err := json.Unmarshal([]byte(tagsJSON), &item.Tags); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(checklistJSON), &item.Checklist); err != nil {
			return nil, err
		}
		st.Items[key] = item
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	bindingRows, err := db.Query(`SELECT session_key, queue_key FROM queue_bindings`)
	if err != nil {
		return nil, err
	}
	defer bindingRows.Close()
	for bindingRows.Next() {
		var sessionKey, queueKey string
		if err := bindingRows.Scan(&sessionKey, &queueKey); err != nil {
			return nil, err
		}
		st.Bindings[sessionKey] = queueKey
	}
	if err := bindingRows.Err(); err != nil {
		return nil, err
	}

	childRows, err := db.Query(`
		SELECT parent_key, child_key, title, COALESCE(cloud_id,''), COALESCE(apple_id,''), COALESCE(updated_at,''),
		       due_at, priority_value, flagged
		FROM queue_children
	`)
	if err != nil {
		return nil, err
	}
	defer childRows.Close()
	for childRows.Next() {
		var (
			parentKey, childKey, title, cloudID, appleID, updatedAt string
			dueAt                                                   sql.NullString
			priority, flaggedInt                                    int
		)
		if err := childRows.Scan(&parentKey, &childKey, &title, &cloudID, &appleID, &updatedAt, &dueAt, &priority, &flaggedInt); err != nil {
			return nil, err
		}
		parent := st.Items[parentKey]
		if parent.Children == nil {
			parent.Children = map[string]ChildStateItem{}
		}
		child := ChildStateItem{
			Key:       childKey,
			Title:     title,
			CloudID:   cloudID,
			AppleID:   appleID,
			UpdatedAt: updatedAt,
			Priority:  priority,
			Flagged:   flaggedInt != 0,
		}
		if dueAt.Valid && strings.TrimSpace(dueAt.String) != "" {
			d := dueAt.String
			child.Due = &d
		}
		parent.Children[childKey] = child
		st.Items[parentKey] = parent
	}
	if err := childRows.Err(); err != nil {
		return nil, err
	}

	leaseRows, err := db.Query(`
		SELECT queue_key, session_key, COALESCE(agent_id,''), started_at, COALESCE(last_seen_at,''), COALESCE(model_name,''), token_count
		FROM queue_leases
	`)
	if err != nil {
		return nil, err
	}
	defer leaseRows.Close()
	for leaseRows.Next() {
		var queueKey, sessionKey, agentID, startedAt, lastSeenAt, model string
		var tokens int64
		if err := leaseRows.Scan(&queueKey, &sessionKey, &agentID, &startedAt, &lastSeenAt, &model, &tokens); err != nil {
			return nil, err
		}
		item := st.Items[queueKey]
		if item.ActiveLeases == nil {
			item.ActiveLeases = map[string]ActiveLease{}
		}
		item.ActiveLeases[sessionKey] = ActiveLease{
			SessionKey: sessionKey,
			AgentID:    agentID,
			StartedAt:  startedAt,
			LastSeenAt: lastSeenAt,
			Model:      model,
			Tokens:     tokens,
		}
		st.Items[queueKey] = item
	}
	return st, leaseRows.Err()
}

func saveStateToDB(db *sql.DB, s *State) error {
	return store.ExecTx(db, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`DELETE FROM queue_leases`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM queue_children`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM queue_bindings`); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM queue_items`); err != nil {
			return err
		}
		for key, item := range s.Items {
			tagsJSON, err := json.Marshal(dedupePreserveOrder(item.Tags))
			if err != nil {
				return err
			}
			checklistJSON, err := json.Marshal(normalizeChecklist(item.Checklist))
			if err != nil {
				return err
			}
			_, err = tx.Exec(`
				INSERT INTO queue_items (
					queue_key, title, cloud_id, apple_id, section_name, tags_json, priority_value, updated_at, due_at,
					status_line, checklist_json, hours_budget, tokens_budget, hours_spent_settled, tokens_spent_settled,
					last_model, executor, blocked, legacy_notes, last_hammer
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			`, key, item.Title, nullIfBlank(item.CloudID), nullIfBlank(item.AppleID), nullIfBlank(item.Section), string(tagsJSON),
				item.Priority, nullIfBlank(item.UpdatedAt), ptrOrNil(item.Due), nullIfBlank(item.StatusLine), string(checklistJSON),
				item.HoursBudget, item.TokensBudget, item.HoursSpentSettled, item.TokensSpentSettled, nullIfBlank(item.LastModel),
				nullIfBlank(item.Executor), boolToInt(item.Blocked), ptrOrNil(item.LegacyNotes), nullIfBlank(item.LastHammer))
			if err != nil {
				return err
			}
			for childKey, child := range item.Children {
				_, err := tx.Exec(`
					INSERT INTO queue_children (parent_key, child_key, title, cloud_id, apple_id, updated_at, due_at, priority_value, flagged)
					VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
				`, key, childKey, child.Title, nullIfBlank(child.CloudID), nullIfBlank(child.AppleID), nullIfBlank(child.UpdatedAt),
					ptrOrNil(child.Due), child.Priority, boolToInt(child.Flagged))
				if err != nil {
					return err
				}
			}
			for sessionKey, lease := range item.ActiveLeases {
				_, err := tx.Exec(`
					INSERT INTO queue_leases (queue_key, session_key, agent_id, started_at, last_seen_at, model_name, token_count)
					VALUES (?, ?, ?, ?, ?, ?, ?)
				`, key, sessionKey, nullIfBlank(lease.AgentID), lease.StartedAt, nullIfBlank(lease.LastSeenAt), nullIfBlank(lease.Model), lease.Tokens)
				if err != nil {
					return err
				}
			}
		}
		for sessionKey, queueKey := range s.Bindings {
			_, err := tx.Exec(`INSERT INTO queue_bindings (session_key, queue_key) VALUES (?, ?)`, sessionKey, queueKey)
			if err != nil {
				return err
			}
		}
		return nil
	})
}

func migrateLegacyJSONState(db *sql.DB) error {
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM queue_items`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	data, err := os.ReadFile(StatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.Bindings == nil {
		st.Bindings = map[string]string{}
	}
	if st.Items == nil {
		st.Items = map[string]StateItem{}
	}
	for key, item := range st.Items {
		item.Key = key
		if item.ActiveLeases == nil {
			item.ActiveLeases = map[string]ActiveLease{}
		}
		if item.Children == nil {
			item.Children = map[string]ChildStateItem{}
		}
		for childKey, child := range item.Children {
			child.Key = childKey
			item.Children[childKey] = child
		}
		st.Items[key] = item
	}
	if err := saveStateToDB(db, &st); err != nil {
		return err
	}
	backupPath := StatePath() + ".migrated.bak"
	if err := os.Rename(StatePath(), backupPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func ptrOrNil(v *string) any {
	if v == nil || strings.TrimSpace(*v) == "" {
		return nil
	}
	return *v
}

func nullIfBlank(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func ChooseCanonical(matches []applebridge.Reminder, cloudByUUID map[string]*models.Reminder, state StateItem, desiredNotes *string) CanonicalChoice {
	if len(matches) == 0 {
		return CanonicalChoice{}
	}

	score := func(r applebridge.Reminder) int {
		score := 0
		if state.AppleID != "" && r.AppleID == state.AppleID {
			score += 1000
		}
		if state.CloudID != "" && strings.EqualFold(r.UUID(), shortID(state.CloudID)) {
			score += 900
		}
		if _, ok := cloudByUUID[strings.ToUpper(r.UUID())]; ok {
			score += 800
		}
		if desiredNotes != nil && r.Body == *desiredNotes {
			score += 200
		}
		score -= len(r.Body)
		return score
	}

	best := matches[0]
	bestScore := score(best)
	for _, m := range matches[1:] {
		s := score(m)
		if s > bestScore {
			best = m
			bestScore = s
		}
	}
	out := CanonicalChoice{Keep: &best}
	for _, m := range matches {
		if m.AppleID != best.AppleID {
			out.Delete = append(out.Delete, m)
		}
	}
	return out
}

func UpdateStateItem(st *State, spec Spec, appleID, cloudID string) {
	now := time.Now()
	UpdateStateItemAt(st, spec, appleID, cloudID, now)
}

func UpdateStateItemAt(st *State, spec Spec, appleID, cloudID string, now time.Time) {
	if st.Items == nil {
		st.Items = map[string]StateItem{}
	}
	item := st.Items[spec.Key]
	item.Key = spec.Key
	item.Title = spec.Title
	item.CloudID = cloudID
	item.AppleID = appleID
	item.Section = spec.Section
	item.Due = spec.Due
	item.Tags = dedupePreserveOrder(spec.Tags)
	item.Priority = spec.Priority
	item.UpdatedAt = now.Format(time.RFC3339)
	item.Executor = spec.Executor
	if item.ActiveLeases == nil {
		item.ActiveLeases = map[string]ActiveLease{}
	}
	if item.Children == nil {
		item.Children = map[string]ChildStateItem{}
	}
	if spec.StatusLine != nil {
		item.StatusLine = strings.TrimSpace(*spec.StatusLine)
	}
	if spec.Checklist != nil {
		item.Checklist = normalizeChecklist(spec.Checklist)
	}
	if spec.HoursBudget != nil {
		item.HoursBudget = maxFloat(0, *spec.HoursBudget)
	}
	if spec.TokensBudget != nil {
		item.TokensBudget = maxInt64(0, *spec.TokensBudget)
	}
	if spec.Blocked != nil {
		item.Blocked = *spec.Blocked
	}
	if spec.Notes != nil {
		trimmed := strings.TrimSpace(*spec.Notes)
		item.LegacyNotes = &trimmed
	}
	if SpecHasStructuredState(spec) {
		item.LegacyNotes = nil
	}
	st.Items[spec.Key] = item
}

func NoteNeedsRendering(spec Spec, existing StateItem) bool {
	if SpecHasStructuredState(spec) {
		return true
	}
	if existing.HoursBudget > 0 || existing.TokensBudget > 0 || existing.StatusLine != "" || len(existing.Checklist) > 0 {
		return true
	}
	return false
}

func RenderNotes(item StateItem, now time.Time, loc *time.Location) string {
	if item.LegacyNotes != nil && !HasStructuredQueueState(item) {
		return strings.TrimSpace(*item.LegacyNotes)
	}
	lines := []string{RenderFirstLine(item, now, loc)}
	if status := strings.TrimSpace(item.StatusLine); status != "" {
		lines = append(lines, status)
	}
	for _, check := range normalizeChecklist(item.Checklist) {
		lines = append(lines, fmt.Sprintf("[%s] %s", check.Marker, check.Text))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func RenderFirstLine(item StateItem, now time.Time, loc *time.Location) string {
	burn := ComputeBurn(item, now)
	localNow := now
	if !now.IsZero() && loc != nil {
		localNow = now.In(loc)
	}
	model := normalizeRenderedModelLabel(item.LastModel)
	line := fmt.Sprintf(
		"%s / %sh, %s / %s tk. Act. %s",
		formatHours(burn.HoursSpent),
		formatHours(item.HoursBudget),
		formatTokens(burn.TokensSpent),
		formatTokens(item.TokensBudget),
		formatClock(localNow),
	)
	if model != "" {
		line = fmt.Sprintf("%s %s", line, model)
	}
	return line
}

func normalizeRenderedModelLabel(raw string) string {
	model := strings.TrimSpace(raw)
	switch strings.ToLower(model) {
	case "", "inherit", "inherited", "auto", "unknown", "unknown model":
		return ""
	default:
		return model
	}
}

func ComputeBurn(item StateItem, now time.Time) BurnSnapshot {
	hours := item.HoursSpentSettled
	tokens := item.TokensSpentSettled
	for _, lease := range item.ActiveLeases {
		started, ok := parseRFC3339(lease.StartedAt)
		if !ok {
			continue
		}
		end := now
		if seen, ok := parseRFC3339(lease.LastSeenAt); ok && seen.Before(end) {
			end = seen
		}
		if end.Before(started) {
			end = started
		}
		hours += end.Sub(started).Hours()
		tokens += lease.Tokens
	}
	hours = roundHours(hours)
	hoursRatio := ratio(hours, item.HoursBudget)
	tokensRatio := ratio(float64(tokens), float64(item.TokensBudget))
	worst := math.Max(hoursRatio, tokensRatio)
	return BurnSnapshot{
		HoursSpent:  hours,
		TokensSpent: tokens,
		HoursRatio:  hoursRatio,
		TokensRatio: tokensRatio,
		WorstRatio:  worst,
		Hammer:      HammerFromRatio(worst),
	}
}

func HammerFromRatio(ratio float64) string {
	switch {
	case ratio >= 1.0:
		return "hard-stop"
	case ratio >= 0.95:
		return "critical"
	case ratio >= 0.80:
		return "red"
	case ratio >= 0.60:
		return "yellow"
	default:
		return "green"
	}
}

func NeedsFlag(burn BurnSnapshot) bool {
	return burn.Hammer == "red" || burn.Hammer == "critical" || burn.Hammer == "hard-stop"
}

func HasStructuredQueueState(item StateItem) bool {
	return item.HoursBudget > 0 || item.TokensBudget > 0 || item.StatusLine != "" || len(item.Checklist) > 0 || len(item.ActiveLeases) > 0 || item.HoursSpentSettled > 0 || item.TokensSpentSettled > 0
}

func BindSession(st *State, sessionKey, queueKey string) {
	if strings.TrimSpace(sessionKey) == "" || strings.TrimSpace(queueKey) == "" {
		return
	}
	if st.Bindings == nil {
		st.Bindings = map[string]string{}
	}
	st.Bindings[sessionKey] = queueKey
}

func UnbindSession(st *State, sessionKey string) {
	if st.Bindings == nil || sessionKey == "" {
		return
	}
	delete(st.Bindings, sessionKey)
}

func TouchLease(item *StateItem, sessionKey, agentID, model string, now time.Time) {
	if item.ActiveLeases == nil {
		item.ActiveLeases = map[string]ActiveLease{}
	}
	lease := item.ActiveLeases[sessionKey]
	if lease.StartedAt == "" {
		lease.StartedAt = now.Format(time.RFC3339)
	}
	lease.SessionKey = sessionKey
	lease.LastSeenAt = now.Format(time.RFC3339)
	if strings.TrimSpace(agentID) != "" {
		lease.AgentID = agentID
	}
	if strings.TrimSpace(model) != "" {
		lease.Model = model
		item.LastModel = model
	}
	item.ActiveLeases[sessionKey] = lease
}

func AddLeaseTokens(item *StateItem, sessionKey string, tokens int64, model string, now time.Time) string {
	if tokens < 0 {
		tokens = 0
	}
	TouchLease(item, sessionKey, "", model, now)
	lease := item.ActiveLeases[sessionKey]
	lease.Tokens += tokens
	if strings.TrimSpace(model) != "" {
		lease.Model = model
		item.LastModel = model
	}
	item.ActiveLeases[sessionKey] = lease
	return ComputeBurn(*item, now).Hammer
}

func SettleLease(item *StateItem, sessionKey string, endedAt time.Time, durationOverride time.Duration) {
	if item.ActiveLeases == nil {
		return
	}
	lease, ok := item.ActiveLeases[sessionKey]
	if !ok {
		return
	}
	started, ok := parseRFC3339(lease.StartedAt)
	if !ok {
		started = endedAt
	}
	duration := endedAt.Sub(started)
	if durationOverride > 0 {
		duration = durationOverride
	}
	if duration < 0 {
		duration = 0
	}
	item.HoursSpentSettled = roundHours(item.HoursSpentSettled + duration.Hours())
	item.TokensSpentSettled += lease.Tokens
	if strings.TrimSpace(lease.Model) != "" {
		item.LastModel = lease.Model
	}
	delete(item.ActiveLeases, sessionKey)
}

func QueueKeysForStaleBindings(st *State) []string {
	keys := make([]string, 0, len(st.Bindings))
	for sessionKey := range st.Bindings {
		keys = append(keys, sessionKey)
	}
	sort.Strings(keys)
	return keys
}

func parseRFC3339(raw string) (time.Time, bool) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func ratio(spent, budget float64) float64 {
	if budget <= 0 {
		return 0
	}
	return spent / budget
}

func normalizeChecklist(items []ChecklistItem) []ChecklistItem {
	out := make([]ChecklistItem, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		marker := strings.TrimSpace(item.Marker)
		switch marker {
		case "x", "!", "~":
		case "":
			marker = " "
		default:
			marker = " "
		}
		out = append(out, ChecklistItem{Marker: marker, Text: text})
	}
	return out
}

func ParseChecklistLine(line string) (ChecklistItem, error) {
	trimmed := strings.TrimSpace(line)
	if len(trimmed) < 4 || trimmed[0] != '[' {
		return ChecklistItem{}, fmt.Errorf("invalid checklist item %q", line)
	}
	end := strings.IndexByte(trimmed, ']')
	if end != 2 {
		return ChecklistItem{}, fmt.Errorf("invalid checklist marker %q", line)
	}
	marker := trimmed[1:2]
	text := strings.TrimSpace(trimmed[end+1:])
	if text == "" {
		return ChecklistItem{}, fmt.Errorf("empty checklist text in %q", line)
	}
	return ChecklistItem{Marker: marker, Text: text}, nil
}

func formatHours(v float64) string {
	if v <= 0 {
		return "0"
	}
	r := roundHours(v)
	if math.Abs(r-math.Round(r)) < 0.05 {
		return strconv.FormatInt(int64(math.Round(r)), 10)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", r), "0"), ".")
}

func roundHours(v float64) float64 {
	return math.Round(v*10) / 10
}

func formatTokens(v int64) string {
	if v < 1000 {
		return strconv.FormatInt(v, 10)
	}
	if v < 1_000_000 {
		return trimDecimal(float64(v)/1000) + "k"
	}
	return trimDecimal(float64(v)/1_000_000) + "M"
}

func trimDecimal(v float64) string {
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.1f", v), "0"), ".")
}

func formatClock(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	out := strings.ToLower(t.Format("3:04pm"))
	return strings.TrimSuffix(out, "m")
}

func dedupePreserveOrder(items []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(items))
	for _, raw := range items {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func shortID(id string) string {
	if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

func FindExactTitleMatches(items []applebridge.Reminder, title string) []applebridge.Reminder {
	var out []applebridge.Reminder
	for _, item := range items {
		if item.Title == title {
			out = append(out, item)
		}
	}
	return out
}

func BuildCloudByUUID(reminders []*models.Reminder, listID string) map[string]*models.Reminder {
	out := map[string]*models.Reminder{}
	for _, r := range reminders {
		if r == nil {
			continue
		}
		if listID != "" && (r.ListRef == nil || *r.ListRef != listID) {
			continue
		}
		out[strings.ToUpper(r.ShortID())] = r
	}
	return out
}

func VerifyVisibleCount(items []applebridge.Reminder, title string, want int) error {
	matches := FindExactTitleMatches(items, title)
	if len(matches) != want {
		return fmt.Errorf("expected exactly %d visible reminder(s) for %q, got %d", want, title, len(matches))
	}
	return nil
}

func VerifySingleVisible(items []applebridge.Reminder, title string) error {
	return VerifyVisibleCount(items, title, 1)
}

func WaitForSingleVisible(fetch func() ([]applebridge.Reminder, error), title string, attempts int, delay time.Duration) ([]applebridge.Reminder, error) {
	return WaitForVisibleCount(fetch, title, 1, attempts, delay)
}

func WaitForVisibleCount(fetch func() ([]applebridge.Reminder, error), title string, want int, attempts int, delay time.Duration) ([]applebridge.Reminder, error) {
	if attempts < 1 {
		attempts = 1
	}
	var lastItems []applebridge.Reminder
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		items, err := fetch()
		if err != nil {
			lastErr = err
		} else {
			lastItems = items
			if verifyErr := VerifyVisibleCount(items, title, want); verifyErr == nil {
				return items, nil
			} else {
				lastErr = verifyErr
			}
		}
		if attempt < attempts-1 && delay > 0 {
			time.Sleep(delay)
		}
	}
	if lastErr != nil {
		return lastItems, lastErr
	}
	return lastItems, fmt.Errorf("expected exactly %d visible reminder(s) for %q", want, title)
}
