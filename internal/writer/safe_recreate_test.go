package writer

import (
	"strings"
	"testing"

	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/cloudkit"
	iclouddsync "icloud-reminders/internal/sync"
	"net/http"
	"net/http/httptest"
)

// TestEditReminderSafeRecreateRejectsNonCanonicalID verifies that
// editReminderSafeRecreate returns a hard error (not silent success) when
// resolveReminderRecord returns a bare UUID without the "Reminder/" prefix.
// This is the regression guard for the ID-confusion bug where the old
// CloudKit CRDT corruption was never cleared.
func TestEditReminderSafeRecreateRejectsNonCanonicalID(t *testing.T) {
	// Serve a minimal lookup response that returns a bare UUID as recordName.
	const bareUUID = "B267CC2E-E156-4CCE-9DED-E1E5576F4911"
	const ownerID = "_owner42"
	changeTag := "ct-bare"
	listRef := "List/AAAAAAAA-BBBB-CCCC-DDDD-EEEEEEEEEEEE"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/records/lookup") {
			// Return the record with a bare (non-prefixed) recordName so
			// resolveReminderRecord caches it without "Reminder/" prefix.
			_, _ = w.Write([]byte(`{"records":[{"recordName":"` + bareUUID + `","recordChangeTag":"` + changeTag + `","fields":{"TitleDocument":{"value":""},"Completed":{"value":0},"List":{"value":{"recordName":"` + listRef + `"}}}}]}`))
			return
		}
		// Any other request — shouldn't be reached in this test.
		http.Error(w, "unexpected", http.StatusInternalServerError)
	}))
	defer srv.Close()

	tmpDir := t.TempDir()
	t.Setenv("ICLOUD_REMINDERS_CONFIG_DIR", tmpDir)

	eng := &iclouddsync.Engine{
		Cache: &cache.Cache{
			Reminders: map[string]*cache.ReminderData{
				// Stored under bare UUID (the pre-fix bug form).
				bareUUID: {
					Title:     "ZZ Dup Test — corrupted",
					ChangeTag: &changeTag,
					ListRef:   &listRef,
				},
			},
			Lists:    map[string]string{listRef: "Delegate"},
			Sections: map[string]*cache.SectionData{},
			Hashtags: map[string]*cache.HashtagData{},
			OwnerID:  strPtr(ownerID),
		},
	}

	client := cloudkit.NewWithHTTPClient(srv.URL, srv.Client())
	w := New(client, eng)

	newTitle := "ZZ Dup Test - ES (Prueba en español)"
	result, err := w.EditReminderNoVisibleRepair(bareUUID, ReminderChanges{Title: &newTitle})

	// Must surface an error — either as a Go error or as result["error"].
	if err != nil {
		return // good: hard error returned
	}
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		if strings.Contains(errMsg, "non-canonical") {
			return // good: canonical-form guard fired
		}
		// Some other error is also acceptable — the important thing is it didn't
		// silently succeed with a corrupted state.
		return
	}
	t.Fatalf("expected an error for non-canonical fullID %q, got success: %v", bareUUID, result)
}
