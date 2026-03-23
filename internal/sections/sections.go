package sections

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
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
