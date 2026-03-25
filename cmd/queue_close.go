package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/queue"
)

var queueCompleteCmd = &cobra.Command{
	Use:   "queue-complete <key>",
	Short: "Complete a Sebastian queue item by stable queue key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			key := args[0]
			payload := struct {
				Key string `json:"key"`
			}{Key: key}
			if err := executeMutation("queue-complete", "queue", key, payload, false, func() (mutationOutcome, error) {
				_, cfg, err := loadOptionalValidatorBridge()
				if err != nil {
					return mutationOutcome{}, err
				}
				fallbackListID := ""
				if cfg != nil {
					fallbackListID = cfg.SebastianListID
				}
				listID := canonicalQueueListID("", fallbackListID)
				if listID == "" {
					listID = canonicalQueueListID("Sebastian", "")
				}
				state, err := queue.LoadState()
				if err != nil {
					return mutationOutcome{}, err
				}
				item, ok := state.Items[key]
				if !ok || strings.TrimSpace(item.Title) == "" {
					return mutationOutcome{}, fmt.Errorf("queue key %q not found in state; create/update it with queue-upsert first", key)
				}
				for childKey := range item.Children {
					if err := completeQueueChild(state, key, childKey, false); err != nil {
						return mutationOutcome{}, err
					}
				}
				cloudID := resolveQueueCloudID(item.CloudID)
				if cloudID != "" {
					result, err := completeReminderWithFallback(cloudID)
					if err != nil {
						return mutationOutcome{}, err
					}
					if errMsg, ok := result["error"].(string); ok && errMsg != "" {
						return mutationOutcome{}, fmt.Errorf("%s", errMsg)
					}
				} else if cfg != nil && strings.TrimSpace(cfg.SebastianListID) == "" {
					return mutationOutcome{}, fmt.Errorf("queue key %q has no stable cloud id; set a default Sebastian list in config or resync state", key)
				} else if cfg == nil {
					return mutationOutcome{}, fmt.Errorf("queue key %q has no stable cloud id to complete", key)
				}
				delete(state.Items, key)
				for sessionKey, boundKey := range state.Bindings {
					if boundKey == key {
						delete(state.Bindings, sessionKey)
					}
				}
				if err := state.Save(); err != nil {
					return mutationOutcome{}, err
				}
				if err := cleanupQueueEmptySections(listID); err != nil {
					return mutationOutcome{}, err
				}
				return mutationOutcome{DeleteProjection: true}, nil
			}); err != nil {
				return err
			}
			fmt.Printf("✅ Queue completed: %s\n", key)
			return nil
		})
	},
}

var queueDeleteCmd = &cobra.Command{
	Use:   "queue-delete <key>",
	Short: "Delete a Sebastian queue item by stable queue key",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			key := args[0]
			payload := struct {
				Key string `json:"key"`
			}{Key: key}
			if err := executeMutation("queue-delete", "queue", key, payload, false, func() (mutationOutcome, error) {
				bridge, cfg, err := loadOptionalValidatorBridge()
				if err != nil {
					return mutationOutcome{}, err
				}
				fallbackListID := ""
				if cfg != nil {
					fallbackListID = cfg.SebastianListID
				}
				listID := canonicalQueueListID("", fallbackListID)
				if listID == "" {
					listID = canonicalQueueListID("Sebastian", "")
				}
				state, err := queue.LoadState()
				if err != nil {
					return mutationOutcome{}, err
				}
				item, ok := state.Items[key]
				if !ok || strings.TrimSpace(item.Title) == "" {
					return mutationOutcome{}, fmt.Errorf("queue key %q not found in state; create/update it with queue-upsert first", key)
				}
				for childKey := range item.Children {
					if err := deleteQueueChild(state, key, childKey, false); err != nil {
						return mutationOutcome{}, err
					}
				}
				cloudID := resolveQueueCloudID(item.CloudID)
				if cloudID == "" && bridge == nil {
					// Keep behavior explicit: without stable id and without bridge access, we cannot
					// safely identify and delete the live reminder.
					return mutationOutcome{}, fmt.Errorf("queue key %q has no stable cloud id and no bridge access", key)
				}
				if cloudID != "" {
					if err := deleteCloudRecordStrict(cloudID); err != nil {
						return mutationOutcome{}, err
					}
				}
				if bridge != nil && cloudID == "" {
					if err := bridge.DeleteReminder(item.AppleID); err != nil && !applebridge.IsNotFoundError(err) {
						return mutationOutcome{}, err
					}
				}
				delete(state.Items, key)
				for sessionKey, boundKey := range state.Bindings {
					if boundKey == key {
						delete(state.Bindings, sessionKey)
					}
				}
				if err := state.Save(); err != nil {
					return mutationOutcome{}, err
				}
				if err := cleanupQueueEmptySections(listID); err != nil {
					return mutationOutcome{}, err
				}
				return mutationOutcome{DeleteProjection: true}, nil
			}); err != nil {
				return err
			}
			fmt.Printf("✅ Queue deleted: %s\n", key)
			return nil
		})
	},
}
