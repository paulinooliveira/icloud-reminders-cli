package sections

import (
	"bytes"
	"compress/gzip"
	"testing"
)

func TestDecodeMembershipFilePlainJSON(t *testing.T) {
	raw := []byte(`{"minimumSupportedVersion":20230430,"memberships":[{"modifiedOn":1.5,"groupID":"G1","memberID":"R1"}]}`)
	got, err := DecodeMembershipFile(raw)
	if err != nil {
		t.Fatalf("DecodeMembershipFile: %v", err)
	}
	if got.MinimumSupportedVersion != 20230430 {
		t.Fatalf("minimumSupportedVersion mismatch: %d", got.MinimumSupportedVersion)
	}
	if len(got.Memberships) != 1 || got.Memberships[0].GroupID != "G1" || got.Memberships[0].MemberID != "R1" {
		t.Fatalf("unexpected memberships: %#v", got.Memberships)
	}
}

func TestDecodeMembershipFileGzipJSON(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(`{"minimumSupportedVersion":20230430,"memberships":[{"modifiedOn":2,"groupID":"G2","memberID":"R2"}]}`)); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	got, err := DecodeMembershipFile(buf.Bytes())
	if err != nil {
		t.Fatalf("DecodeMembershipFile: %v", err)
	}
	if len(got.Memberships) != 1 || got.Memberships[0].GroupID != "G2" || got.Memberships[0].MemberID != "R2" {
		t.Fatalf("unexpected memberships: %#v", got.Memberships)
	}
}

func TestOrderedSections(t *testing.T) {
	names := map[string]string{
		"b": "Beta",
		"a": "Alpha",
	}
	got := OrderedSections(names, []Membership{
		{GroupID: "b", MemberID: "r2"},
		{GroupID: "a", MemberID: "r1"},
		{GroupID: "b", MemberID: "r3"},
		{GroupID: "b", MemberID: "r2"},
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 sections, got %d", len(got))
	}
	if got[0].ID != "a" || got[0].Name != "Alpha" {
		t.Fatalf("first section mismatch: %#v", got[0])
	}
	if got[1].ID != "b" || len(got[1].Members) != 2 {
		t.Fatalf("second section mismatch: %#v", got[1])
	}
}

func TestUpsertMemberships(t *testing.T) {
	mf := &MembershipFile{
		Memberships: []Membership{
			{GroupID: "old", MemberID: "r1"},
			{GroupID: "keep", MemberID: "r2"},
		},
	}
	UpsertMemberships(mf, "new", []string{"r1", "r3"}, 7.5)

	if len(mf.Memberships) != 3 {
		t.Fatalf("unexpected membership count: %d", len(mf.Memberships))
	}
	if mf.Memberships[0].GroupID != "keep" || mf.Memberships[0].MemberID != "r2" {
		t.Fatalf("expected keep membership first, got %#v", mf.Memberships[0])
	}
	if mf.Memberships[1].GroupID != "new" || mf.Memberships[1].MemberID != "r1" {
		t.Fatalf("expected reassigned r1, got %#v", mf.Memberships[1])
	}
	if mf.Memberships[2].GroupID != "new" || mf.Memberships[2].MemberID != "r3" {
		t.Fatalf("expected new r3, got %#v", mf.Memberships[2])
	}
}

func TestRemoveMemberships(t *testing.T) {
	mf := &MembershipFile{
		Memberships: []Membership{
			{GroupID: "a", MemberID: "r1"},
			{GroupID: "b", MemberID: "r2"},
		},
	}
	RemoveMemberships(mf, []string{"r1"})
	if len(mf.Memberships) != 1 || mf.Memberships[0].MemberID != "r2" {
		t.Fatalf("unexpected memberships after remove: %#v", mf.Memberships)
	}
}
