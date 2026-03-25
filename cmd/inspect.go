package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect <reminder-id>",
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

		recordName := resolveReminderRecordName(args[0])
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

func resolveReminderRecordName(hint string) string {
	return syncEngine.FindReminderByID(hint)
}

func init() {
	// Intentionally ID-only resolution; title-based lookup is disabled by design.
}
