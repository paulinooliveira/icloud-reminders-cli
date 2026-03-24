package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var completeCmd = &cobra.Command{
	Use:   "complete <id>",
	Short: "Mark a reminder as complete",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reminderID := args[0]
		payload := struct {
			ReminderID string `json:"reminder_id"`
		}{ReminderID: reminderID}
		if err := executeMutation("complete", "reminder", reminderID, payload, true, func() (mutationOutcome, error) {
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.CompleteReminder(reminderID)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			return mutationOutcome{
				Backend: result,
				CloudID: reminderID,
				Projection: map[string]interface{}{
					"id":        reminderID,
					"completed": true,
					"deleted":   false,
				},
			}, nil
		}); err != nil {
			return err
		}
		fmt.Printf("✅ Completed: %s\n", args[0])
		return nil
	},
}
