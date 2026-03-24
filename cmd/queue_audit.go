package cmd

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/queue"
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
			return nil
		})
	},
}

func init() {}
