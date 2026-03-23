package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var createListCmd = &cobra.Command{
	Use:   "create-list <name>",
	Short: "Create a reminder list",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		result, err := w.CreateList(name)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		if existing, ok := result["existing"].(bool); ok && existing {
			fmt.Printf("ℹ️  List already exists: %s\n", name)
			return nil
		}
		fmt.Printf("✅ Created list: %s\n", name)
		return nil
	},
}
