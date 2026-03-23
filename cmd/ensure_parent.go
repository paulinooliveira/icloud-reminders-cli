package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	ensureParentList  string
	ensureParentNotes string
)

var ensureParentCmd = &cobra.Command{
	Use:   "ensure-parent <title>",
	Short: "Ensure a top-level reminder exists as a parent anchor",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		title := args[0]
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		result, err := w.EnsureParentReminder(title, ensureParentList, ensureParentNotes)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}

		if existing, _ := result["existing"].(bool); existing {
			fmt.Printf("ℹ️  Parent already exists: %s [%v]\n", title, result["id"])
			return nil
		}

		fmt.Printf("✅ Parent ready: '%s' → %s [%v]\n", title, ensureParentList, result["id"])
		return nil
	},
}

func init() {
	ensureParentCmd.Flags().StringVarP(&ensureParentList, "list", "l", "", "List name or ID (required)")
	ensureParentCmd.Flags().StringVarP(&ensureParentNotes, "notes", "n", "", "Notes")
	_ = ensureParentCmd.MarkFlagRequired("list")
}
