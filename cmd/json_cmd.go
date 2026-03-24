package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/sections"
	"icloud-reminders/pkg/models"
)

var jsonListFilter string

var jsonCmd = &cobra.Command{
	Use:   "json",
	Short: "Output reminders as JSON",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		reminders := syncEngine.GetReminders(true)
		lists := syncEngine.GetLists()
		listID := ""
		if jsonListFilter != "" {
			listID = syncEngine.FindListByName(jsonListFilter)
		}
		sectionByReminder := map[string]struct {
			id   string
			name string
		}{}
		if jsonListFilter != "" {
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
			listRecordName, err := resolveListRecordName(ownerID, jsonListFilter)
			if err != nil {
				return err
			}
			listRecord, err := lookupSingleRecord(ownerID, listRecordName)
			if err != nil {
				return err
			}
			fields, _ := listRecord["fields"].(map[string]interface{})
			membershipFile, sectionRecords, err := loadListSections(ownerID, listRecordName, fields)
			if err != nil {
				return err
			}
			sectionNames := make(map[string]string, len(sectionRecords))
			for _, record := range sectionRecords {
				recordName, _ := record["recordName"].(string)
				recordFields, _ := record["fields"].(map[string]interface{})
				sectionID := shortID(recordName)
				name := getRecordString(recordFields, "DisplayName")
				if name == "" {
					name = getRecordString(recordFields, "CanonicalName")
				}
				if name == "" {
					name = sectionID
				}
				sectionNames[sectionID] = name
			}
			for _, membership := range membershipFile.Memberships {
				sectionByReminder[membership.MemberID] = struct {
					id   string
					name string
				}{
					id:   sections.ListSectionRecordName(membership.GroupID),
					name: sectionNames[membership.GroupID],
				}
			}
		}

		type output struct {
			Lists     []*models.ReminderList `json:"lists"`
			Active    []*models.Reminder     `json:"active"`
			Completed []*models.Reminder     `json:"completed"`
		}
		var active, completed []*models.Reminder
		for _, r := range reminders {
			if jsonListFilter != "" {
				match := toLowerStr(r.ListName) == toLowerStr(jsonListFilter)
				if !match && listID != "" && r.ListRef != nil && *r.ListRef == listID {
					match = true
				}
				if !match {
					continue
				}
			}
			section, ok := sectionByReminder[r.ID]
			if !ok {
				section, ok = sectionByReminder[shortReminderID(r.ID)]
			}
			if ok {
				r.SectionID = &section.id
				r.Section = &section.name
			}
			if r.Completed {
				completed = append(completed, r)
			} else {
				active = append(active, r)
			}
		}
		if jsonListFilter != "" {
			filtered := make([]*models.ReminderList, 0, len(lists))
			for _, list := range lists {
				if list == nil {
					continue
				}
				if toLowerStr(list.Name) == toLowerStr(jsonListFilter) || (listID != "" && list.ID == listID) {
					filtered = append(filtered, list)
				}
			}
			lists = filtered
		}

		out := output{
			Lists:     lists,
			Active:    active,
			Completed: completed,
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	},
}

func shortReminderID(id string) string {
	if idx := strings.LastIndexByte(id, '/'); idx >= 0 {
		return id[idx+1:]
	}
	return id
}

func init() {
	jsonCmd.Flags().StringVarP(&jsonListFilter, "list", "l", "", "Filter JSON output by list name or ID")
}
