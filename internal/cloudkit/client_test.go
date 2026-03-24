package cloudkit

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestModifyRecordsReturnsHTTPError(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	client := NewWithHTTPClient(srv.URL, srv.Client())
	if _, err := client.ModifyRecords("owner", []map[string]interface{}{}); err == nil {
		t.Fatal("expected modify error")
	}
}
