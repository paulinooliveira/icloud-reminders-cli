package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/queue"
	"icloud-reminders/internal/writer"
)

var (
	queueChildTitle     string
	queueChildPriority  string
	queueChildDue       string
	queueChildFlagged   bool
	queueChildUnflagged bool
)

var (
	queueChildAddReminder = func(title, listName, dueDate, priority, notes, parentID string) (map[string]interface{}, error) {
		return w.AddReminder(title, listName, dueDate, priority, notes, parentID)
	}
	queueChildEditReminder = func(reminderID string, changes writer.ReminderChanges) (map[string]interface{}, error) {
		return w.EditReminderNoVisibleRepair(reminderID, changes)
	}
	queueChildDeleteReminder = func(reminderID string) (map[string]interface{}, error) { return w.DeleteReminder(reminderID) }
)

var queueChildUpsertCmd = &cobra.Command{
	Use:   "queue-child-upsert <parent-key> <child-key>",
	Short: "Idempotently create or reconcile one Sebastian queue child item under a parent queue item",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			parentKey := args[0]
			childKey := args[1]
			title := strings.TrimSpace(queueChildTitle)
			if title == "" {
				return fmt.Errorf("--title is required")
			}
			duePtr := optionalStringPtr(cmd.Flags().Changed("due"), queueChildDue)
			flaggedPtr := optionalBoolPtr(queueChildFlagged, queueChildUnflagged)
			priorityValue := priorityFromLabel(queueChildPriority)
			priorityPtr := optionalPriorityPtr(cmd.Flags().Changed("priority"), priorityValue)
			payload := struct {
				ParentKey string  `json:"parent_key"`
				ChildKey  string  `json:"child_key"`
				Title     string  `json:"title"`
				Due       *string `json:"due,omitempty"`
				Priority  *int    `json:"priority,omitempty"`
				Flagged   *bool   `json:"flagged,omitempty"`
			}{ParentKey: parentKey, ChildKey: childKey, Title: title, Due: duePtr, Priority: priorityPtr, Flagged: flaggedPtr}
			return executeMutation("queue-child-upsert", "queue-child", parentKey+"/"+childKey, payload, false, func() (mutationOutcome, error) {
				if err := bestEffortSync(); err != nil && !shouldProceedWithoutSync(parentKey) {
					return mutationOutcome{}, err
				}
				state, err := queue.LoadState()
				if err != nil {
					return mutationOutcome{}, err
				}
				parentItem, ok := state.Items[parentKey]
				if !ok || strings.TrimSpace(parentItem.Title) == "" {
					return mutationOutcome{}, fmt.Errorf("parent queue key %q not found in state", parentKey)
				}
				if err := upsertQueueChild(state, parentKey, childKey, title, duePtr, priorityPtr, flaggedPtr); err != nil {
					return mutationOutcome{}, err
				}
				if err := state.Save(); err != nil {
					return mutationOutcome{}, err
				}
				child := state.Items[parentKey].Children[childKey]
				return mutationOutcome{
					Backend:    map[string]interface{}{"cloud_id": child.CloudID, "apple_id": child.AppleID},
					CloudID:    child.CloudID,
					AppleID:    child.AppleID,
					Title:      child.Title,
					Projection: child,
				}, nil
			})
		})
	},
}

var queueChildCompleteCmd = &cobra.Command{
	Use:   "queue-child-complete <parent-key> <child-key>",
	Short: "Complete one Sebastian queue child item",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			parentKey := args[0]
			childKey := args[1]
			payload := struct {
				ParentKey string `json:"parent_key"`
				ChildKey  string `json:"child_key"`
			}{ParentKey: parentKey, ChildKey: childKey}
			return executeMutation("queue-child-complete", "queue-child", parentKey+"/"+childKey, payload, false, func() (mutationOutcome, error) {
				if err := bestEffortSync(); err != nil && !shouldProceedWithoutSync(parentKey) {
					return mutationOutcome{}, err
				}
				state, err := queue.LoadState()
				if err != nil {
					return mutationOutcome{}, err
				}
				if err := completeQueueChild(state, parentKey, childKey, true); err != nil {
					return mutationOutcome{}, err
				}
				if err := state.Save(); err != nil {
					return mutationOutcome{}, err
				}
				return mutationOutcome{DeleteProjection: true}, nil
			})
		})
	},
}

var queueChildDeleteCmd = &cobra.Command{
	Use:   "queue-child-delete <parent-key> <child-key>",
	Short: "Delete one Sebastian queue child item",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			parentKey := args[0]
			childKey := args[1]
			payload := struct {
				ParentKey string `json:"parent_key"`
				ChildKey  string `json:"child_key"`
			}{ParentKey: parentKey, ChildKey: childKey}
			return executeMutation("queue-child-delete", "queue-child", parentKey+"/"+childKey, payload, false, func() (mutationOutcome, error) {
				if err := bestEffortSync(); err != nil && !shouldProceedWithoutSync(parentKey) {
					return mutationOutcome{}, err
				}
				state, err := queue.LoadState()
				if err != nil {
					return mutationOutcome{}, err
				}
				if err := deleteQueueChild(state, parentKey, childKey, true); err != nil {
					return mutationOutcome{}, err
				}
				if err := state.Save(); err != nil {
					return mutationOutcome{}, err
				}
				return mutationOutcome{DeleteProjection: true}, nil
			})
		})
	},
}

func optionalStringPtr(changed bool, value string) *string {
	if !changed {
		return nil
	}
	return &value
}

func optionalBoolPtr(on, off bool) *bool {
	if !on && !off {
		return nil
	}
	value := on && !off
	return &value
}

func optionalPriorityPtr(changed bool, value int) *int {
	if !changed {
		return nil
	}
	return &value
}

func upsertQueueChild(state *queue.State, parentKey, childKey, title string, due *string, priority *int, flagged *bool) error {
	parentItem, ok := state.Items[parentKey]
	if !ok || strings.TrimSpace(parentItem.Title) == "" {
		return fmt.Errorf("parent queue key %q not found in state", parentKey)
	}
	parentCloudID := resolveParentQueueCloudID(parentItem)
	if parentCloudID == "" {
		return fmt.Errorf("parent queue key %q has no resolved cloud id", parentKey)
	}
	parentListID := resolveParentQueueListID(parentCloudID)
	if parentListID == "" {
		return fmt.Errorf("parent queue key %q has no resolved list id", parentKey)
	}
	if parentItem.Children == nil {
		parentItem.Children = map[string]queue.ChildStateItem{}
	}
	childState := parentItem.Children[childKey]
	priorTitle := strings.TrimSpace(childState.Title)
	childState.Key = childKey
	childState.Title = title
	if due != nil {
		childState.Due = due
	}
	if priority != nil {
		childState.Priority = *priority
	}
	if flagged != nil {
		childState.Flagged = *flagged
	}

	childID := resolveQueueCloudID(childState.CloudID)

	if childID == "" {
		result, err := queueChildAddReminder(title, parentListID, deref(due), priorityLabelFromValue(derefPriority(priority, childState.Priority)), "", parentCloudID)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		if created, _ := result["id"].(string); strings.TrimSpace(created) != "" {
			childID = created
		} else {
			return fmt.Errorf("queue child creation did not return a reminder id")
		}
	} else if priorTitle != title && childState.CloudID != "" {
		result, err := recreateQueueChildReminderID(parentCloudID, parentListID, childID, title, childState, due, priority, flagged)
		if err != nil {
			return err
		}
		childID, _ = result["id"].(string)
		childState.CloudID = childID
		childState.AppleID = "x-apple-reminder://" + shortReminderID(childID)
	}

	changes := writer.ReminderChanges{}
	if due != nil {
		changes.DueDate = due
	}
	if priority != nil {
		changes.Priority = priority
	}
	if flagged != nil {
		changes.Flagged = flagged
	}
	if changes.DueDate != nil || changes.Priority != nil || changes.Flagged != nil {
		result, err := queueChildEditReminder(childID, changes)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
	}

	childState.CloudID = childID
	childState.UpdatedAt = time.Now().Format(time.RFC3339)
	parentItem.Children[childKey] = childState
	state.Items[parentKey] = parentItem
	fmt.Printf("✅ Queue child upserted: %s [%s/%s]\n", title, parentKey, childKey)
	return nil
}

func recreateQueueChildReminderID(parentCloudID, parentListID, oldCloudID, newTitle string, childState queue.ChildStateItem, due *string, priority *int, flagged *bool) (map[string]interface{}, error) {
	dueValue := deref(due)
	if due == nil {
		dueValue = deref(childState.Due)
	}
	priorityValue := derefPriority(priority, childState.Priority)

	result, err := queueChildAddReminder(newTitle, parentListID, dueValue, priorityLabelFromValue(priorityValue), "", parentCloudID)
	if err != nil {
		return nil, err
	}
	newID, _ := result["id"].(string)
	if strings.TrimSpace(newID) == "" {
		return nil, fmt.Errorf("queue child recreate did not return a new reminder id")
	}

	targetFlagged := childState.Flagged
	if flagged != nil {
		targetFlagged = *flagged
	}
	if targetFlagged {
		if _, err := queueChildEditReminder(newID, writer.ReminderChanges{Flagged: &targetFlagged}); err != nil {
			return nil, err
		}
	}
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return nil, fmt.Errorf("%s", errMsg)
	}

	if oldCloudID != "" {
		deleteOldResult, deleteErr := queueChildDeleteReminder(oldCloudID)
		if deleteErr != nil {
			if errMsg, ok := deleteOldResult["error"].(string); ok && errMsg != "" {
				deleteErr = fmt.Errorf("%s", errMsg)
			}
			if _, cleanupErr := queueChildDeleteReminder(newID); cleanupErr != nil {
				return nil, fmt.Errorf("recreate child failed and could not delete new id %s after old delete failed: %w", newID, deleteErr)
			}
			return nil, deleteErr
		}
	}
	result["id"] = newID
	return result, nil
}

func completeQueueChild(state *queue.State, parentKey, childKey string, loud bool) error {
	parentItem, ok := state.Items[parentKey]
	if !ok || strings.TrimSpace(parentItem.Title) == "" {
		return fmt.Errorf("parent queue key %q not found in state", parentKey)
	}
	childState, ok := parentItem.Children[childKey]
	if !ok {
		return fmt.Errorf("queue child %q not found under %q", childKey, parentKey)
	}
	childID := resolveChildQueueCloudID(childState)
	if childID == "" {
		return fmt.Errorf("queue child %q under %q has no resolved cloud id", childKey, parentKey)
	}
	result, err := completeReminderWithFallback(childID)
	if err != nil {
		return err
	}
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return fmt.Errorf("%s", errMsg)
	}
	delete(parentItem.Children, childKey)
	state.Items[parentKey] = parentItem
	if loud {
		fmt.Printf("✅ Queue child completed: %s [%s/%s]\n", childState.Title, parentKey, childKey)
	}
	return nil
}

func deleteQueueChild(state *queue.State, parentKey, childKey string, loud bool) error {
	parentItem, ok := state.Items[parentKey]
	if !ok || strings.TrimSpace(parentItem.Title) == "" {
		return fmt.Errorf("parent queue key %q not found in state", parentKey)
	}
	childState, ok := parentItem.Children[childKey]
	if !ok {
		return fmt.Errorf("queue child %q not found under %q", childKey, parentKey)
	}
	childID := resolveChildQueueCloudID(childState)
	if childID != "" {
		if err := deleteCloudRecordStrict(childID); err != nil {
			return err
		}
	}
	delete(parentItem.Children, childKey)
	state.Items[parentKey] = parentItem
	if loud {
		fmt.Printf("✅ Queue child deleted: %s [%s/%s]\n", childState.Title, parentKey, childKey)
	}
	return nil
}

func resolveParentQueueCloudID(parentItem queue.StateItem) string {
	return resolveQueueCloudID(parentItem.CloudID)
}

func resolveParentQueueListID(parentCloudID string) string {
	if reminder := syncEngine.Cache.Reminders[parentCloudID]; reminder != nil && reminder.ListRef != nil {
		return *reminder.ListRef
	}
	return ""
}

func resolveChildQueueCloudID(childState queue.ChildStateItem) string {
	return resolveQueueCloudID(childState.CloudID)
}

func derefPriority(priority *int, fallback int) int {
	if priority != nil {
		return *priority
	}
	return fallback
}

func init() {
	queueChildUpsertCmd.Flags().StringVar(&queueChildTitle, "title", "", "Child reminder title")
	queueChildUpsertCmd.Flags().StringVar(&queueChildPriority, "priority", "", "Priority (high, medium, low, none)")
	queueChildUpsertCmd.Flags().StringVar(&queueChildDue, "due", "", "Due date or datetime")
	queueChildUpsertCmd.Flags().BoolVar(&queueChildFlagged, "flagged", false, "Flag child reminder")
	queueChildUpsertCmd.Flags().BoolVar(&queueChildUnflagged, "unflagged", false, "Clear child reminder flagged state")
}
