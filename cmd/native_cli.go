package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/reminders"
)

func withNativeService(cmd *cobra.Command, fn func(reminders.Service) error) error {
	service, err := reminders.NewService()
	if err != nil {
		return err
	}
	defer service.Close()
	return fn(service)
}

func writeJSON(cmd *cobra.Command, value any) error {
	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

var listsNativeCmd = &cobra.Command{
	Use: "lists", Short: "List reminder lists through native EventKit",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withNativeService(cmd, func(service reminders.Service) error {
			lists, err := service.Lists(cmd.Context())
			if err != nil {
				return err
			}
			return writeJSON(cmd, map[string]any{"lists": lists})
		})
	},
}

var showList string
var showCompleted bool
var showLimit int
var showOffset int

var showNativeCmd = &cobra.Command{
	Use: "show", Short: "Show a bounded page from one reminder list",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withNativeService(cmd, func(service reminders.Service) error {
			page, err := service.Show(cmd.Context(), reminders.ShowInput{List: showList, IncludeCompleted: showCompleted, Limit: showLimit, Offset: showOffset})
			if err != nil {
				return err
			}
			return writeJSON(cmd, page)
		})
	},
}

var getList string
var getNativeCmd = &cobra.Command{
	Use: "get <id>", Short: "Get a reminder by ID or unique prefix", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withNativeService(cmd, func(service reminders.Service) error {
			item, err := service.Find(cmd.Context(), getList, args[0])
			if err != nil {
				return err
			}
			return writeJSON(cmd, map[string]any{"reminder": item})
		})
	},
}

var nativeAddList, nativeAddDue, nativeAddPriority, nativeAddNotes string
var addNativeCmd = &cobra.Command{
	Use: "add <title>", Short: "Add a reminder through native EventKit", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withNativeService(cmd, func(service reminders.Service) error {
			item, err := service.Add(cmd.Context(), reminders.AddInput{Title: args[0], List: nativeAddList, Due: nativeAddDue, Priority: nativeAddPriority, Notes: nativeAddNotes})
			if err != nil {
				return err
			}
			return writeJSON(cmd, map[string]any{"reminder": item})
		})
	},
}

var completeList string
var completeNativeCmd = &cobra.Command{
	Use: "complete <id>", Short: "Complete a reminder through native EventKit", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withNativeService(cmd, func(service reminders.Service) error {
			matched, err := service.Find(cmd.Context(), completeList, args[0])
			if err != nil {
				return err
			}
			item, err := service.Complete(cmd.Context(), matched.ID)
			if err != nil {
				return err
			}
			return writeJSON(cmd, map[string]any{"reminder": item})
		})
	},
}

var deleteNativeCmd = &cobra.Command{
	Use: "delete <id>...", Short: "Delete one or more reminders through native EventKit", Args: cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return withNativeService(cmd, func(service reminders.Service) error {
			for _, id := range args {
				if err := service.Delete(cmd.Context(), id); err != nil {
					return fmt.Errorf("delete %s: %w", id, err)
				}
			}
			return writeJSON(cmd, map[string]any{"deleted": len(args)})
		})
	},
}

var statusNativeCmd = &cobra.Command{
	Use: "status", Short: "Report native EventKit authorization status",
	RunE: func(cmd *cobra.Command, _ []string) error {
		return withNativeService(cmd, func(service reminders.Service) error {
			status := service.Status(cmd.Context())
			if !status.Authenticated {
				return fmt.Errorf("EventKit unavailable: %s", status.Detail)
			}
			return writeJSON(cmd, status)
		})
	},
}

func init() {
	showNativeCmd.Flags().StringVarP(&showList, "list", "l", "", "Reminder list name or ID (required)")
	showNativeCmd.Flags().BoolVarP(&showCompleted, "completed", "a", false, "Include completed reminders")
	showNativeCmd.Flags().IntVar(&showLimit, "limit", reminders.DefaultLimit, "Maximum items to return")
	showNativeCmd.Flags().IntVar(&showOffset, "offset", 0, "Zero-based offset")
	_ = showNativeCmd.MarkFlagRequired("list")
	getNativeCmd.Flags().StringVarP(&getList, "list", "l", "", "Reminder list name or ID (required)")
	_ = getNativeCmd.MarkFlagRequired("list")
	addNativeCmd.Flags().StringVarP(&nativeAddList, "list", "l", "", "Reminder list name or ID (required)")
	addNativeCmd.Flags().StringVarP(&nativeAddDue, "due", "d", "", "ISO-8601 or YYYY-MM-DD due date")
	addNativeCmd.Flags().StringVarP(&nativeAddPriority, "priority", "p", "none", "none, low, medium, or high")
	addNativeCmd.Flags().StringVarP(&nativeAddNotes, "notes", "n", "", "Reminder notes")
	_ = addNativeCmd.MarkFlagRequired("list")
	completeNativeCmd.Flags().StringVarP(&completeList, "list", "l", "", "Reminder list name or ID (required)")
	_ = completeNativeCmd.MarkFlagRequired("list")
}
