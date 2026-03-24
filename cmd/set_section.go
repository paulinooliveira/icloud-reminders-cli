package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	setSectionList  string
	setSectionName  string
	setSectionClear bool
)

var setSectionCmd = &cobra.Command{
	Use:   "set-section <reminder-id>",
	Short: "Assign an existing reminder to an existing section",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if setSectionClear && cmd.Flags().Changed("section") {
			return fmt.Errorf("use either --section or --clear, not both")
		}
		if !setSectionClear && setSectionName == "" {
			return fmt.Errorf("--section or --clear is required")
		}
		if err := bestEffortSync(); err != nil && !shouldProceedWithoutSync(args[0]) {
			return err
		}

		var (
			result map[string]interface{}
			err    error
		)
		if setSectionClear {
			result, err = w.ClearReminderSection(args[0], setSectionList)
		} else {
			result, err = w.AssignReminderToSection(args[0], setSectionList, setSectionName)
		}
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}

		if setSectionClear {
			fmt.Printf("✅ Cleared section for: %s\n", args[0])
			return nil
		}
		fmt.Printf("✅ Section set: %s → %s\n", args[0], setSectionName)
		return nil
	},
}

func init() {
	setSectionCmd.Flags().StringVarP(&setSectionList, "list", "l", "", "List name or ID")
	setSectionCmd.Flags().StringVar(&setSectionName, "section", "", "Existing section name or ID")
	setSectionCmd.Flags().BoolVar(&setSectionClear, "clear", false, "Remove any section assignment")
}
