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
		reminderID := args[0]
		payload := struct {
			ReminderID string `json:"reminder_id"`
		}{ReminderID: reminderID}
		if err := executeMutation("reopen", "reminder", reminderID, payload, true, func() (mutationOutcome, error) {
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.ReopenReminder(reminderID)
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
					"completed": false,
					"deleted":   false,
				},
			}, nil
		}); err != nil {
			return err
		}
		fmt.Printf("✅ Reopened: %s\n", args[0])
		return nil
	},
}
