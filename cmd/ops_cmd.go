package cmd

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/queue"
	"icloud-reminders/internal/store"
	"icloud-reminders/internal/writer"
	"icloud-reminders/pkg/models"
)

type addMutationPayload struct {
	Title    string   `json:"title"`
	List     string   `json:"list"`
	Due      string   `json:"due"`
	Priority string   `json:"priority"`
	Notes    string   `json:"notes"`
	Parent   string   `json:"parent"`
	Section  string   `json:"section"`
	Tags     []string `json:"tags,omitempty"`
}

type addBatchMutationPayload struct {
	Titles  []string `json:"titles"`
	List    string   `json:"list"`
	Parent  string   `json:"parent"`
	Section string   `json:"section"`
}

type editMutationPayload struct {
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
}

type reminderIDPayload struct {
	ReminderID string `json:"reminder_id"`
}

type queueUpsertMutationPayload struct {
	Spec          queue.Spec `json:"spec"`
	PriorityLabel string     `json:"priority_label"`
	ListOverride  string     `json:"list_override"`
}

type queueKeyPayload struct {
	Key string `json:"key"`
}

type queueChildMutationPayload struct {
	ParentKey string  `json:"parent_key"`
	ChildKey  string  `json:"child_key"`
	Title     string  `json:"title,omitempty"`
	Due       *string `json:"due,omitempty"`
	Priority  *int    `json:"priority,omitempty"`
	Flagged   *bool   `json:"flagged,omitempty"`
}

var opsAll bool

var opsCmd = &cobra.Command{
	Use:   "ops",
	Short: "List journaled reminder mutation operations",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := store.Open()
		if err != nil {
			return err
		}
		defer db.Close()
		if err := resetStaleApplyingOperations(db); err != nil {
			return err
		}
		if err := compactSupersededOperations(db); err != nil {
			return err
		}
		if err := pruneHistoricalOperations(db); err != nil {
			return err
		}
		var statuses []string
		if !opsAll {
			statuses = []string{"pending", "failed", "applying", "applied", "validator_pending", "reconcile_needed"}
		}
		ops, err := store.ListOperations(db, statuses...)
		if err != nil {
			return err
		}
		fmt.Printf("Operations: %d\n", len(ops))
		for _, op := range ops {
			line := fmt.Sprintf("- %s %s %s/%s", op.Status, op.Kind, op.TargetType, op.TargetKey)
			if op.ErrorText != "" {
				line += " :: " + op.ErrorText
			}
			fmt.Println(line)
		}
		return nil
	},
}

var reconcileCmd = &cobra.Command{
	Use:   "reconcile",
	Short: "Replay pending or failed journaled reminder operations",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := store.Open()
		if err != nil {
			return err
		}
		defer db.Close()
		if err := resetStaleApplyingOperations(db); err != nil {
			return err
		}
		if err := compactSupersededOperations(db); err != nil {
			return err
		}
		if err := pruneHistoricalOperations(db); err != nil {
			return err
		}
		ops, err := store.ListOperations(db, "pending", "failed", "applying", "applied", "validator_pending", "reconcile_needed")
		if err != nil {
			return err
		}
		if len(ops) == 0 {
			fmt.Println("Nothing to reconcile.")
			return nil
		}
		var failed []string
		for idx := len(ops) - 1; idx >= 0; idx-- {
			op := ops[idx]
			if err := replayOperation(db, &op); err != nil {
				failed = append(failed, fmt.Sprintf("%s %s/%s: %v", op.Kind, op.TargetType, op.TargetKey, err))
			}
		}
		if len(failed) > 0 {
			for _, line := range failed {
				fmt.Println(line)
			}
			return fmt.Errorf("reconcile left %d operation(s) unresolved", len(failed))
		}
		if err := pruneHistoricalOperations(db); err != nil {
			return err
		}
		fmt.Printf("Reconciled %d operations.\n", len(ops))
		return nil
	},
}

func replayOperation(db *sql.DB, op *store.Operation) error {
	return runRecordedOperation(db, op, true, func() (mutationOutcome, error) {
		switch op.Kind {
		case "add":
			var payload addMutationPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.AddReminder(payload.Title, payload.List, payload.Due, payload.Priority, payload.Notes, payload.Parent)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok && errMsg != "" {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			reminderID, _ := result["id"].(string)
			if len(payload.Tags) > 0 {
				if _, err := w.SetReminderTags(reminderID, payload.Tags); err != nil {
					return mutationOutcome{}, err
				}
			}
			if payload.Section != "" {
				if _, err := w.AssignReminderToSection(reminderID, payload.List, payload.Section); err != nil {
					return mutationOutcome{}, err
				}
			}
			return mutationOutcome{Backend: result, CloudID: reminderID, Title: payload.Title}, nil
		case "add-batch":
			var payload addBatchMutationPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.AddRemindersBatch(payload.Titles, payload.List, payload.Parent)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok && errMsg != "" {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			if payload.Section != "" {
				listID := payload.List
				if resolved := syncEngine.FindListByName(payload.List); resolved != "" {
					listID = resolved
				}
				rawIDs, _ := result["ids"].([]string)
				if err := w.AssignReminderIDsToSection(listID, payload.Section, rawIDs); err != nil {
					return mutationOutcome{}, err
				}
			}
			return mutationOutcome{Backend: result}, nil
		case "edit":
			var payload editMutationPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			if err := bestEffortSync(); err != nil && !shouldProceedWithoutSync(payload.ReminderID) {
				return mutationOutcome{}, err
			}
			changes := writer.ReminderChanges{}
			if strings.TrimSpace(payload.Title) != "" {
				changes.Title = &payload.Title
			}
			if payload.Due != "" {
				changes.DueDate = &payload.Due
			}
			if payload.ClearDue {
				empty := ""
				changes.DueDate = &empty
			}
			if payload.Notes != "" {
				changes.Notes = &payload.Notes
			}
			if payload.ClearNotes {
				empty := ""
				changes.Notes = &empty
			}
			if payload.Priority != "" {
				p, ok := models.PriorityMap[payload.Priority]
				if !ok {
					return mutationOutcome{}, fmt.Errorf("invalid priority %q", payload.Priority)
				}
				changes.Priority = &p
			}
			if payload.Flagged {
				flagged := true
				changes.Flagged = &flagged
			}
			if payload.Unflagged {
				flagged := false
				changes.Flagged = &flagged
			}
			if payload.Parent != "" {
				fullID := syncEngine.FindReminderByID(payload.ReminderID)
				listID := ""
				if current := syncEngine.Cache.Reminders[fullID]; current != nil && current.ListRef != nil {
					listID = *current.ListRef
				}
				parentRef := ""
				if rid := syncEngine.FindReminderByID(payload.Parent); rid != "" {
					parentRef = rid
				} else if rid := syncEngine.FindReminderByTitle(payload.Parent, listID, true); rid != "" {
					parentRef = rid
				} else if rid := syncEngine.FindReminderByTitle(payload.Parent, "", true); rid != "" {
					parentRef = rid
				}
				if parentRef == "" {
					return mutationOutcome{}, fmt.Errorf("parent reminder %q not found", payload.Parent)
				}
				changes.ParentRef = &parentRef
			}
			if payload.ClearParent {
				empty := ""
				changes.ParentRef = &empty
			}
			result, err := w.EditReminder(payload.ReminderID, changes)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok && errMsg != "" {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			fullID := syncEngine.FindReminderByID(payload.ReminderID)
			return mutationOutcome{Backend: result, CloudID: fullID}, nil
		case "delete":
			return replayReminderDelete(op)
		case "complete":
			var payload reminderIDPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.CompleteReminder(payload.ReminderID)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok && errMsg != "" {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			return mutationOutcome{Backend: result, CloudID: payload.ReminderID}, nil
		case "reopen":
			var payload reminderIDPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.ReopenReminder(payload.ReminderID)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok && errMsg != "" {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			return mutationOutcome{Backend: result, CloudID: payload.ReminderID}, nil
		case "queue-upsert":
			var payload queueUpsertMutationPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			state, err := queue.LoadState()
			if err != nil {
				return mutationOutcome{}, err
			}
			now := time.Now()
			finalSpec, preview := finalizeQueueSpec(state, payload.Spec, now)
			reconciled, err := reconcileQueueReminder(finalSpec, state.Items[payload.Spec.Key], payload.PriorityLabel, payload.ListOverride)
			if err != nil {
				return mutationOutcome{}, err
			}
			preview.AppleID = reconciled.AppleID
			preview.CloudID = reconciled.CloudID
			if reconciled.Children != nil {
				preview.Children = reconciled.Children
			}
			preview.UpdatedAt = now.Format(time.RFC3339)
			state.Items[payload.Spec.Key] = preview
			if err := state.Save(); err != nil {
				return mutationOutcome{}, err
			}
			return mutationOutcome{Backend: map[string]interface{}{"cloud_id": reconciled.CloudID, "apple_id": reconciled.AppleID}, CloudID: reconciled.CloudID, AppleID: reconciled.AppleID, Title: preview.Title, Projection: preview}, nil
		case "queue-refresh":
			var payload queueKeyPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			state, err := queue.LoadState()
			if err != nil {
				return mutationOutcome{}, err
			}
			item := state.Items[payload.Key]
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
				Key: payload.Key, Title: item.Title, Section: item.Section, Tags: item.Tags, Priority: item.Priority, Due: item.Due,
				StatusLine: statusPtr, Checklist: item.Checklist, HoursBudget: hoursBudgetPtr, TokensBudget: tokensBudgetPtr,
				Executor: item.Executor, Blocked: blockedPtr,
			}
			finalSpec, preview := finalizeQueueSpec(state, spec, time.Now())
			reconciled, err := reconcileQueueReminder(finalSpec, item, priorityLabelFromValue(item.Priority), "")
			if err != nil {
				return mutationOutcome{}, err
			}
			preview.AppleID = reconciled.AppleID
			preview.CloudID = reconciled.CloudID
			if reconciled.Children != nil {
				preview.Children = reconciled.Children
			}
			preview.UpdatedAt = time.Now().Format(time.RFC3339)
			state.Items[payload.Key] = preview
			if err := state.Save(); err != nil {
				return mutationOutcome{}, err
			}
			return mutationOutcome{Backend: map[string]interface{}{"cloud_id": reconciled.CloudID, "apple_id": reconciled.AppleID}, CloudID: reconciled.CloudID, AppleID: reconciled.AppleID, Title: preview.Title, Projection: preview}, nil
		case "queue-complete":
			var payload queueKeyPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			return replayQueueTerminal(payload.Key, true)
		case "queue-delete":
			var payload queueKeyPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			return replayQueueTerminal(payload.Key, false)
		case "queue-child-upsert":
			var payload queueChildMutationPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			state, err := queue.LoadState()
			if err != nil {
				return mutationOutcome{}, err
			}
			if err := upsertQueueChild(state, payload.ParentKey, payload.ChildKey, payload.Title, payload.Due, payload.Priority, payload.Flagged); err != nil {
				return mutationOutcome{}, err
			}
			if err := state.Save(); err != nil {
				return mutationOutcome{}, err
			}
			child := state.Items[payload.ParentKey].Children[payload.ChildKey]
			return mutationOutcome{Backend: map[string]interface{}{"cloud_id": child.CloudID, "apple_id": child.AppleID}, CloudID: child.CloudID, AppleID: child.AppleID, Title: child.Title, Projection: child}, nil
		case "queue-child-complete":
			var payload queueChildMutationPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			state, err := queue.LoadState()
			if err != nil {
				return mutationOutcome{}, err
			}
			if err := completeQueueChild(state, payload.ParentKey, payload.ChildKey, false); err != nil {
				return mutationOutcome{}, err
			}
			if err := state.Save(); err != nil {
				return mutationOutcome{}, err
			}
			return mutationOutcome{DeleteProjection: true}, nil
		case "queue-child-delete":
			var payload queueChildMutationPayload
			if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
				return mutationOutcome{}, err
			}
			state, err := queue.LoadState()
			if err != nil {
				return mutationOutcome{}, err
			}
			if err := deleteQueueChild(state, payload.ParentKey, payload.ChildKey, false); err != nil {
				return mutationOutcome{}, err
			}
			if err := state.Save(); err != nil {
				return mutationOutcome{}, err
			}
			return mutationOutcome{DeleteProjection: true}, nil
		default:
			return mutationOutcome{}, fmt.Errorf("unsupported operation kind %q", op.Kind)
		}
	})
}

func replayReminderDelete(op *store.Operation) (mutationOutcome, error) {
	var payload reminderIDPayload
	if err := json.Unmarshal([]byte(op.DesiredJSON), &payload); err != nil {
		return mutationOutcome{}, err
	}
	if err := bestEffortSync(); err != nil && !shouldProceedWithoutSync(payload.ReminderID) {
		return mutationOutcome{}, err
	}
	result, err := w.DeleteReminder(payload.ReminderID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return mutationOutcome{DeleteProjection: true}, nil
		}
		return mutationOutcome{}, err
	}
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		if strings.Contains(strings.ToLower(errMsg), "not found") {
			return mutationOutcome{DeleteProjection: true}, nil
		}
		return mutationOutcome{}, fmt.Errorf("%s", errMsg)
	}
	return mutationOutcome{Backend: result, DeleteProjection: true}, nil
}

func replayQueueTerminal(key string, complete bool) (mutationOutcome, error) {
	state, err := queue.LoadState()
	if err != nil {
		return mutationOutcome{}, err
	}
	item, ok := state.Items[key]
	if !ok {
		return mutationOutcome{DeleteProjection: true}, nil
	}
	for childKey := range item.Children {
		if complete {
			if err := completeQueueChild(state, key, childKey, false); err != nil {
				return mutationOutcome{}, err
			}
		} else {
			if err := deleteQueueChild(state, key, childKey, false); err != nil {
				return mutationOutcome{}, err
			}
		}
	}
	if complete {
		if item.CloudID == "" {
			return mutationOutcome{}, fmt.Errorf("queue item %q has no cloud id to complete", key)
		}
		if _, err := w.CompleteReminder(item.CloudID); err != nil {
			return mutationOutcome{}, err
		}
	} else if item.CloudID != "" {
		if _, err := w.DeleteReminder(item.CloudID); err != nil {
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
}

func resetStaleApplyingOperations(db *sql.DB) error {
	_, err := db.Exec(`
		UPDATE operations
		SET status = 'pending',
		    error_text = CASE
		        WHEN error_text IS NULL OR error_text = '' THEN 'interrupted during apply; reset to pending'
		        ELSE error_text
		    END,
		    updated_at = ?
		WHERE status = 'applying'
	`, time.Now().UTC().Format(time.RFC3339))
	return err
}

func compactSupersededOperations(db *sql.DB) error {
	ops, err := store.ListOperations(db)
	if err != nil {
		return err
	}

	latestByTarget := make(map[string]store.Operation)
	for _, op := range ops {
		target := op.TargetType + "/" + op.TargetKey
		if _, exists := latestByTarget[target]; !exists {
			latestByTarget[target] = op
			continue
		}
		if !isOutstandingOperationStatus(op.Status) {
			continue
		}
		newer := latestByTarget[target]
		op.Status = "superseded"
		op.ErrorText = fmt.Sprintf("superseded by newer %s %s", newer.Kind, newer.ID)
		if op.VerifiedAt == "" {
			op.VerifiedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if err := store.UpdateOperation(db, op); err != nil {
			return err
		}
	}
	return nil
}

func pruneHistoricalOperations(db *sql.DB) error {
	return pruneHistoricalOperationsForTarget(db, "", "")
}

func pruneHistoricalOperationsForTarget(db *sql.DB, targetType, targetKey string) error {
	ops, err := store.ListOperations(db)
	if err != nil {
		return err
	}

	keepVerified := make(map[string]bool)
	deleteIDs := make([]string, 0)
	for _, op := range ops {
		if targetType != "" && (op.TargetType != targetType || op.TargetKey != targetKey) {
			continue
		}
		target := op.TargetType + "/" + op.TargetKey
		switch op.Status {
		case "superseded":
			deleteIDs = append(deleteIDs, op.ID)
		case "verified":
			if keepVerified[target] {
				deleteIDs = append(deleteIDs, op.ID)
			} else {
				keepVerified[target] = true
			}
		}
	}
	if len(deleteIDs) == 0 {
		return nil
	}
	return store.ExecTx(db, func(tx *sql.Tx) error {
		return store.DeleteOperations(tx, deleteIDs)
	})
}

func isOutstandingOperationStatus(status string) bool {
	switch status {
	case "pending", "failed", "applying", "applied", "validator_pending", "reconcile_needed":
		return true
	default:
		return false
	}
}

func init() {
	opsCmd.Flags().BoolVar(&opsAll, "all", false, "Include verified operations")
}
