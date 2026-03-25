package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/queue"
	"icloud-reminders/internal/store"
)

var queueAuditCmd = &cobra.Command{
	Use:   "queue-audit",
	Short: "Audit Sebastian queue consistency across CloudKit and Apple Reminders app",
	RunE: func(cmd *cobra.Command, args []string) error {
		return queue.WithQueueLock(func() error {
			if err := syncEngine.Sync(false); err != nil {
				return err
			}
			bridge, cfg, err := loadOptionalValidatorBridge()
			if err != nil {
				return err
			}
			listID := canonicalQueueListID("", "")
			listName := "Sebastian"
			if cfg != nil {
				listID = canonicalQueueListID("", cfg.SebastianListID)
				if cfg.SebastianListName != "" {
					listName = cfg.SebastianListName
				}
			}
			var appItems []applebridge.Reminder
			if bridge != nil {
				appItems, err = bridge.ListReminders(listName)
				if err != nil {
					return err
				}
			}
			cloudByUUID := queue.BuildCloudByUUID(syncEngine.GetReminders(true), listID)
			state, err := queue.LoadState()
			if err != nil {
				return err
			}
			managedCloud := map[string]string{}
			for key, item := range state.Items {
				if item.CloudID != "" {
					managedCloud[strings.ToUpper(shortReminderID(item.CloudID))] = key
				}
				for _, child := range item.Children {
					if child.CloudID != "" {
						managedCloud[strings.ToUpper(shortReminderID(child.CloudID))] = key + "/" + child.Key
					}
				}
			}
			byTitle := map[string][]applebridge.Reminder{}
			for _, item := range appItems {
				byTitle[item.Title] = append(byTitle[item.Title], item)
			}
			titles := make([]string, 0, len(byTitle))
			for title := range byTitle {
				titles = append(titles, title)
			}
			sort.Strings(titles)
			fmt.Printf("Queue audit for %s\n", listName)
			for _, title := range titles {
				items := byTitle[title]
				status := "ok"
				if len(items) > 1 {
					status = "duplicate-visible"
				}
				appOnly := 0
				for _, item := range items {
					if _, ok := cloudByUUID[strings.ToUpper(item.UUID())]; !ok {
						appOnly++
					}
				}
				if appOnly > 0 {
					if status == "ok" {
						status = "app-only"
					} else {
						status += "+app-only"
					}
				}
				fmt.Printf("- %s: %s (%d visible)\n", title, status, len(items))
				for _, item := range items {
					cloud := "app-only"
					if _, ok := cloudByUUID[strings.ToUpper(item.UUID())]; ok {
						cloud = "cloud-backed"
					}
					fmt.Printf("  %s %s\n", item.UUID(), cloud)
				}
			}
			var unmanaged []string
			for _, reminder := range syncEngine.GetReminders(true) {
				if reminder == nil || reminder.Completed || reminder.ListRef == nil || *reminder.ListRef != listID {
					continue
				}
				shortID := strings.ToUpper(reminder.ShortID())
				if _, ok := managedCloud[shortID]; ok {
					continue
				}
				kind := "top-level"
				if reminder.ParentRef != nil && strings.TrimSpace(*reminder.ParentRef) != "" {
					kind = "child"
				}
				unmanaged = append(unmanaged, fmt.Sprintf("- unmanaged %s: %s (%s)", kind, reminder.Title, reminder.ShortID()))
			}
			sort.Strings(unmanaged)
			if len(unmanaged) > 0 {
				fmt.Printf("Unmanaged active Sebastian items: %d\n", len(unmanaged))
				for _, line := range unmanaged {
					fmt.Println(line)
				}
			}
			db, err := store.Open()
			if err != nil {
				return err
			}
			defer db.Close()
			ops, err := store.ListOperations(db, "pending", "failed", "applying", "applied", "validator_pending", "reconcile_needed")
			if err != nil {
				return err
			}
			if len(ops) > 0 {
				fmt.Printf("Outstanding mutation operations: %d\n", len(ops))
				for _, op := range ops {
					line := fmt.Sprintf("- %s %s %s/%s", op.Status, op.Kind, op.TargetType, op.TargetKey)
					if op.ErrorText != "" {
						line += " :: " + op.ErrorText
					}
					fmt.Println(line)
				}
			}
			return nil
		})
	},
}

func init() {}
