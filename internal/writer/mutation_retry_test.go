package writer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"icloud-reminders/internal/cloudkit"
)

func TestModifyRecordsWithRetryRetriesOnLockFailure(t *testing.T) {
	server := newModifyRetryTestServer(t)
	defer server.Close()

	client := cloudkit.NewWithHTTPClient(server.URL(), server.srv.Client())
	w := &Writer{CK: client}
	result, err := w.modifyRecordsWithRetry("owner", []map[string]interface{}{
		{
			"operationType": "noop",
		},
	})
	if err != nil {
		t.Fatalf("modifyRecordsWithRetry error: %v", err)
	}
	if result == nil {
		t.Fatal("expected a result map, got nil")
	}
	if server.modifyCalls() < 2 {
		t.Fatalf("expected retry to occur, got %d calls", server.modifyCalls())
	}
}

type modifyRetryTestServer struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	calls  int
	owner  string
	record string
}

func newModifyRetryTestServer(t *testing.T) *modifyRetryTestServer {
	s := &modifyRetryTestServer{
		t:      t,
		owner:  "owner",
		record: "Reminder/11111111-1111-1111-1111-111111111111",
	}
	s.srv = httptest.NewServer(http.HandlerFunc(s.handle))
	return s
}

func (s *modifyRetryTestServer) Close() {
	s.srv.Close()
}

func (s *modifyRetryTestServer) URL() string {
	return s.srv.URL
}

func (s *modifyRetryTestServer) modifyCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func (s *modifyRetryTestServer) handle(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/records/modify") {
		s.t.Fatalf("unexpected request path: %s", r.URL.Path)
	}

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		s.t.Fatalf("decode payload: %v", err)
	}

	s.mu.Lock()
	s.calls++
	call := s.calls
	s.mu.Unlock()

	switch call {
	case 1:
		writeModifyTestJSON(s.t, w, map[string]interface{}{
			"records": []interface{}{
				map[string]interface{}{
					"serverErrorCode": "OP_LOCK_FAILURE",
					"reason":          "simulate lock",
				},
			},
		})
	default:
		writeModifyTestJSON(s.t, w, map[string]interface{}{
			"records": []interface{}{
				map[string]interface{}{
					"recordName":      s.record,
					"recordChangeTag": "ct-1",
				},
			},
		})
	}
}

func writeModifyTestJSON(t *testing.T, w http.ResponseWriter, payload map[string]interface{}) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}
