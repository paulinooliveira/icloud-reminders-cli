package sections

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

// MembershipFile is the serialized list-level section membership payload.
type MembershipFile struct {
	MinimumSupportedVersion int          `json:"minimumSupportedVersion"`
	Memberships             []Membership `json:"memberships"`
}

// Membership maps a reminder into a section.
type Membership struct {
	ModifiedOn float64 `json:"modifiedOn"`
	GroupID    string  `json:"groupID"`
	MemberID   string  `json:"memberID"`
}

// Section is a resolved section with display name and member reminders.
type Section struct {
	ID      string
	Name    string
	Members []string
}

// DecodeMembershipFile decodes Apple's section-membership asset.
func DecodeMembershipFile(raw []byte) (*MembershipFile, error) {
	decoded := raw
	if len(raw) >= 2 && raw[0] == 0x1f && raw[1] == 0x8b {
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return nil, fmt.Errorf("gzip reader: %w", err)
		}
		defer gz.Close()
		decoded, err = io.ReadAll(gz)
		if err != nil {
			return nil, fmt.Errorf("gzip read: %w", err)
		}
	}

	var mf MembershipFile
	if err := json.Unmarshal(decoded, &mf); err != nil {
		return nil, fmt.Errorf("membership json: %w", err)
	}
	return &mf, nil
}

// EncodeMembershipFile serializes the membership payload back to JSON.
func EncodeMembershipFile(mf *MembershipFile) ([]byte, error) {
	return json.Marshal(mf)
}

// ListSectionRecordName returns the CloudKit record name for a section.
func ListSectionRecordName(sectionID string) string {
	if strings.HasPrefix(sectionID, "ListSection/") {
		return sectionID
	}
	return "ListSection/" + sectionID
}

// ReminderRecordName returns the CloudKit record name for a reminder.
func ReminderRecordName(reminderID string) string {
	if strings.HasPrefix(reminderID, "Reminder/") {
		return reminderID
	}
	return "Reminder/" + reminderID
}

// OrderedSections builds stable section output from display names and memberships.
func OrderedSections(names map[string]string, memberships []Membership) []Section {
	memberSet := make(map[string]map[string]struct{})
	for _, membership := range memberships {
		if memberSet[membership.GroupID] == nil {
			memberSet[membership.GroupID] = make(map[string]struct{})
		}
		memberSet[membership.GroupID][membership.MemberID] = struct{}{}
	}

	groupIDs := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for groupID := range names {
		groupIDs = append(groupIDs, groupID)
		seen[groupID] = struct{}{}
	}
	for groupID := range memberSet {
		if _, ok := seen[groupID]; !ok {
			groupIDs = append(groupIDs, groupID)
		}
	}

	sort.Slice(groupIDs, func(i, j int) bool {
		ni := names[groupIDs[i]]
		nj := names[groupIDs[j]]
		if ni == nj {
			return groupIDs[i] < groupIDs[j]
		}
		if ni == "" {
			return false
		}
		if nj == "" {
			return true
		}
		return ni < nj
	})

	out := make([]Section, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		memberIDs := make([]string, 0, len(memberSet[groupID]))
		for memberID := range memberSet[groupID] {
			memberIDs = append(memberIDs, memberID)
		}
		sort.Strings(memberIDs)
		out = append(out, Section{
			ID:      groupID,
			Name:    names[groupID],
			Members: memberIDs,
		})
	}
	return out
}

// UpsertMemberships removes any previous section assignment for the reminder IDs
// and assigns them to the target section.
func UpsertMemberships(mf *MembershipFile, sectionID string, reminderIDs []string, modifiedOn float64) {
	if mf == nil {
		return
	}
	removeSet := make(map[string]struct{}, len(reminderIDs))
	for _, reminderID := range reminderIDs {
		removeSet[reminderID] = struct{}{}
	}

	filtered := make([]Membership, 0, len(mf.Memberships)+len(reminderIDs))
	for _, membership := range mf.Memberships {
		if _, ok := removeSet[membership.MemberID]; ok {
			continue
		}
		filtered = append(filtered, membership)
	}
	for _, reminderID := range reminderIDs {
		filtered = append(filtered, Membership{
			ModifiedOn: modifiedOn,
			GroupID:    sectionID,
			MemberID:   reminderID,
		})
	}
	mf.Memberships = filtered
}

// RemoveMemberships removes any section assignment for the reminder IDs.
func RemoveMemberships(mf *MembershipFile, reminderIDs []string) {
	if mf == nil {
		return
	}
	removeSet := make(map[string]struct{}, len(reminderIDs))
	for _, reminderID := range reminderIDs {
		removeSet[reminderID] = struct{}{}
	}

	filtered := make([]Membership, 0, len(mf.Memberships))
	for _, membership := range mf.Memberships {
		if _, ok := removeSet[membership.MemberID]; ok {
			continue
		}
		filtered = append(filtered, membership)
	}
	mf.Memberships = filtered
}

// GroupIDForMember returns the section/group id for a reminder if present.
func GroupIDForMember(mf *MembershipFile, reminderID string) string {
	if mf == nil {
		return ""
	}
	for _, membership := range mf.Memberships {
		if membership.MemberID == reminderID {
			return membership.GroupID
		}
	}
	return ""
}

// HasMembers reports whether a section still has at least one membership.
func HasMembers(mf *MembershipFile, sectionID string) bool {
	if mf == nil {
		return false
	}
	for _, membership := range mf.Memberships {
		if membership.GroupID == sectionID {
			return true
		}
	}
	return false
}

// RemoveSection removes all memberships for a given section ID and returns the
// removed member IDs.
func RemoveSection(mf *MembershipFile, sectionID string) []string {
	if mf == nil {
		return nil
	}
	filtered := make([]Membership, 0, len(mf.Memberships))
	var removed []string
	for _, membership := range mf.Memberships {
		if membership.GroupID == sectionID {
			removed = append(removed, membership.MemberID)
			continue
		}
		filtered = append(filtered, membership)
	}
	mf.Memberships = filtered
	sort.Strings(removed)
	return removed
}

// CanonicalName returns a stable kebab-case-like section identifier.
func CanonicalName(name string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return ""
	}

	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastDash = false
		case r == '&':
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
			}
			b.WriteString("and")
			b.WriteByte('-')
			lastDash = true
		default:
			if b.Len() > 0 && !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "section"
	}
	return out
}

// ResolutionTokenMapJSON creates the minimal CRDT metadata Apple expects on
// section records.
func ResolutionTokenMapJSON(nowMillis int64, replicaID string, includeCanonical bool) string {
	modificationTime := float64(nowMillis)/1000 - 978307200
	entry := func(counter int) map[string]interface{} {
		return map[string]interface{}{
			"modificationTime": modificationTime,
			"replicaID":        replicaID,
			"counter":          counter,
		}
	}

	m := map[string]interface{}{
		"minimumSupportedVersion": entry(1),
		"list":                    entry(1),
		"displayName":             entry(1),
		"creationDate":            entry(1),
	}
	if includeCanonical {
		m["canonicalName"] = entry(1)
	}

	payload := map[string]interface{}{
		"map": m,
	}
	raw, _ := json.Marshal(payload)
	return string(raw)
}

// DeterministicReplicaID returns a UUID-like uppercase identifier derived from
// the provided section ID. This avoids extra random bookkeeping in the CLI.
func DeterministicReplicaID(sectionID string) string {
	id := strings.ToUpper(strings.TrimPrefix(sectionID, "ListSection/"))
	if len(id) == 36 && strings.Count(id, "-") == 4 {
		return id
	}
	if len(id) >= 32 {
		return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
	}
	return "SECTION-" + strconv.FormatInt(int64(len(id)), 10)
}
