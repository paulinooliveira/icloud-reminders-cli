package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/writer"
	"icloud-reminders/pkg/models"
)

var (
	editTitle      string
	editDue        string
	editNotes      string
	editPriority   string
	editParent     string
	clearDue       bool
	clearNotes     bool
	clearParent    bool
	editFlagged    bool
	editUnflagged  bool
	unsafeTextEdit bool
)

var editCmd = &cobra.Command{
	Use:   "edit <id>",
	Short: "Edit a reminder (title, due date, notes, priority, parent, or flagged state)",
	Long: `Update one or more fields on an existing reminder.

At least one flag must be provided. Only specified fields are changed;
unspecified fields are left unchanged.

Examples:
  reminders edit ABC123 --title "New title"
  reminders edit ABC123 --due 2026-03-01 --priority high
  reminders edit ABC123 --notes "Updated notes"
  reminders edit ABC123 --flagged
  reminders edit ABC123 --unflagged
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
		if clearParent && cmd.Flags().Changed("parent") {
			return fmt.Errorf("use either --parent or --clear-parent, not both")
		}
		if editFlagged && editUnflagged {
			return fmt.Errorf("use either --flagged or --unflagged, not both")
		}
		reminderID := args[0]
		payload := struct {
			ReminderID  string `json:"reminder_id"`
			Title       string `json:"title,omitempty"`
			Due         string `json:"due,omitempty"`
			ClearDue    bool   `json:"clear_due,omitempty"`
			Notes       string `json:"notes,omitempty"`
			ClearNotes  bool   `json:"clear_notes,omitempty"`
			Priority    string `json:"priority,omitempty"`
			Parent      string `json:"parent,omitempty"`
			ClearParent bool   `json:"clear_parent,omitempty"`
			Flagged     bool   `json:"flagged,omitempty"`
			Unflagged   bool   `json:"unflagged,omitempty"`
		}{
			ReminderID: reminderID, Title: editTitle, Due: editDue, ClearDue: clearDue, Notes: editNotes,
			ClearNotes: clearNotes, Priority: editPriority, Parent: editParent, ClearParent: clearParent,
			Flagged: editFlagged, Unflagged: editUnflagged,
		}
		if err := executeMutation("edit", "reminder", reminderID, payload, true, func() (mutationOutcome, error) {
			if err := bestEffortSync(); err != nil {
				if !shouldProceedWithoutSync(reminderID) {
					return mutationOutcome{}, err
				}
				if cmd.Flags().Changed("parent") && !shouldProceedWithoutSync(editParent) {
					return mutationOutcome{}, err
				}
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
					return mutationOutcome{}, fmt.Errorf("invalid priority %q (use: high, medium, low, none)", editPriority)
				}
				changes.Priority = &priorityVal
			}
			if editFlagged {
				flagged := true
				changes.Flagged = &flagged
			}
			if editUnflagged {
				flagged := false
				changes.Flagged = &flagged
			}
			if cmd.Flags().Changed("parent") {
				fullID := syncEngine.FindReminderByID(reminderID)
				listID := ""
				if current := syncEngine.Cache.Reminders[fullID]; current != nil && current.ListRef != nil {
					listID = *current.ListRef
				}
				parentRef := ""
				if rid := syncEngine.FindReminderByID(editParent); rid != "" {
					parentRef = rid
				} else if rid := syncEngine.FindReminderByTitle(editParent, listID, true); rid != "" {
					parentRef = rid
				} else if rid := syncEngine.FindReminderByTitle(editParent, "", true); rid != "" {
					parentRef = rid
				}
				if parentRef == "" {
					return mutationOutcome{}, fmt.Errorf("parent reminder %q not found", editParent)
				}
				changes.ParentRef = &parentRef
			}
			if clearParent {
				empty := ""
				changes.ParentRef = &empty
			}
			if changes.Title == nil && changes.DueDate == nil && changes.Notes == nil && changes.Priority == nil && changes.Completed == nil && changes.Flagged == nil && changes.HashtagIDs == nil && changes.ParentRef == nil {
				return mutationOutcome{}, nil
			}
			result, err := w.EditReminder(reminderID, changes)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			fullID := syncEngine.FindReminderByID(reminderID)
			var projection map[string]interface{}
			if rd := syncEngine.Cache.Reminders[fullID]; rd != nil {
				projection = map[string]interface{}{
					"id":         fullID,
					"title":      rd.Title,
					"completed":  rd.Completed,
					"flagged":    rd.Flagged,
					"due":        rd.Due,
					"priority":   rd.Priority,
					"notes":      rd.Notes,
					"list_ref":   rd.ListRef,
					"parent_ref": rd.ParentRef,
					"deleted":    false,
				}
			}
			return mutationOutcome{
				Backend:    result,
				CloudID:    fullID,
				Projection: projection,
			}, nil
		}); err != nil {
			return err
		}
		fmt.Printf("✅ Updated: %s\n", args[0])
		return nil
	},
}

func init() {
	editCmd.Flags().StringVar(&editTitle, "title", "", "New title")
	editCmd.Flags().StringVarP(&editDue, "due", "d", "", "New due date or datetime (YYYY-MM-DD, YYYY-MM-DDTHH:MM, or RFC3339)")
	editCmd.Flags().StringVarP(&editNotes, "notes", "n", "", "New notes")
	editCmd.Flags().StringVarP(&editPriority, "priority", "p", "", "New priority (high, medium, low, none)")
	editCmd.Flags().StringVar(&editParent, "parent", "", "New parent reminder title or ID")
	editCmd.Flags().BoolVar(&editFlagged, "flagged", false, "Mark the reminder flagged")
	editCmd.Flags().BoolVar(&editUnflagged, "unflagged", false, "Clear the reminder flagged state")
	editCmd.Flags().BoolVar(&unsafeTextEdit, "unsafe-text-edit", false, "Deprecated debug flag; normal title/note edits use the CloudKit-safe path")
	editCmd.Flags().BoolVar(&clearDue, "clear-due", false, "Clear the due date")
	editCmd.Flags().BoolVar(&clearNotes, "clear-notes", false, "Clear the notes")
	editCmd.Flags().BoolVar(&clearParent, "clear-parent", false, "Clear the parent reminder")
	_ = editCmd.Flags().MarkHidden("unsafe-text-edit")
}
