package cmd

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

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

		assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
		if assetURL == "" {
			fmt.Printf("\n📚 %s\n", listName)
			fmt.Println("  No section metadata on this list.")
			return nil
		}

		rawAsset, err := ckClient.DownloadAsset(assetURL)
		if err != nil {
			return fmt.Errorf("download section memberships: %w", err)
		}
		membershipFile, err := sections.DecodeMembershipFile(rawAsset)
		if err != nil {
			return fmt.Errorf("decode section memberships: %w", err)
		}

		if len(membershipFile.Memberships) == 0 {
			fmt.Printf("\n📚 %s\n", listName)
			fmt.Println("  No reminders are currently assigned to sections.")
			return nil
		}

		sectionRecordNames := make([]string, 0, len(membershipFile.Memberships))
		reminderRecordNames := make([]string, 0, len(membershipFile.Memberships)*2)
		seenSections := make(map[string]struct{})
		seenReminders := make(map[string]struct{})
		seenReminderRecords := make(map[string]struct{})
		for _, membership := range membershipFile.Memberships {
			if _, ok := seenSections[membership.GroupID]; !ok {
				seenSections[membership.GroupID] = struct{}{}
				sectionRecordNames = append(sectionRecordNames, sections.ListSectionRecordName(membership.GroupID))
			}
			if _, ok := seenReminders[membership.MemberID]; !ok {
				seenReminders[membership.MemberID] = struct{}{}
				for _, candidate := range []string{membership.MemberID, sections.ReminderRecordName(membership.MemberID)} {
					if _, exists := seenReminderRecords[candidate]; exists {
						continue
					}
					seenReminderRecords[candidate] = struct{}{}
					reminderRecordNames = append(reminderRecordNames, candidate)
				}
			}
		}

		sectionRecords, err := ckClient.LookupRecords(ownerID, sectionRecordNames)
		if err != nil {
			return fmt.Errorf("lookup sections: %w", err)
		}
		reminderRecords, err := ckClient.LookupRecords(ownerID, reminderRecordNames)
		if err != nil {
			return fmt.Errorf("lookup section reminders: %w", err)
		}

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

		reminderTitles := make(map[string]string, len(reminderRecords))
		for _, record := range reminderRecords {
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

		ordered := sections.OrderedSections(sectionNames, membershipFile.Memberships)
		fmt.Printf("\n📚 %s (%d sections)\n", listName, len(ordered))
		for _, section := range ordered {
			title := section.Name
			if title == "" {
				title = section.ID
			}
			fmt.Printf("\n§ %s (%d)\n", title, len(section.Members))
			for _, memberID := range section.Members {
				fmt.Printf("  • %s  (%s)\n", reminderTitles[memberID], memberID)
			}
		}

		unsectioned := findUnsectionedReminderIDs(fields, seenReminders)
		if len(unsectioned) > 0 {
			fmt.Printf("\n§ No section (%d)\n", len(unsectioned))
			for _, reminderID := range unsectioned {
				title := reminderTitles[reminderID]
				if title == "" {
					title = reminderID
				}
				fmt.Printf("  • %s  (%s)\n", title, reminderID)
			}
		}
		return nil
	},
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
