package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var inspectList string

var inspectCmd = &cobra.Command{
	Use:   "inspect <reminder-id-or-title>",
	Short: "Inspect a raw CloudKit reminder record",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := syncEngine.Sync(false); err != nil {
			return err
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

		listID := ""
		if inspectList != "" {
			listID = syncEngine.FindListByName(inspectList)
			if listID == "" {
				return fmt.Errorf("list %q not found", inspectList)
			}
		}

		recordName := resolveReminderRecordName(args[0], listID)
		if recordName == "" {
			return fmt.Errorf("reminder %q not found", args[0])
		}

		record, err := lookupSingleRecord(ownerID, recordName)
		if err != nil {
			return err
		}

		data, err := json.MarshalIndent(record, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(data))
		return nil
	},
}

func resolveReminderRecordName(hint string, listID string) string {
	if rid := syncEngine.FindReminderByID(hint); rid != "" {
		return rid
	}
	if rid := syncEngine.FindReminderByTitle(hint, listID, false); rid != "" {
		return rid
	}
	if rid := syncEngine.FindReminderByTitle(hint, "", false); rid != "" {
		return rid
	}
	return ""
}

func init() {
	inspectCmd.Flags().StringVarP(&inspectList, "list", "l", "", "Filter title lookup by list name or ID")
}
