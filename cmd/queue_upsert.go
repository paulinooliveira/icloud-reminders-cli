package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/queue"
)

var (
	queueSection      string
	queueTags         []string
	queuePriority     string
	queueNotes        string
	queueUnsafeNotes  bool
	queueDue          string
	queueFlagged      bool
	queueUnflagged    bool
	queueList         string
	queueStatus       string
	queueItems        []string
	queueHoursBudget  float64
	queueTokensBudget int64
	queueExecutor     string
	queueBlocked      bool
	queueUnblocked    bool
)

var queueUpsertCmd = &cobra.Command{
	Use:   "queue-upsert <key>",
	Short: "Idempotently create or reconcile one Sebastian queue item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			key := args[0]
			title, _ := cmd.Flags().GetString("title")
			if strings.TrimSpace(title) == "" {
				return fmt.Errorf("--title is required")
			}
			var duePtr *string
			if cmd.Flags().Changed("due") {
				duePtr = &queueDue
			}
			var flaggedPtr *bool
			if queueFlagged || queueUnflagged {
				v := queueFlagged && !queueUnflagged
				flaggedPtr = &v
			}
			var statusPtr *string
			if cmd.Flags().Changed("status") {
				statusPtr = &queueStatus
			}
			var hoursBudgetPtr *float64
			if cmd.Flags().Changed("hours-budget") {
				hoursBudgetPtr = &queueHoursBudget
			}
			var tokensBudgetPtr *int64
			if cmd.Flags().Changed("tokens-budget") {
				tokensBudgetPtr = &queueTokensBudget
			}
			var blockedPtr *bool
			if queueBlocked || queueUnblocked {
				v := queueBlocked && !queueUnblocked
				blockedPtr = &v
			}
			var notesPtr *string
			if cmd.Flags().Changed("notes") {
				if !queueUnsafeNotes {
					return fmt.Errorf("raw queue note overrides are blocked by default: update semantic queue state with --status/--item/--executor/--hours-budget/--tokens-budget/--blocked and let the CLI render notes; use --unsafe-legacy-notes only for manual migration/debugging")
				}
				notesPtr = &queueNotes
			}
			checklist := make([]queue.ChecklistItem, 0, len(queueItems))
			for _, line := range queueItems {
				item, err := queue.ParseChecklistLine(line)
				if err != nil {
					return err
				}
				checklist = append(checklist, item)
			}
			spec := queue.Spec{
				Key: key, Title: title,
				Section: queueSection, Tags: queueTags, Priority: priorityFromLabel(queuePriority),
				Notes: notesPtr, Due: duePtr, Flagged: flaggedPtr, StatusLine: statusPtr,
				Checklist: checklist, HoursBudget: hoursBudgetPtr, TokensBudget: tokensBudgetPtr,
				Executor: strings.TrimSpace(queueExecutor), Blocked: blockedPtr,
			}
			payload := struct {
				Spec          queue.Spec `json:"spec"`
				PriorityLabel string     `json:"priority_label"`
				ListOverride  string     `json:"list_override"`
			}{Spec: spec, PriorityLabel: queuePriority, ListOverride: queueList}
			if err := executeMutation("queue-upsert", "queue", key, payload, false, func() (mutationOutcome, error) {
				state, err := queue.LoadState()
				if err != nil {
					return mutationOutcome{}, err
				}
				now := time.Now()
				finalSpec, preview := finalizeQueueSpec(state, spec, now)
				reconciled, err := reconcileQueueReminder(finalSpec, state.Items[key], queuePriority, queueList)
				if err != nil {
					return mutationOutcome{}, err
				}
				preview.AppleID = reconciled.AppleID
				preview.CloudID = reconciled.CloudID
				if reconciled.Children != nil {
					preview.Children = reconciled.Children
				}
				preview.UpdatedAt = now.Format(time.RFC3339)
				state.Items[key] = preview
				if err := state.Save(); err != nil {
					return mutationOutcome{}, err
				}
				return mutationOutcome{
					Backend:    map[string]interface{}{"cloud_id": reconciled.CloudID, "apple_id": reconciled.AppleID},
					CloudID:    reconciled.CloudID,
					AppleID:    reconciled.AppleID,
					Title:      preview.Title,
					Projection: preview,
				}, nil
			}); err != nil {
				return err
			}
			fmt.Printf("✅ Queue upserted: %s [%s]\n", title, key)
			return nil
		})
	},
}

func priorityFromLabel(label string) int {
	switch label {
	case "high":
		return 1
	case "medium":
		return 5
	case "low":
		return 9
	case "none":
		return 0
	default:
		return 0
	}
}

func deref(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func canonicalQueueListID(explicit, fallback string) string {
	candidate := explicit
	if candidate == "" {
		candidate = fallback
	}
	if candidate == "" {
		return ""
	}
	if rid := syncEngine.FindListByName(candidate); rid != "" {
		return rid
	}
	if strings.Contains(candidate, "/") {
		return candidate
	}
	return "List/" + candidate
}

func init() {
	queueUpsertCmd.Flags().String("title", "", "Reminder title")
	queueUpsertCmd.Flags().StringVar(&queueSection, "section", "", "Native section name")
	queueUpsertCmd.Flags().StringSliceVar(&queueTags, "tag", nil, "Native tag(s)")
	queueUpsertCmd.Flags().StringVar(&queuePriority, "priority", "", "Priority (high, medium, low, none)")
	queueUpsertCmd.Flags().StringVar(&queueNotes, "notes", "", "Legacy reminder notes/body override (debug only)")
	queueUpsertCmd.Flags().BoolVar(&queueUnsafeNotes, "unsafe-legacy-notes", false, "Allow raw legacy queue note override instead of deterministic rendering")
	queueUpsertCmd.Flags().StringVar(&queueDue, "due", "", "Due date or datetime")
	queueUpsertCmd.Flags().BoolVar(&queueFlagged, "flagged", false, "Flag reminder")
	queueUpsertCmd.Flags().BoolVar(&queueUnflagged, "unflagged", false, "Unflag reminder")
	queueUpsertCmd.Flags().StringVar(&queueStatus, "status", "", "Structured status line")
	queueUpsertCmd.Flags().StringSliceVar(&queueItems, "item", nil, "Structured checklist item like '[ ] text'")
	queueUpsertCmd.Flags().Float64Var(&queueHoursBudget, "hours-budget", 0, "Hours appetite budget")
	queueUpsertCmd.Flags().Int64Var(&queueTokensBudget, "tokens-budget", 0, "Token appetite budget")
	queueUpsertCmd.Flags().StringVar(&queueExecutor, "executor", "", "Current top-level executor agent id")
	queueUpsertCmd.Flags().BoolVar(&queueBlocked, "blocked", false, "Mark task blocked")
	queueUpsertCmd.Flags().BoolVar(&queueUnblocked, "unblocked", false, "Clear blocked state")
	queueUpsertCmd.Flags().StringVarP(&queueList, "list", "l", "", "List id or name (defaults to Sebastian queue)")
	_ = queueUpsertCmd.Flags().MarkHidden("notes")
}
