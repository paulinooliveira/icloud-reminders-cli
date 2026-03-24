package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/queue"
	"icloud-reminders/internal/writer"
	"icloud-reminders/pkg/models"
)

var (
	queueChildTitle     string
	queueChildPriority  string
	queueChildDue       string
	queueChildFlagged   bool
	queueChildUnflagged bool
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

	children := listChildrenForParent(parentCloudID)
	candidate := chooseCanonicalChild(children, childState, title)
	for _, duplicate := range duplicateChildren(children, candidate, title) {
		result, err := w.DeleteReminder(duplicate.ID)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
	}
	childID := childState.CloudID
	if candidate != nil {
		childID = candidate.ID
	}

	if childID == "" {
		result, err := w.AddReminder(title, parentListID, deref(due), priorityLabelFromValue(derefPriority(priority, childState.Priority)), "", parentCloudID)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
		if created, _ := result["id"].(string); strings.TrimSpace(created) != "" {
			childID = created
		}
	} else if candidate != nil && strings.TrimSpace(candidate.Title) != title {
		result, err := w.EditReminder(childID, writer.ReminderChanges{Title: &title})
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
		}
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
		result, err := w.EditReminder(childID, changes)
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

func completeQueueChild(state *queue.State, parentKey, childKey string, loud bool) error {
	parentItem, ok := state.Items[parentKey]
	if !ok || strings.TrimSpace(parentItem.Title) == "" {
		return fmt.Errorf("parent queue key %q not found in state", parentKey)
	}
	childState, ok := parentItem.Children[childKey]
	if !ok {
		return fmt.Errorf("queue child %q not found under %q", childKey, parentKey)
	}
	childID := resolveChildQueueCloudID(parentItem, childState)
	if childID == "" {
		return fmt.Errorf("queue child %q under %q has no resolved cloud id", childKey, parentKey)
	}
	result, err := w.CompleteReminder(childID)
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
	childID := resolveChildQueueCloudID(parentItem, childState)
	if childID != "" {
		result, err := w.DeleteReminder(childID)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok && errMsg != "" {
			return fmt.Errorf("%s", errMsg)
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
	if parentItem.CloudID != "" {
		if syncEngine.Cache.Reminders[parentItem.CloudID] != nil {
			return parentItem.CloudID
		}
	}
	if candidate := syncEngine.FindReminderByTitle(parentItem.Title, "", false); candidate != "" {
		return candidate
	}
	return ""
}

func resolveParentQueueListID(parentCloudID string) string {
	if reminder := syncEngine.Cache.Reminders[parentCloudID]; reminder != nil && reminder.ListRef != nil {
		return *reminder.ListRef
	}
	return ""
}

func resolveChildQueueCloudID(parentItem queue.StateItem, childState queue.ChildStateItem) string {
	if childState.CloudID != "" && syncEngine.Cache.Reminders[childState.CloudID] != nil {
		return childState.CloudID
	}
	parentCloudID := resolveParentQueueCloudID(parentItem)
	if parentCloudID == "" {
		return ""
	}
	candidate := chooseCanonicalChild(listChildrenForParent(parentCloudID), childState, childState.Title)
	if candidate != nil {
		return candidate.ID
	}
	return ""
}

func listChildrenForParent(parentCloudID string) []*models.Reminder {
	children := make([]*models.Reminder, 0)
	for _, reminder := range syncEngine.GetReminders(true) {
		if reminder == nil || reminder.ParentRef == nil || *reminder.ParentRef != parentCloudID {
			continue
		}
		children = append(children, reminder)
	}
	return children
}

func chooseCanonicalChild(children []*models.Reminder, childState queue.ChildStateItem, desiredTitle string) *models.Reminder {
	var candidates []*models.Reminder
	for _, child := range children {
		if child == nil {
			continue
		}
		if childState.CloudID != "" && child.ID == childState.CloudID {
			return child
		}
		if strings.TrimSpace(desiredTitle) != "" && child.Title == desiredTitle {
			candidates = append(candidates, child)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	best := candidates[0]
	bestTS := reminderModifiedTS(best)
	for _, child := range candidates[1:] {
		if ts := reminderModifiedTS(child); ts > bestTS {
			best = child
			bestTS = ts
		}
	}
	return best
}

func duplicateChildren(children []*models.Reminder, keep *models.Reminder, desiredTitle string) []*models.Reminder {
	duplicates := make([]*models.Reminder, 0)
	for _, child := range children {
		if child == nil || child.Title != desiredTitle {
			continue
		}
		if keep != nil && child.ID == keep.ID {
			continue
		}
		duplicates = append(duplicates, child)
	}
	return duplicates
}

func reminderModifiedTS(reminder *models.Reminder) int64 {
	if reminder == nil || reminder.ModifiedTS == nil {
		return 0
	}
	return *reminder.ModifiedTS
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
