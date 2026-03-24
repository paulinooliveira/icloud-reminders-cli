package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var (
	addListName string
	addDue      string
	addPriority string
	addNotes    string
	addParent   string
	addSection  string
	addTags     []string
)

var addCmd = &cobra.Command{
	Use:   "add <title>",
	Short: "Add a reminder",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		title := args[0]
		payload := struct {
			Title    string   `json:"title"`
			List     string   `json:"list"`
			Due      string   `json:"due"`
			Priority string   `json:"priority"`
			Notes    string   `json:"notes"`
			Parent   string   `json:"parent"`
			Section  string   `json:"section"`
			Tags     []string `json:"tags,omitempty"`
		}{
			Title: title, List: addListName, Due: addDue, Priority: addPriority, Notes: addNotes,
			Parent: addParent, Section: addSection, Tags: addTags,
		}
		if err := executeMutation("add", "reminder", "", payload, true, func() (mutationOutcome, error) {
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.AddReminder(title, addListName, addDue, addPriority, addNotes, addParent)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			reminderID, _ := result["id"].(string)
			if len(addTags) > 0 {
				tagResult, err := w.SetReminderTags(reminderID, addTags)
				if err != nil {
					return mutationOutcome{}, err
				}
				if errMsg, ok := tagResult["error"].(string); ok {
					return mutationOutcome{}, fmt.Errorf("%s", errMsg)
				}
			}
			if addSection != "" {
				sectionResult, err := w.AssignReminderToSection(reminderID, addListName, addSection)
				if err != nil {
					return mutationOutcome{}, err
				}
				if errMsg, ok := sectionResult["error"].(string); ok {
					return mutationOutcome{}, fmt.Errorf("%s", errMsg)
				}
			}
			var projection map[string]interface{}
			if rd := syncEngine.Cache.Reminders[reminderID]; rd != nil {
				projection = map[string]interface{}{
					"id":         reminderID,
					"title":      rd.Title,
					"completed":  rd.Completed,
					"flagged":    rd.Flagged,
					"due":        rd.Due,
					"priority":   rd.Priority,
					"notes":      rd.Notes,
					"list_ref":   rd.ListRef,
					"parent_ref": rd.ParentRef,
					"deleted":    false,
				}
			}
			return mutationOutcome{
				Backend:    result,
				CloudID:    reminderID,
				Title:      title,
				ListID:     addListName,
				Projection: projection,
			}, nil
		}); err != nil {
			return err
		}
		listStr := ""
		if addListName != "" {
			listStr = fmt.Sprintf(" → %s", addListName)
		}
		parentStr := ""
		if addParent != "" {
			parentStr = fmt.Sprintf(" (subtask of %s)", addParent)
		}
		sectionStr := ""
		if addSection != "" {
			sectionStr = fmt.Sprintf(" [section %s]", addSection)
		}
		tagStr := ""
		if len(addTags) > 0 {
			tagStr = fmt.Sprintf(" [tags %v]", addTags)
		}
		fmt.Printf("✅ Added: '%s'%s%s%s%s\n", title, listStr, parentStr, sectionStr, tagStr)
		return nil
	},
}

var (
	batchListName string
	batchParent   string
	batchSection  string
)

var addBatchCmd = &cobra.Command{
	Use:   "add-batch <title>...",
	Short: "Add multiple reminders at once",
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		payload := struct {
			Titles  []string `json:"titles"`
			List    string   `json:"list"`
			Parent  string   `json:"parent"`
			Section string   `json:"section"`
		}{Titles: args, List: batchListName, Parent: batchParent, Section: batchSection}
		if err := executeMutation("add-batch", "reminder-batch", "", payload, true, func() (mutationOutcome, error) {
			if err := syncEngine.Sync(false); err != nil {
				return mutationOutcome{}, err
			}
			result, err := w.AddRemindersBatch(args, batchListName, batchParent)
			if err != nil {
				return mutationOutcome{}, err
			}
			if errMsg, ok := result["error"].(string); ok {
				return mutationOutcome{}, fmt.Errorf("%s", errMsg)
			}
			if batchSection != "" {
				listID := ""
				if batchListName != "" {
					listID = syncEngine.FindListByName(batchListName)
				}
				if listID == "" {
					listID = batchListName
				}
				var ids []string
				if rawIDs, ok := result["ids"].([]string); ok {
					ids = rawIDs
				}
				if err := w.AssignReminderIDsToSection(listID, batchSection, ids); err != nil {
					return mutationOutcome{}, err
				}
			}
			return mutationOutcome{Backend: result}, nil
		}); err != nil {
			return err
		}
		count := len(args)
		listStr := ""
		if batchListName != "" {
			listStr = fmt.Sprintf(" → %s", batchListName)
		}
		parentStr := ""
		if batchParent != "" {
			parentStr = fmt.Sprintf(" (subtasks of %s)", batchParent)
		}
		sectionStr := ""
		if batchSection != "" {
			sectionStr = fmt.Sprintf(" [section %s]", batchSection)
		}
		fmt.Printf("✅ Added %d reminders%s%s%s:\n", count, listStr, parentStr, sectionStr)
		for _, t := range args {
			fmt.Printf("   • %s\n", t)
		}
		return nil
	},
}

func init() {
	addCmd.Flags().StringVarP(&addListName, "list", "l", "", "List name (required)")
	addCmd.Flags().StringVarP(&addDue, "due", "d", "", "Due date or datetime (YYYY-MM-DD, YYYY-MM-DDTHH:MM, or RFC3339)")
	addCmd.Flags().StringVarP(&addPriority, "priority", "p", "", "Priority (high, medium, low)")
	addCmd.Flags().StringVarP(&addNotes, "notes", "n", "", "Notes")
	addCmd.Flags().StringVar(&addParent, "parent", "", "Parent reminder title or ID (creates subtask)")
	addCmd.Flags().StringVar(&addSection, "section", "", "Existing section name or ID")
	addCmd.Flags().StringSliceVar(&addTags, "tag", nil, "Native tag name(s), without the leading #")
	_ = addCmd.MarkFlagRequired("list")

	addBatchCmd.Flags().StringVarP(&batchListName, "list", "l", "", "List name (required)")
	addBatchCmd.Flags().StringVar(&batchParent, "parent", "", "Parent reminder title or ID (creates subtasks)")
	addBatchCmd.Flags().StringVar(&batchSection, "section", "", "Existing section name or ID")
	_ = addBatchCmd.MarkFlagRequired("list")
}
