package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/reminders"
)

var mcpAuthorizeCmd = &cobra.Command{
	Use:    "mcp-authorize",
	Short:  "Request macOS Full Access for the Reminders MCP app",
	Hidden: true,
	RunE: func(cmd *cobra.Command, _ []string) error {
		service, err := reminders.NewService()
		if err != nil {
			return err
		}
		defer service.Close()
		status := service.Status(cmd.Context())
		if !status.Authenticated {
			return fmt.Errorf("Reminders MCP authorization failed: %s", status.Detail)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Reminders MCP has Full Access")
		return nil
	},
}

func init() { RootCmd.AddCommand(mcpAuthorizeCmd) }
