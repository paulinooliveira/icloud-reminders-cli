package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/queue"
)

var queueRefreshCmd = &cobra.Command{
	Use:   "queue-refresh <key>",
	Short: "Recompute deterministic Sebastian queue fields and rewrite the reminder",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			key := args[0]
			payload := struct {
				Key  string `json:"key"`
				List string `json:"list_override"`
			}{Key: key, List: queueList}
			if err := executeMutation("queue-refresh", "queue", key, payload, false, func() (mutationOutcome, error) {
				state, err := queue.LoadState()
				if err != nil {
					return mutationOutcome{}, err
				}
				item, ok := state.Items[key]
				if !ok || strings.TrimSpace(item.Title) == "" {
					return mutationOutcome{}, fmt.Errorf("queue key %q not found in state", key)
				}
				now := time.Now()
				var statusPtr *string
				if strings.TrimSpace(item.StatusLine) != "" {
					status := item.StatusLine
					statusPtr = &status
				}
				var hoursBudgetPtr *float64
				if item.HoursBudget > 0 {
					hoursBudgetPtr = floatPtr(item.HoursBudget)
				}
				var tokensBudgetPtr *int64
				if item.TokensBudget > 0 {
					tokensBudgetPtr = int64Ptr(item.TokensBudget)
				}
				var blockedPtr *bool
				if item.Blocked {
					blockedPtr = boolPtr(true)
				}
				spec := queue.Spec{
					Key:          item.Key,
					Title:        item.Title,
					Section:      item.Section,
					Tags:         item.Tags,
					Priority:     item.Priority,
					Due:          item.Due,
					StatusLine:   statusPtr,
					Checklist:    item.Checklist,
					HoursBudget:  hoursBudgetPtr,
					TokensBudget: tokensBudgetPtr,
					Executor:     item.Executor,
					Blocked:      blockedPtr,
				}
				finalSpec, preview := finalizeQueueSpec(state, spec, now)
				appleID, cloudID, err := reconcileQueueReminder(finalSpec, item, priorityLabelFromValue(item.Priority), queueList)
				if err != nil {
					return mutationOutcome{}, err
				}
				preview.AppleID = appleID
				preview.CloudID = cloudID
				preview.UpdatedAt = now.Format(time.RFC3339)
				state.Items[key] = preview
				if err := state.Save(); err != nil {
					return mutationOutcome{}, err
				}
				return mutationOutcome{
					Backend:    map[string]interface{}{"cloud_id": cloudID, "apple_id": appleID},
					CloudID:    cloudID,
					AppleID:    appleID,
					Title:      preview.Title,
					Projection: preview,
				}, nil
			}); err != nil {
				return err
			}
			fmt.Printf("✅ Queue refreshed: %s [%s]\n", key, key)
			return nil
		})
	},
}

func floatPtr(v float64) *float64 {
	return &v
}

func int64Ptr(v int64) *int64 {
	return &v
}

func boolPtr(v bool) *bool {
	return &v
}
