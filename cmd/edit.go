package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/writer"
	"icloud-reminders/pkg/models"
)

var (
	editTitle    string
	editDue      string
	editNotes    string
	editPriority string
	clearDue     bool
	clearNotes   bool
)

var editCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Edit a reminder (title, due date, notes, or priority)",
	Long: `Update one or more fields on an existing reminder.

At least one flag must be provided. Only specified fields are changed;
unspecified fields are left unchanged.

Examples:
  reminders edit ABC123 --title "New title"
  reminders edit ABC123 --due 2026-03-01 --priority high
  reminders edit ABC123 --notes "Updated notes"
  reminders edit ABC123 --clear-notes
  reminders edit ABC123 --clear-due
  reminders edit ABC123 --priority none`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if clearDue && cmd.Flags().Changed("due") {
			return fmt.Errorf("use either --due or --clear-due, not both")
		}
		if clearNotes && cmd.Flags().Changed("notes") {
			return fmt.Errorf("use either --notes or --clear-notes, not both")
		}
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		changes := writer.ReminderChanges{}
		if cmd.Flags().Changed("title") {
			changes.Title = &editTitle
		}
		if cmd.Flags().Changed("due") {
			changes.DueDate = &editDue
		}
		if clearDue {
			empty := ""
			changes.DueDate = &empty
		}
		if cmd.Flags().Changed("notes") {
			changes.Notes = &editNotes
		}
		if clearNotes {
			empty := ""
			changes.Notes = &empty
		}
		if cmd.Flags().Changed("priority") {
			priorityVal, ok := models.PriorityMap[editPriority]
			if !ok {
				return fmt.Errorf("invalid priority %q (use: high, medium, low, none)", editPriority)
			}
			changes.Priority = &priorityVal
		}
		result, err := w.EditReminder(args[0], changes)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		fmt.Printf("✅ Updated: %s\n", args[0])
		return nil
	},
}

func init() {
	editCmd.Flags().StringVar(&editTitle, "title", "", "New title")
	editCmd.Flags().StringVarP(&editDue, "due", "d", "", "New due date (YYYY-MM-DD)")
	editCmd.Flags().StringVarP(&editNotes, "notes", "n", "", "New notes")
	editCmd.Flags().StringVarP(&editPriority, "priority", "p", "", "New priority (high, medium, low, none)")
	editCmd.Flags().BoolVar(&clearDue, "clear-due", false, "Clear the due date")
	editCmd.Flags().BoolVar(&clearNotes, "clear-notes", false, "Clear the notes")
}
