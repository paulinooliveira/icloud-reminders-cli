package cmd

import (
	"fmt"
	"strings"

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
			result, err := completeReminderWithFallback(reminderID)
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

func completeReminderWithFallback(reminderID string) (map[string]interface{}, error) {
	result, err := w.CompleteReminder(reminderID)
	if err == nil {
		return result, nil
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "not found") && !strings.Contains(msg, "missing change tag") {
		return nil, err
	}
	if syncErr := syncEngine.Sync(false); syncErr != nil {
		return nil, err
	}
	return w.CompleteReminder(reminderID)
}
