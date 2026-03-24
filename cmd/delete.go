package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var deleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a reminder",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reminderID := args[0]
		payload := struct {
			ReminderID string `json:"reminder_id"`
		}{ReminderID: reminderID}
		if err := executeMutation("delete", "reminder", reminderID, payload, true, func() (mutationOutcome, error) {
			if err := bestEffortSync(); err != nil && !shouldProceedWithoutSync(reminderID) {
				return mutationOutcome{}, err
			}
			result, err := w.DeleteReminder(reminderID)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			return mutationOutcome{Backend: result, DeleteProjection: true}, nil
		}); err != nil {
			return err
		}
		fmt.Printf("✅ Deleted: %s\n", args[0])
		return nil
	},
}
