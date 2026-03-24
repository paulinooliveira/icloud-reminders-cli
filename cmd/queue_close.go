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
				if err := syncEngine.Sync(false); err != nil {
					return mutationOutcome{}, err
				}
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
					if err := completeQueueChild(state, key, childKey, false); err != nil {
						return mutationOutcome{}, err
					}
				}
				cloudByUUID := queue.BuildCloudByUUID(syncEngine.GetReminders(true), listID)
				var choice queue.CanonicalChoice
				if bridge != nil {
					listName := "Sebastian"
					if cfg != nil && cfg.SebastianListName != "" {
						listName = cfg.SebastianListName
					}
					appItems, err := bridge.ListReminders(listName)
					if err != nil {
						return mutationOutcome{}, err
					}
					matches := queue.FindExactTitleMatches(appItems, item.Title)
					choice = queue.ChooseCanonical(matches, cloudByUUID, item, nil)
					for _, dup := range choice.Delete {
						if err := bridge.DeleteReminder(dup.AppleID); err != nil {
							return mutationOutcome{}, err
						}
					}
				}
				cloudID := item.CloudID
				if cloudID == "" && choice.Keep != nil {
					if cloud := cloudByUUID[strings.ToUpper(choice.Keep.UUID())]; cloud != nil {
						cloudID = cloud.ID
					}
				}
				if cloudID == "" {
					cloudID = syncEngine.FindReminderByTitle(item.Title, listID, false)
				}
				if cloudID != "" {
					result, err := w.CompleteReminder(cloudID)
					if err != nil {
						return mutationOutcome{}, err
					}
					if errMsg, ok := result["error"].(string); ok && errMsg != "" {
						return mutationOutcome{}, fmt.Errorf("%s", errMsg)
					}
				} else if bridge != nil && choice.Keep != nil {
					completed := true
					if err := bridge.UpdateReminder(choice.Keep.AppleID, nil, nil, &completed); err != nil {
						return mutationOutcome{}, err
					}
				} else {
					return mutationOutcome{}, fmt.Errorf("queue key %q has no visible or cloud-backed reminder to complete", key)
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
				if err := bestEffortSync(); err != nil {
					return mutationOutcome{}, err
				}
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
				cloudByUUID := queue.BuildCloudByUUID(syncEngine.GetReminders(true), listID)
				var choice queue.CanonicalChoice
				listName := "Sebastian"
				if cfg != nil && cfg.SebastianListName != "" {
					listName = cfg.SebastianListName
				}
				if bridge != nil {
					appItems, err := bridge.ListReminders(listName)
					if err != nil {
						return mutationOutcome{}, err
					}
					matches := queue.FindExactTitleMatches(appItems, item.Title)
					choice = queue.ChooseCanonical(matches, cloudByUUID, item, nil)
					for _, dup := range choice.Delete {
						if err := bridge.DeleteReminder(dup.AppleID); err != nil {
							return mutationOutcome{}, err
						}
					}
				}
				cloudID := item.CloudID
				if cloudID == "" && choice.Keep != nil {
					if cloud := cloudByUUID[strings.ToUpper(choice.Keep.UUID())]; cloud != nil {
						cloudID = cloud.ID
					}
				}
				if cloudID == "" {
					cloudID = syncEngine.FindReminderByTitle(item.Title, listID, false)
				}
				if cloudID != "" {
					result, err := w.DeleteReminder(cloudID)
					if err != nil {
						return mutationOutcome{}, err
					}
					if errMsg, ok := result["error"].(string); ok && errMsg != "" {
						return mutationOutcome{}, fmt.Errorf("%s", errMsg)
					}
				}
				if bridge != nil && choice.Keep != nil {
					if err := bridge.DeleteReminder(choice.Keep.AppleID); err != nil && !applebridge.IsNotFoundError(err) {
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
				return mutationOutcome{DeleteProjection: true}, nil
			}); err != nil {
				return err
			}
			fmt.Printf("✅ Queue deleted: %s\n", key)
			return nil
		})
	},
}
