package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var reopenCmd = &cobra.Command{
	Use:     "reopen <id>",
	Aliases: []string{"todo"},
	Short:   "Mark a reminder as to-do again",
	Args:    cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		result, err := w.ReopenReminder(args[0])
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		fmt.Printf("✅ Reopened: %s\n", args[0])
		return nil
	},
}
