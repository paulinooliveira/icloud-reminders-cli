// Package cmd provides the native EventKit CLI and MCP commands.
package cmd

import (
	"github.com/spf13/cobra"
)

// RootCmd is the root cobra command.
var RootCmd = &cobra.Command{
	Use:   "reminders",
	Short: "Apple Reminders CLI and MCP (native macOS EventKit)",
}

func init() {
	RootCmd.AddCommand(
		listsNativeCmd,
		showNativeCmd,
		getNativeCmd,
		addNativeCmd,
		completeNativeCmd,
		deleteNativeCmd,
		statusNativeCmd,
	)
}
