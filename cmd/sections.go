package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/sections"
	"icloud-reminders/internal/utils"
)

var sectionsList string

var sectionsCmd = &cobra.Command{
	Use:   "sections",
	Short: "Show live Reminders sections for a list",
	RunE: func(cmd *cobra.Command, args []string) error {
		if sectionsList == "" {
			return fmt.Errorf("--list is required")
		}

		ownerID := ""
		if syncEngine != nil && syncEngine.Cache != nil && syncEngine.Cache.OwnerID != nil && *syncEngine.Cache.OwnerID != "" {
			ownerID = *syncEngine.Cache.OwnerID
		}
		if ownerID == "" {
			var err error
			ownerID, err = ckClient.GetOwnerID()
			if err != nil {
				return err
			}
		}

		listRecordName, err := resolveListRecordName(ownerID, sectionsList)
		if err != nil {
			return err
		}

		listRecord, err := lookupSingleRecord(ownerID, listRecordName)
		if err != nil {
			return err
		}
		fields, _ := listRecord["fields"].(map[string]interface{})
		listName := getRecordString(fields, "Name")
		if listName == "" {
			listName = shortID(listRecordName)
		}

		membershipFile, sectionRecords, err := loadListSections(ownerID, listRecordName, fields)
		if err != nil {
			return err
		}

		seenReminders := make(map[string]struct{})
		memberIDs := make([]string, 0, len(membershipFile.Memberships))
		for _, membership := range membershipFile.Memberships {
			if _, ok := seenReminders[membership.MemberID]; ok {
				continue
			}
			seenReminders[membership.MemberID] = struct{}{}
			memberIDs = append(memberIDs, membership.MemberID)
		}

		reminderTitles, err := lookupReminderTitles(ownerID, memberIDs)
		if err != nil {
			return fmt.Errorf("lookup section reminders: %w", err)
		}
		visibleAppReminderIDs := liveVisibleReminderIDs(listName)

		sectionNames := make(map[string]string, len(sectionRecords))
		for _, record := range sectionRecords {
			recordName, _ := record["recordName"].(string)
			if code, _ := record["serverErrorCode"].(string); code != "" {
				sectionNames[shortID(recordName)] = shortID(recordName)
				continue
			}
			fields, _ := record["fields"].(map[string]interface{})
			sectionID := shortID(recordName)
			name := getRecordString(fields, "DisplayName")
			if name == "" {
				name = getRecordString(fields, "CanonicalName")
			}
			if name == "" {
				name = sectionID
			}
			sectionNames[sectionID] = name
		}

		ordered := sections.OrderedSections(sectionNames, membershipFile.Memberships)
		visibleSections := 0
		for _, section := range ordered {
			if sectionVisibleCount(section, reminderTitles, visibleAppReminderIDs) > 0 {
				visibleSections++
			}
		}
		if visibleSections == 0 {
			fmt.Printf("\n📚 %s\n", listName)
			fmt.Println("  No sections on this list.")
		} else {
			fmt.Printf("\n📚 %s (%d sections)\n", listName, visibleSections)
			for _, section := range ordered {
				visibleMembers := visibleSectionMembers(section, reminderTitles, visibleAppReminderIDs)
				if len(visibleMembers) == 0 {
					continue
				}
				title := section.Name
				if title == "" {
					title = section.ID
				}
				fmt.Printf("\n§ %s (%d)\n", title, len(visibleMembers))
				for _, memberID := range visibleMembers {
					fmt.Printf("  • %s  (%s)\n", reminderTitles[memberID], memberID)
				}
			}
		}

		unsectioned := findUnsectionedReminderIDs(fields, seenReminders)
		if len(unsectioned) > 0 {
			unsectionedTitles, err := lookupReminderTitles(ownerID, unsectioned)
			if err != nil {
				return fmt.Errorf("lookup unsectioned reminders: %w", err)
			}
			visible := make([]string, 0, len(unsectioned))
			for _, reminderID := range unsectioned {
				if isVisibleReminder(reminderID, reminderTitles[reminderID], visibleAppReminderIDs) || isVisibleReminder(reminderID, unsectionedTitles[reminderID], visibleAppReminderIDs) {
					visible = append(visible, reminderID)
				}
			}
			if len(visible) > 0 {
				fmt.Printf("\n§ No section (%d)\n", len(visible))
			}
			for _, reminderID := range visible {
				title := reminderTitles[reminderID]
				if title == "" {
					title = unsectionedTitles[reminderID]
				}
				if title == "" {
					title = reminderID
				}
				fmt.Printf("  • %s  (%s)\n", title, reminderID)
			}
		}
		return nil
	},
}

func lookupReminderTitles(ownerID string, reminderIDs []string) (map[string]string, error) {
	reminderRecordNames := make([]string, 0, len(reminderIDs)*2)
	seenReminderRecords := make(map[string]struct{})
	for _, reminderID := range reminderIDs {
		for _, candidate := range []string{reminderID, sections.ReminderRecordName(reminderID)} {
			if _, exists := seenReminderRecords[candidate]; exists {
				continue
			}
			seenReminderRecords[candidate] = struct{}{}
			reminderRecordNames = append(reminderRecordNames, candidate)
		}
	}
	if len(reminderRecordNames) == 0 {
		return map[string]string{}, nil
	}

	records, err := ckClient.LookupRecords(ownerID, reminderRecordNames)
	if err != nil {
		return nil, err
	}
	reminderTitles := make(map[string]string, len(records))
	for _, record := range records {
		recordName, _ := record["recordName"].(string)
		short := shortID(recordName)
		if code, _ := record["serverErrorCode"].(string); code != "" {
			if _, exists := reminderTitles[short]; !exists {
				reminderTitles[short] = "(missing reminder)"
			}
			continue
		}
		fields, _ := record["fields"].(map[string]interface{})
		title := utils.ExtractTitle(getRecordString(fields, "TitleDocument"))
		if title == "" {
			title = short
		}
		reminderTitles[short] = title
	}
	return reminderTitles, nil
}

func visibleSectionMembers(section sections.Section, reminderTitles map[string]string, visibleAppReminderIDs map[string]struct{}) []string {
	out := make([]string, 0, len(section.Members))
	for _, memberID := range section.Members {
		if isVisibleReminder(memberID, reminderTitles[memberID], visibleAppReminderIDs) {
			out = append(out, memberID)
		}
	}
	return out
}

func sectionVisibleCount(section sections.Section, reminderTitles map[string]string, visibleAppReminderIDs map[string]struct{}) int {
	return len(visibleSectionMembers(section, reminderTitles, visibleAppReminderIDs))
}

func isVisibleReminder(reminderID, title string, visibleAppReminderIDs map[string]struct{}) bool {
	title = strings.TrimSpace(title)
	if title == "" || title == "(missing reminder)" {
		return false
	}
	if len(visibleAppReminderIDs) == 0 {
		return true
	}
	_, ok := visibleAppReminderIDs[strings.ToUpper(shortID(reminderID))]
	return ok
}

func liveVisibleReminderIDs(listName string) map[string]struct{} {
	cfg, err := applebridge.LoadConfig()
	if err != nil {
		return nil
	}
	bridge := applebridge.New(cfg)
	items, err := bridge.ListReminders(listName)
	if err != nil {
		return nil
	}
	out := make(map[string]struct{}, len(items))
	for _, item := range items {
		out[strings.ToUpper(item.UUID())] = struct{}{}
	}
	return out
}

func resolveListRecordName(ownerID, hint string) (string, error) {
	if strings.HasPrefix(hint, "List/") {
		return hint, nil
	}
	if syncEngine != nil {
		if cached := syncEngine.FindListByName(hint); cached != "" {
			return cached, nil
		}
	}
	if looksLikeUUID(hint) {
		return "List/" + hint, nil
	}
	if live := findListByNameLive(ownerID, hint); live != "" {
		return live, nil
	}
	return "", fmt.Errorf("list %q not found; pass a list ID if needed", hint)
}

func findListByNameLive(ownerID, name string) string {
	target := toLowerStr(name)
	token := ""
	const maxPages = 12
	for page := 1; page <= maxPages; page++ {
		data, err := ckClient.ChangesZone(ownerID, token)
		if err != nil {
			return ""
		}
		zones, _ := data["zones"].([]interface{})
		if len(zones) == 0 {
			return ""
		}
		zone, _ := zones[0].(map[string]interface{})
		records, _ := zone["records"].([]interface{})
		for _, raw := range records {
			record, _ := raw.(map[string]interface{})
			recordName, _ := record["recordName"].(string)
			recordType, _ := record["recordType"].(string)
			if recordType != "List" && recordType != "ReminderList" {
				continue
			}
			fields, _ := record["fields"].(map[string]interface{})
			listName := getRecordString(fields, "Name")
			if listName == "" {
				listName = utils.ExtractTitle(getRecordString(fields, "TitleDocument"))
			}
			if toLowerStr(listName) == target || toLowerStr(shortID(recordName)) == target {
				return recordName
			}
		}
		moreComing, _ := zone["moreComing"].(bool)
		if !moreComing {
			return ""
		}
		token, _ = zone["syncToken"].(string)
		if token == "" {
			return ""
		}
	}
	return ""
}

func loadListSections(ownerID, listRecordName string, fields map[string]interface{}) (*sections.MembershipFile, []map[string]interface{}, error) {
	membershipFile := &sections.MembershipFile{}
	assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
	if assetURL != "" {
		rawAsset, err := ckClient.DownloadAsset(assetURL)
		if err != nil {
			return nil, nil, fmt.Errorf("download section memberships: %w", err)
		}
		decoded, err := sections.DecodeMembershipFile(rawAsset)
		if err != nil {
			return nil, nil, fmt.Errorf("decode section memberships: %w", err)
		}
		membershipFile = decoded
	}

	sectionRecordNames := make(map[string]struct{})
	for _, membership := range membershipFile.Memberships {
		sectionRecordNames[sections.ListSectionRecordName(membership.GroupID)] = struct{}{}
	}
	if syncEngine != nil && syncEngine.Cache != nil {
		for recordName, section := range syncEngine.Cache.Sections {
			if section == nil || section.ListRef == nil || *section.ListRef != listRecordName {
				continue
			}
			sectionRecordNames[recordName] = struct{}{}
		}
	}

	token := ""
	const maxPages = 16
	for page := 1; page <= maxPages; page++ {
		data, err := ckClient.ChangesZone(ownerID, token)
		if err != nil {
			return nil, nil, err
		}
		zones, _ := data["zones"].([]interface{})
		if len(zones) == 0 {
			break
		}
		zone, _ := zones[0].(map[string]interface{})
		records, _ := zone["records"].([]interface{})
		for _, raw := range records {
			record, _ := raw.(map[string]interface{})
			recordType, _ := record["recordType"].(string)
			if recordType != "ListSection" {
				continue
			}
			if deleted, _ := record["deleted"].(bool); deleted {
				continue
			}
			recordFields, _ := record["fields"].(map[string]interface{})
			if getReferenceRecordName(recordFields, "List") != listRecordName {
				continue
			}
			recordName, _ := record["recordName"].(string)
			if recordName != "" {
				sectionRecordNames[recordName] = struct{}{}
			}
		}
		moreComing, _ := zone["moreComing"].(bool)
		if !moreComing {
			break
		}
		token, _ = zone["syncToken"].(string)
		if token == "" {
			break
		}
	}

	recordNames := make([]string, 0, len(sectionRecordNames))
	for recordName := range sectionRecordNames {
		recordNames = append(recordNames, recordName)
	}
	sort.Strings(recordNames)

	if len(recordNames) == 0 {
		return membershipFile, nil, nil
	}

	records, err := ckClient.LookupRecords(ownerID, recordNames)
	if err != nil {
		return nil, nil, fmt.Errorf("lookup sections: %w", err)
	}
	return membershipFile, records, nil
}

func lookupSingleRecord(ownerID, recordName string) (map[string]interface{}, error) {
	records, err := ckClient.LookupRecords(ownerID, []string{recordName})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("record %s not found", recordName)
	}
	if code, _ := records[0]["serverErrorCode"].(string); code != "" {
		reason, _ := records[0]["reason"].(string)
		return nil, fmt.Errorf("record %s lookup failed: %s", recordName, reason)
	}
	return records[0], nil
}

func getRecordString(fields map[string]interface{}, key string) string {
	field, _ := fields[key].(map[string]interface{})
	val, _ := field["value"].(string)
	return val
}

func getAssetDownloadURL(fields map[string]interface{}, key string) string {
	field, _ := fields[key].(map[string]interface{})
	val, _ := field["value"].(map[string]interface{})
	url, _ := val["downloadURL"].(string)
	return url
}

func getReferenceRecordName(fields map[string]interface{}, key string) string {
	field, _ := fields[key].(map[string]interface{})
	val, _ := field["value"].(map[string]interface{})
	recordName, _ := val["recordName"].(string)
	return recordName
}

func findUnsectionedReminderIDs(fields map[string]interface{}, seenReminders map[string]struct{}) []string {
	raw := getRecordString(fields, "ReminderIDs")
	if raw == "" {
		return nil
	}

	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}

	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if _, ok := seenReminders[id]; !ok {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func init() {
	sectionsCmd.Flags().StringVarP(&sectionsList, "list", "l", "", "List name or ID")
}
