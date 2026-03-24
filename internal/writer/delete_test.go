package writer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/cloudkit"
	iclouddsync "icloud-reminders/internal/sync"
)

func TestDeleteReminderRemovesAliasesOnlyAfterVerifiedDelete(t *testing.T) {
	server := newDeleteTestServer(t, deleteTestBehavior{})
	defer server.Close()

	w, deleteHint, reminderID, cleanup := newDeleteTestWriter(t, server)
	defer cleanup()

	oldBackoff := deleteVerifyBackoff
	deleteVerifyBackoff = []time.Duration{0}
	defer func() { deleteVerifyBackoff = oldBackoff }()

	result, err := w.DeleteReminder(deleteHint)
	if err != nil {
		t.Fatalf("DeleteReminder error: %v", err)
	}
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("unexpected delete error result: %s", errMsg)
	}
	if server.modifyCalls() != 1 {
		t.Fatalf("modify call count mismatch: got %d want 1", server.modifyCalls())
	}
	if server.lookupCalls() < 2 {
		t.Fatalf("expected initial lookup plus verification, got %d lookups", server.lookupCalls())
	}
	for _, alias := range cache.ReminderAliases(reminderID) {
		if _, ok := w.Sync.Cache.Reminders[alias]; ok {
			t.Fatalf("expected alias %q to be removed from cache", alias)
		}
	}
}

func TestDeleteReminderFailsWhenRecordStillExistsAfterDelete(t *testing.T) {
	server := newDeleteTestServer(t, deleteTestBehavior{verifyStillPresent: true})
	defer server.Close()

	w, deleteHint, reminderID, cleanup := newDeleteTestWriter(t, server)
	defer cleanup()

	oldBackoff := deleteVerifyBackoff
	deleteVerifyBackoff = []time.Duration{0, 0}
	defer func() { deleteVerifyBackoff = oldBackoff }()

	result, err := w.DeleteReminder(deleteHint)
	if err != nil {
		t.Fatalf("DeleteReminder error: %v", err)
	}
	errMsg, _ := result["error"].(string)
	if !strings.Contains(errMsg, "delete verification failed") {
		t.Fatalf("expected verification failure, got %#v", result)
	}
	if _, ok := w.Sync.Cache.Reminders[reminderID]; !ok {
		t.Fatal("expected reminder to remain cached on failed delete")
	}
}

func TestDeleteReminderFailsWhenVerificationLookupFails(t *testing.T) {
	server := newDeleteTestServer(t, deleteTestBehavior{verifyLookupStatus: http.StatusServiceUnavailable})
	defer server.Close()

	w, deleteHint, _, cleanup := newDeleteTestWriter(t, server)
	defer cleanup()

	oldBackoff := deleteVerifyBackoff
	deleteVerifyBackoff = []time.Duration{0}
	defer func() { deleteVerifyBackoff = oldBackoff }()

	result, err := w.DeleteReminder(deleteHint)
	if err != nil {
		t.Fatalf("DeleteReminder error: %v", err)
	}
	errMsg, _ := result["error"].(string)
	if !strings.Contains(errMsg, "delete verification lookup failed") {
		t.Fatalf("expected verification lookup failure, got %#v", result)
	}
}

func TestDeleteReminderCanonicalizesShortIDToFullRecordName(t *testing.T) {
	server := newDeleteTestServer(t, deleteTestBehavior{})
	defer server.Close()

	w, _, reminderID, cleanup := newDeleteTestWriter(t, server)
	defer cleanup()

	shortID := strings.TrimPrefix(reminderID, "Reminder/")
	delete(w.Sync.Cache.Reminders, reminderID)
	w.Sync.Cache.Reminders[shortID] = &cache.ReminderData{
		Title:     "Audit finance statement coverage and parsing",
		ChangeTag: strPtr(server.recordTag),
	}

	oldBackoff := deleteVerifyBackoff
	deleteVerifyBackoff = []time.Duration{0}
	defer func() { deleteVerifyBackoff = oldBackoff }()

	result, err := w.DeleteReminder(shortID)
	if err != nil {
		t.Fatalf("DeleteReminder error: %v", err)
	}
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("unexpected delete error result: %s", errMsg)
	}
	if got := server.lastModifiedRecordName(); got != reminderID {
		t.Fatalf("delete used wrong record name: got %q want %q", got, reminderID)
	}
}

type deleteTestBehavior struct {
	verifyStillPresent bool
	verifyLookupStatus int
}

type deleteTestServer struct {
	t            *testing.T
	srv          *httptest.Server
	mu           sync.Mutex
	lookupCount  int
	modifyCount  int
	lastModified string
	behavior     deleteTestBehavior
	reminderID   string
	recordTag    string
	ownerID      string
}

func newDeleteTestServer(t *testing.T, behavior deleteTestBehavior) *deleteTestServer {
	dts := &deleteTestServer{
		t:          t,
		behavior:   behavior,
		reminderID: "Reminder/11111111-1111-1111-1111-111111111111",
		recordTag:  "ct-delete-1",
		ownerID:    "_owner",
	}
	dts.srv = httptest.NewServer(http.HandlerFunc(dts.handle))
	return dts
}

func (d *deleteTestServer) Close() {
	d.srv.Close()
}

func (d *deleteTestServer) URL() string {
	return d.srv.URL
}

func (d *deleteTestServer) lookupCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lookupCount
}

func (d *deleteTestServer) modifyCalls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.modifyCount
}

func (d *deleteTestServer) lastModifiedRecordName() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.lastModified
}

func (d *deleteTestServer) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/records/lookup"):
		d.handleLookup(w, r)
	case strings.HasSuffix(r.URL.Path, "/records/modify"):
		d.handleModify(w, r)
	default:
		d.t.Fatalf("unexpected request path: %s", r.URL.Path)
	}
}

func (d *deleteTestServer) handleLookup(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	d.lookupCount++
	modifyCount := d.modifyCount
	d.mu.Unlock()

	if modifyCount > 0 && d.behavior.verifyLookupStatus != 0 {
		http.Error(w, "lookup failed", d.behavior.verifyLookupStatus)
		return
	}

	record := map[string]interface{}{
		"recordName":      d.reminderID,
		"recordType":      "Reminder",
		"recordChangeTag": d.recordTag,
		"fields": map[string]interface{}{
			"TitleDocument": map[string]interface{}{
				"value": "Audit finance statement coverage and parsing",
			},
		},
	}

	resp := map[string]interface{}{"records": []interface{}{}}
	if modifyCount == 0 || d.behavior.verifyStillPresent {
		resp["records"] = []interface{}{record}
	}
	writeDeleteTestJSON(d.t, w, resp)
}

func (d *deleteTestServer) handleModify(w http.ResponseWriter, r *http.Request) {
	var payload struct {
		Operations []struct {
			Record struct {
				RecordName string `json:"recordName"`
			} `json:"record"`
		} `json:"operations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		d.t.Fatalf("decode modify payload: %v", err)
	}

	d.mu.Lock()
	d.modifyCount++
	if len(payload.Operations) > 0 {
		d.lastModified = payload.Operations[0].Record.RecordName
	}
	d.mu.Unlock()
	writeDeleteTestJSON(d.t, w, map[string]interface{}{
		"records": []interface{}{
			map[string]interface{}{
				"recordName": d.reminderID,
			},
		},
	})
}

func writeDeleteTestJSON(t *testing.T, w http.ResponseWriter, payload map[string]interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func newDeleteTestWriter(t *testing.T, server *deleteTestServer) (*Writer, string, string, func()) {
	t.Helper()

	origDir := cache.ConfigDir
	origFile := cache.CacheFile
	tmpDir := t.TempDir()
	cache.ConfigDir = tmpDir
	cache.CacheFile = filepath.Join(tmpDir, "ck_cache.json")

	restore := func() {
		cache.ConfigDir = origDir
		cache.CacheFile = origFile
	}

	engine := &iclouddsync.Engine{
		Cache: &cache.Cache{
			Reminders: map[string]*cache.ReminderData{},
			Sections:  map[string]*cache.SectionData{},
			Hashtags:  map[string]*cache.HashtagData{},
			Lists:     map[string]string{},
			OwnerID:   strPtr(server.ownerID),
		},
	}
	changeTag := server.recordTag
	rd := &cache.ReminderData{
		Title:     "Audit finance statement coverage and parsing",
		ChangeTag: &changeTag,
	}
	engine.Cache.Reminders[server.reminderID] = rd
	engine.Cache.Reminders[strings.TrimPrefix(server.reminderID, "Reminder/")] = &cache.ReminderData{}

	client := cloudkit.NewWithHTTPClient(server.URL(), server.srv.Client())
	return New(client, engine), rd.Title, server.reminderID, restore
}
