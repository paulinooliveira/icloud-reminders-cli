package writer

import (
	"reflect"
	"testing"

	"icloud-reminders/internal/sections"
)

func TestUniqueMembershipMemberIDsNormalizesAndSorts(t *testing.T) {
	membershipFile := &sections.MembershipFile{
		Memberships: []sections.Membership{
			{GroupID: "S1", MemberID: "b-id"},
			{GroupID: "S1", MemberID: "A-id"},
			{GroupID: "S2", MemberID: "B-ID"},
			{GroupID: "S3", MemberID: " "},
		},
	}

	got := uniqueMembershipMemberIDs(membershipFile)
	want := []string{"A-ID", "B-ID"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("uniqueMembershipMemberIDs mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestPruneInactiveMembershipsRemovesStaleEntries(t *testing.T) {
	membershipFile := &sections.MembershipFile{
		Memberships: []sections.Membership{
			{GroupID: "finance", MemberID: "KEEP-1"},
			{GroupID: "finance", MemberID: "DROP-1"},
			{GroupID: "people", MemberID: "DROP-2"},
			{GroupID: "scheduled", MemberID: "KEEP-2"},
		},
	}

	changed := pruneInactiveMemberships(membershipFile, map[string]struct{}{
		"KEEP-1": {},
		"KEEP-2": {},
	})
	if !changed {
		t.Fatal("expected pruneInactiveMemberships to report a change")
	}

	got := membershipFile.Memberships
	want := []sections.Membership{
		{GroupID: "finance", MemberID: "KEEP-1"},
		{GroupID: "scheduled", MemberID: "KEEP-2"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("memberships mismatch after prune:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestPruneInactiveMembershipsNoopWhenAllMembersStayActive(t *testing.T) {
	membershipFile := &sections.MembershipFile{
		Memberships: []sections.Membership{
			{GroupID: "scheduled", MemberID: "KEEP-1"},
			{GroupID: "scheduled", MemberID: "KEEP-2"},
		},
	}

	changed := pruneInactiveMemberships(membershipFile, map[string]struct{}{
		"KEEP-1": {},
		"KEEP-2": {},
	})
	if changed {
		t.Fatal("expected pruneInactiveMemberships to be a no-op")
	}
}
