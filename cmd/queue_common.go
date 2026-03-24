package cmd

import (
	"fmt"
	"strings"
	"time"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/queue"
	"icloud-reminders/internal/writer"
)

func queueLocation() *time.Location {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		return time.Local
	}
	return loc
}

func buildQueuePreview(state *queue.State, spec queue.Spec, now time.Time) queue.StateItem {
	previewState := &queue.State{
		Bindings: state.Bindings,
		Items:    map[string]queue.StateItem{},
	}
	existing := state.Items[spec.Key]
	previewState.Items[spec.Key] = existing
	queue.UpdateStateItemAt(previewState, spec, existing.AppleID, existing.CloudID, now)
	preview := previewState.Items[spec.Key]
	if preview.Key == "" {
		preview.Key = spec.Key
	}
	return preview
}

func blockedTitle(title string, blocked bool) string {
	trimmed := strings.TrimSpace(title)
	if trimmed == "" {
		return trimmed
	}
	trimmed = strings.TrimSpace(strings.TrimSuffix(trimmed, "[blocked]"))
	if blocked {
		return trimmed + " [blocked]"
	}
	return trimmed
}

func priorityLabelFromValue(value int) string {
	switch value {
	case 1:
		return "high"
	case 5:
		return "medium"
	case 9:
		return "low"
	case 0:
		return ""
	default:
		return ""
	}
}

func finalizeQueueSpec(state *queue.State, spec queue.Spec, now time.Time) (queue.Spec, queue.StateItem) {
	existing := state.Items[spec.Key]
	preview := buildQueuePreview(state, spec, now)
	burn := queue.ComputeBurn(preview, now)
	if queue.NoteNeedsRendering(spec, existing) {
		rendered := queue.RenderNotes(preview, now, queueLocation())
		spec.Notes = &rendered
		if spec.Flagged == nil {
			flagged := queue.NeedsFlag(burn)
			spec.Flagged = &flagged
		}
	}
	spec.Title = blockedTitle(spec.Title, preview.Blocked || burn.Hammer == "hard-stop")
	preview.Title = spec.Title
	if spec.Flagged != nil {
		flagged := *spec.Flagged
		if flagged && burn.Hammer == "green" {
			preview.LastHammer = "manual"
		}
	}
	preview.LastHammer = burn.Hammer
	return spec, preview
}

func reconcileQueueReminder(spec queue.Spec, stateItem queue.StateItem, priorityLabel, listOverride string) (string, string, error) {
	if err := syncEngine.Sync(false); err != nil {
		return "", "", err
	}
	bridge, cfg, err := loadOptionalValidatorBridge()
	if err != nil {
		return "", "", err
	}
	fallbackListID := ""
	listName := "Sebastian"
	if cfg != nil {
		fallbackListID = cfg.SebastianListID
		if cfg.SebastianListName != "" {
			listName = cfg.SebastianListName
		}
	}
	listID := canonicalQueueListID(listOverride, fallbackListID)
	if listID == "" {
		listID = canonicalQueueListID("Sebastian", "")
	}
	if spec.Section != "" {
		if _, err := w.EnsureSection(listID, spec.Section); err != nil {
			return "", "", err
		}
	}

	cloudByUUID := queue.BuildCloudByUUID(syncEngine.GetReminders(true), listID)
	var titleMatches []applebridge.Reminder
	if bridge != nil {
		appItems, err := bridge.ListReminders(listName)
		if err == nil {
			titleMatches = queue.FindExactTitleMatches(appItems, spec.Title)
			if len(titleMatches) == 0 && stateItem.AppleID != "" {
				for _, item := range appItems {
					if item.AppleID == stateItem.AppleID {
						titleMatches = append(titleMatches, item)
						break
					}
				}
			}
		} else {
			bridge = nil
		}
	}
	choice := queue.ChooseCanonical(titleMatches, cloudByUUID, stateItem, spec.Notes)
	for _, dup := range choice.Delete {
		if bridge == nil {
			break
		}
		if err := bridge.DeleteReminder(dup.AppleID); err != nil {
			break
		}
	}

	var cloudID string
	var appleID string
	if choice.Keep != nil {
		appleID = choice.Keep.AppleID
		if cloud := cloudByUUID[strings.ToUpper(choice.Keep.UUID())]; cloud != nil {
			cloudID = cloud.ID
		}
	}
	if cloudID == "" && stateItem.CloudID != "" {
		cloudID = stateItem.CloudID
	}
	if cloudID == "" {
		if candidate := syncEngine.FindReminderByTitle(strings.TrimSuffix(strings.TrimSpace(spec.Title), " [blocked]"), listID, false); candidate != "" {
			cloudID = candidate
		}
	}
	if cloudID == "" {
		res, err := w.AddReminder(spec.Title, listID, deref(spec.Due), priorityLabel, deref(spec.Notes), "")
		if err != nil {
			return "", "", err
		}
		if errMsg, ok := res["error"].(string); ok && errMsg != "" {
			return "", "", fmt.Errorf("%s", errMsg)
		}
		cloudID, _ = res["id"].(string)
	}

	if cloudID == "" {
		if candidate := syncEngine.FindReminderByTitle(spec.Title, listID, false); candidate != "" {
			cloudID = candidate
		}
	}
	if cloudID != "" && (spec.Notes != nil || stateItem.Title != spec.Title) {
		changes := writer.ReminderChanges{Title: &spec.Title}
		if spec.Notes != nil {
			changes.Notes = spec.Notes
		}
		if _, err := w.EditReminder(cloudID, changes); err != nil {
			return "", "", err
		}
	}
	if cloudID != "" {
		if len(spec.Tags) > 0 {
			if _, err := w.SetReminderTags(cloudID, spec.Tags); err != nil {
				return "", "", err
			}
		}
		if spec.Section != "" {
			if _, err := w.AssignReminderToSection(cloudID, listID, spec.Section); err != nil {
				return "", "", err
			}
		}
		changes := writer.ReminderChanges{}
		if spec.Due != nil {
			changes.DueDate = spec.Due
		}
		if spec.Flagged != nil {
			changes.Flagged = spec.Flagged
		}
		if spec.Priority != 0 || priorityLabel == "none" {
			p := spec.Priority
			changes.Priority = &p
		}
		if changes.DueDate != nil || changes.Flagged != nil || changes.Priority != nil {
			if _, err := w.EditReminder(cloudID, changes); err != nil {
				return "", "", err
			}
		}
		if appleID == "" {
			appleID = "x-apple-reminder://" + shortReminderID(cloudID)
		}
	}

	return appleID, cloudID, nil
}
