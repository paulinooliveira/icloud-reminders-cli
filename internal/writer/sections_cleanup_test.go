package writer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"icloud-reminders/internal/cloudkit"
	"icloud-reminders/internal/sections"
)

func TestMembershipSectionInfosFromFileIncludesMembershipOnlySectionIDs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/database/1/com.apple.reminders/production/private/records/lookup" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"records": []map[string]interface{}{
				{
					"recordName": "ListSection/LIVE",
					"fields": map[string]interface{}{
						"DisplayName": map[string]interface{}{"value": "Finance Systems"},
					},
				},
				{
					"recordName":      "ListSection/GHOST",
					"serverErrorCode": "NOT_FOUND",
				},
			},
		})
	}))
	defer server.Close()

	ck := cloudkit.NewWithHTTPClient(server.URL, server.Client())
	membershipFile := &sections.MembershipFile{
		Memberships: []sections.Membership{
			{GroupID: "GHOST", MemberID: "r1"},
			{GroupID: "LIVE", MemberID: "r2"},
			{GroupID: "LIVE", MemberID: "r3"},
		},
	}

	infos, err := membershipSectionInfosFromFile(ck, "_owner", membershipFile)
	if err != nil {
		t.Fatalf("membershipSectionInfosFromFile: %v", err)
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 sections, got %#v", infos)
	}
	if infos[0].ID != "LIVE" || infos[0].Name != "Finance Systems" {
		t.Fatalf("unexpected first section: %#v", infos[0])
	}
	if infos[1].ID != "GHOST" || infos[1].Name != "GHOST" {
		t.Fatalf("unexpected fallback section: %#v", infos[1])
	}
}
