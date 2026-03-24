package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	setTagsNames []string
	setTagsClear bool
)

var setTagsCmd = &cobra.Command{
	Use:   "set-tags <reminder-id-or-title>",
	Short: "Set native Apple Reminders tags on an existing reminder",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if setTagsClear && len(setTagsNames) > 0 {
			return fmt.Errorf("use either --tag or --clear, not both")
		}
		if !setTagsClear && len(setTagsNames) == 0 {
			return fmt.Errorf("--tag or --clear is required")
		}
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		tags := setTagsNames
		if setTagsClear {
			tags = nil
		}
		result, err := w.SetReminderTags(args[0], tags)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		if setTagsClear {
			fmt.Printf("✅ Cleared tags: %s\n", args[0])
			return nil
		}
		fmt.Printf("✅ Tags set: %s → %v\n", args[0], setTagsNames)
		return nil
	},
}

func init() {
	setTagsCmd.Flags().StringSliceVar(&setTagsNames, "tag", nil, "Native tag name(s), without the leading #")
	setTagsCmd.Flags().BoolVar(&setTagsClear, "clear", false, "Remove all native tags")
}
