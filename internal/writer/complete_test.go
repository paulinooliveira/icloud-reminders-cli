package writer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/cloudkit"
	iclouddsync "icloud-reminders/internal/sync"
)

func TestCompleteReminderUsesIntegerCompletionFields(t *testing.T) {
	server := newCompleteTestServer(t)
	defer server.Close()

	w, reminderID, cleanup := newCompleteTestWriter(t, server)
	defer cleanup()

	result, err := w.CompleteReminder(reminderID)
	if err != nil {
		t.Fatalf("CompleteReminder error: %v", err)
	}
	if errMsg, ok := result["error"].(string); ok {
		t.Fatalf("unexpected result error: %s", errMsg)
	}
	if server.modifyCalls() != 1 {
		t.Fatalf("modify call count mismatch: got %d want 1", server.modifyCalls())
	}
	if got := normalizeNumber(server.completedValue()); got != 1 {
		t.Fatalf("completed payload mismatch: got %#v want 1", server.completedValue())
	}
	if got := server.completionDateValue(); got == nil {
		t.Fatal("expected completion date payload")
	}
	rd := w.Sync.Cache.Reminders[reminderID]
	if rd == nil || !rd.Completed {
		t.Fatalf("expected cache reminder to be completed, got %#v", rd)
	}
	if rd.CompletionDate == nil || strings.TrimSpace(*rd.CompletionDate) == "" {
		t.Fatalf("expected completion date in cache, got %#v", rd.CompletionDate)
	}
}

type completeTestServer struct {
	t                   *testing.T
	srv                 *httptest.Server
	mu                  sync.Mutex
	modifyCount         int
	recordName          string
	recordTag           string
	ownerID             string
	lastCompletedValue  interface{}
	lastCompletionValue interface{}
}

func newCompleteTestServer(t *testing.T) *completeTestServer {
	cts := &completeTestServer{
		t:          t,
		recordName: "Reminder/22222222-2222-2222-2222-222222222222",
		recordTag:  "ct-complete-1",
		ownerID:    "_owner",
	}
	cts.srv = httptest.NewServer(http.HandlerFunc(cts.handle))
	return cts
}

func (c *completeTestServer) Close() {
	c.srv.Close()
}

func (c *completeTestServer) URL() string {
	return c.srv.URL
}

func (c *completeTestServer) modifyCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.modifyCount
}

func (c *completeTestServer) completedValue() interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCompletedValue
}

func (c *completeTestServer) completionDateValue() interface{} {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastCompletionValue
}

func (c *completeTestServer) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case strings.HasSuffix(r.URL.Path, "/records/lookup"):
		writeCompleteTestJSON(c.t, w, map[string]interface{}{
			"records": []interface{}{
				map[string]interface{}{
					"recordName":      c.recordName,
					"recordType":      "Reminder",
					"recordChangeTag": c.recordTag,
					"fields": map[string]interface{}{
						"TitleDocument": map[string]interface{}{"value": "Complete me"},
						"Completed":     map[string]interface{}{"value": int64(0)},
					},
				},
			},
		})
	case strings.HasSuffix(r.URL.Path, "/records/modify"):
		var payload struct {
			Operations []struct {
				Record struct {
					RecordName string                 `json:"recordName"`
					Fields     map[string]interface{} `json:"fields"`
				} `json:"record"`
			} `json:"operations"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			c.t.Fatalf("decode modify payload: %v", err)
		}
		if len(payload.Operations) == 0 {
			c.t.Fatal("expected modify operation")
		}
		fields := payload.Operations[0].Record.Fields
		completedField, ok := fields["Completed"].(map[string]interface{})
		if !ok {
			c.t.Fatalf("expected Completed field, got %#v", fields["Completed"])
		}
		completionField, ok := fields["CompletionDate"].(map[string]interface{})
		if !ok {
			c.t.Fatalf("expected CompletionDate field, got %#v", fields["CompletionDate"])
		}
		c.mu.Lock()
		c.modifyCount++
		c.lastCompletedValue = completedField["value"]
		c.lastCompletionValue = completionField["value"]
		c.mu.Unlock()
		writeCompleteTestJSON(c.t, w, map[string]interface{}{
			"records": []interface{}{
				map[string]interface{}{
					"recordName":      c.recordName,
					"recordChangeTag": "ct-complete-2",
				},
			},
		})
	default:
		c.t.Fatalf("unexpected request path: %s", r.URL.Path)
	}
}

func writeCompleteTestJSON(t *testing.T, w http.ResponseWriter, payload map[string]interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func normalizeNumber(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
}

func newCompleteTestWriter(t *testing.T, server *completeTestServer) (*Writer, string, func()) {
	t.Helper()

	tmpDir := t.TempDir()
	t.Setenv("ICLOUD_REMINDERS_CONFIG_DIR", tmpDir)

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
	title := "Complete me"
	engine.Cache.Reminders[server.recordName] = &cache.ReminderData{
		Title:     title,
		ChangeTag: &changeTag,
	}
	engine.Cache.Reminders[strings.TrimPrefix(server.recordName, "Reminder/")] = &cache.ReminderData{}

	client := cloudkit.NewWithHTTPClient(server.URL(), server.srv.Client())
	return New(client, engine), server.recordName, func() {}
}
