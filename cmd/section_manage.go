package cmd

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var (
	ensureSectionList string
	renameSectionList string
	renameSectionName string
	deleteSectionList string
	deleteSectionForce bool
	cleanupSectionList string
)

var ensureSectionCmd = &cobra.Command{
	Use:   "ensure-section <name>",
	Short: "Create a section if it does not already exist",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if ensureSectionList == "" {
			return fmt.Errorf("--list is required")
		}
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		result, err := w.EnsureSection(ensureSectionList, args[0])
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		if existing, _ := result["existing"].(bool); existing {
			fmt.Printf("♻️  Section exists: %s\n", args[0])
			return nil
		}
		fmt.Printf("✅ Created section: %s\n", args[0])
		return nil
	},
}

var renameSectionCmd = &cobra.Command{
	Use:   "rename-section <section>",
	Short: "Rename an existing section",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if renameSectionList == "" {
			return fmt.Errorf("--list is required")
		}
		if renameSectionName == "" {
			return fmt.Errorf("--name is required")
		}
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		result, err := w.RenameSection(renameSectionList, args[0], renameSectionName)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		fmt.Printf("✅ Renamed section: %s → %s\n", args[0], renameSectionName)
		return nil
	},
}

var deleteSectionCmd = &cobra.Command{
	Use:   "delete-section <section>",
	Short: "Delete a section",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if deleteSectionList == "" {
			return fmt.Errorf("--list is required")
		}
		if err := syncEngine.Sync(false); err != nil {
			return err
		}
		result, err := w.DeleteSection(deleteSectionList, args[0], deleteSectionForce)
		if err != nil {
			return err
		}
		if errMsg, ok := result["error"].(string); ok {
			return fmt.Errorf("%s", errMsg)
		}
		if cleared, _ := result["cleared_count"].(int); cleared > 0 {
			fmt.Printf("✅ Deleted section: %s (cleared %d memberships)\n", args[0], cleared)
			return nil
		}
		fmt.Printf("✅ Deleted section: %s\n", args[0])
		return nil
	},
}

var cleanupEmptySectionsCmd = &cobra.Command{
	Use:   "cleanup-empty-sections",
	Short: "Delete all empty sections from a list",
	RunE: func(cmd *cobra.Command, args []string) error {
		if cleanupSectionList == "" {
			return fmt.Errorf("--list is required")
		}
		if !canProceedWithoutSectionSync(cleanupSectionList) {
			if err := bestEffortSync(); err != nil {
				return err
			}
		}
		deleted, err := w.SweepEmptySections(cleanupSectionList)
		if err != nil {
			return err
		}
		if len(deleted) == 0 {
			fmt.Println("✅ No empty sections to clean.")
			return nil
		}
		fmt.Printf("✅ Deleted empty sections: %s\n", strings.Join(deleted, ", "))
		return nil
	},
}

func canProceedWithoutSectionSync(listHint string) bool {
	return strings.HasPrefix(listHint, "List/") || looksLikeReminderUUID(listHint)
}

func init() {
	ensureSectionCmd.Flags().StringVarP(&ensureSectionList, "list", "l", "", "List name or ID")

	renameSectionCmd.Flags().StringVarP(&renameSectionList, "list", "l", "", "List name or ID")
	renameSectionCmd.Flags().StringVar(&renameSectionName, "name", "", "New section name")

	deleteSectionCmd.Flags().StringVarP(&deleteSectionList, "list", "l", "", "List name or ID")
	deleteSectionCmd.Flags().BoolVar(&deleteSectionForce, "force", false, "Clear memberships before deleting the section")

	cleanupEmptySectionsCmd.Flags().StringVarP(&cleanupSectionList, "list", "l", "", "List name or ID")
}
