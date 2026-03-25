package cmd

import (
	"fmt"
	"strings"
	"time"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/queue"
	"icloud-reminders/internal/writer"
	"icloud-reminders/pkg/models"
)

type queueReminderValidator interface {
	GetReminder(appleID string) (*applebridge.Reminder, error)
	UpdateReminder(appleID string, title, body *string, completed *bool) error
	DeleteReminder(appleID string) error
}

type queueReconcileResult struct {
	AppleID   string
	CloudID   string
	Children  map[string]queue.ChildStateItem
	Created   bool
	Rewritten bool
}

func canProceedWithoutQueueSync(stateItem queue.StateItem) bool {
	// Queue upserts are allowed to proceed without a prior sync as long as we
	// have a stable identifier (cloud id or apple id). This keeps the system
	// responsive and avoids "sync gate" stalls when the sync path is flaky.
	return resolveQueueCloudID(stateItem.CloudID) != "" || strings.TrimSpace(stateItem.AppleID) != ""
}

func shouldQueryQueueValidatorList(stateItem queue.StateItem) bool {
	return strings.TrimSpace(stateItem.AppleID) == "" && strings.TrimSpace(stateItem.CloudID) == ""
}

func cleanupQueueEmptySections(listID string) error {
	if strings.TrimSpace(listID) == "" {
		return nil
	}
	_, err := w.SweepEmptySections(listID)
	return err
}

func queueLocation() *time.Location {
	loc, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		return time.Local
	}
	return loc
}

func resolveQueueCloudID(hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToUpper(hint), "REMINDER/") && strings.TrimSpace(shortReminderID(hint)) != "" {
		uuid := strings.TrimSpace(shortReminderID(hint))
		if !looksLikeUUID(uuid) {
			return ""
		}
		return "Reminder/" + strings.ToUpper(uuid)
	}
	if looksLikeUUID(hint) {
		return "Reminder/" + strings.ToUpper(hint)
	}
	return ""
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

func reconcileQueueReminder(spec queue.Spec, stateItem queue.StateItem, priorityLabel, listOverride string) (queueReconcileResult, error) {
	if !canProceedWithoutQueueSync(stateItem) {
		if err := syncEngine.Sync(false); err != nil {
			return queueReconcileResult{}, err
		}
	}
	bridge, cfg, err := loadOptionalValidatorBridge()
	if err != nil {
		return queueReconcileResult{}, err
	}
	fallbackListID := ""
	if cfg != nil {
		fallbackListID = cfg.SebastianListID
	}
	listID := canonicalQueueListID(listOverride, fallbackListID)
	if listID == "" {
		listID = canonicalQueueListID("Sebastian", "")
	}
	if spec.Section != "" {
		if _, err := w.EnsureSection(listID, spec.Section); err != nil {
			return queueReconcileResult{}, err
		}
	}

	var cloudID string
	var appleID string
	createdNew := false
	cloudID = resolveQueueCloudID(stateItem.CloudID)
	appleID = strings.TrimSpace(stateItem.AppleID)
	if cloudID == "" {
		res, err := w.AddReminder(spec.Title, listID, deref(spec.Due), priorityLabel, deref(spec.Notes), "")
		if err != nil {
			return queueReconcileResult{}, err
		}
		if errMsg, ok := res["error"].(string); ok && errMsg != "" {
			return queueReconcileResult{}, fmt.Errorf("%s", errMsg)
		}
		cloudID, _ = res["id"].(string)
		createdNew = cloudID != ""
	}

	if cloudID != "" {
		textNeedsRewrite := !createdNew && (spec.Notes != nil || stateItem.Title != spec.Title)
		rewritten := false
		children := stateItem.Children
		if textNeedsRewrite {
			// Never mutate title/notes in place: Apple’s CRDT merge can duplicate/corrupt.
			// Instead: recreate (create-new + delete-old) and hard-dedupe residuals.
			if err := syncEngine.Sync(false); err != nil && !shouldProceedWithoutSync(cloudID) {
				return queueReconcileResult{}, err
			}
			oldCloudID := cloudID
			newAppleID, newCloudID, newChildren, err := recreateQueueReminderTree(oldCloudID, listID, spec, priorityLabel, children)
			if err != nil {
				return queueReconcileResult{}, err
			}
			appleID = newAppleID
			cloudID = newCloudID
			children = newChildren
			createdNew = true
			rewritten = true

		}

		changes := writer.ReminderChanges{}
		needsEdit := false
		if !createdNew && spec.Due != nil {
			if stateItem.Due == nil || *stateItem.Due != *spec.Due {
				changes.DueDate = spec.Due
				needsEdit = true
			}
		}
		if spec.Flagged != nil {
			if !createdNew || *spec.Flagged {
				changes.Flagged = spec.Flagged
				needsEdit = true
			}
		}
		if spec.Priority != 0 || priorityLabel == "none" {
			if !createdNew || stateItem.Priority != spec.Priority {
				p := spec.Priority
				changes.Priority = &p
				needsEdit = true
			}
		}
		if needsEdit {
			if _, err := w.EditReminder(cloudID, changes); err != nil {
				return queueReconcileResult{}, err
			}
		}
		if len(spec.Tags) > 0 {
			if _, err := w.SetReminderTags(cloudID, spec.Tags); err != nil {
				return queueReconcileResult{}, err
			}
		}
		if spec.Section != "" {
			if _, err := w.AssignReminderToSection(cloudID, listID, spec.Section); err != nil {
				return queueReconcileResult{}, err
			}
		}
		if appleID == "" {
			appleID = "x-apple-reminder://" + shortReminderID(cloudID)
		}
		// Final convergence pass: enforce a single top-level reminder for this queue item title.
		// This is intentionally strict (Shape Up: don't babysit the system).
		if err := syncEngine.Sync(false); err != nil && !shouldProceedWithoutSync(cloudID) {
			return queueReconcileResult{}, err
		}
		if err := repairUniqueTitleInList(listID, spec.Title, cloudID); err != nil {
			return queueReconcileResult{}, err
		}
		var validator queueReminderValidator
		if bridge != nil {
			validator = bridge
		}
		return finalizeQueueReconcileResult(queueReconcileResult{
			AppleID:   appleID,
			CloudID:   cloudID,
			Children:  children,
			Created:   createdNew,
			Rewritten: rewritten,
		}, validator, spec.Title, spec.Notes, listID)
	}
	var validator queueReminderValidator
	if bridge != nil {
		validator = bridge
	}
	return finalizeQueueReconcileResult(queueReconcileResult{
		AppleID:   appleID,
		CloudID:   cloudID,
		Children:  stateItem.Children,
		Created:   createdNew,
		Rewritten: false,
	}, validator, spec.Title, spec.Notes, listID)
}

func finalizeQueueReconcileResult(res queueReconcileResult, bridge queueReminderValidator, title string, notes *string, listID string) (queueReconcileResult, error) {
	if bridge != nil && strings.TrimSpace(res.AppleID) != "" {
		if err := verifyQueueReminderValidatorText(bridge, res.AppleID, title, notes); err != nil {
			// Validator mismatches are treated as projection lag; the queue state remains canonical.
			// Surface the error upstream so callers can decide if/when to retry/repair.
			return res, err
		}
	}
	if err := cleanupQueueEmptySections(listID); err != nil {
		return res, err
	}
	return res, nil
}

func verifyQueueReminderValidatorText(bridge queueReminderValidator, appleID, title string, notes *string) error {
	if bridge == nil || strings.TrimSpace(appleID) == "" {
		return nil
	}
	live, err := bridge.GetReminder(appleID)
	if err != nil {
		return err
	}
	if live == nil {
		return fmt.Errorf("validator returned no reminder for %s", appleID)
	}
	if live.Title != title {
		return fmt.Errorf("validator mismatch for %s: title mismatch", appleID)
	}
	if notes != nil && live.Body != *notes {
		return fmt.Errorf("validator mismatch for %s: notes mismatch", appleID)
	}
	return nil
}

func repairUniqueTitleInList(listID, expectedTitle, keepCloudID string) error {
	expected := strings.TrimSpace(expectedTitle)
	if expected == "" || strings.TrimSpace(listID) == "" {
		return nil
	}

	// Titles we consider "the same item" for convergence purposes.
	base := strings.TrimSpace(strings.TrimSuffix(expected, " [blocked]"))
	var wanted []string
	if base != "" {
		wanted = append(wanted, base)
		wanted = append(wanted, base+" [blocked]")
	}
	if expected != base {
		wanted = append(wanted, expected)
	}

	isWanted := func(title string) bool {
		t := strings.TrimSpace(title)
		for _, w := range wanted {
			if t == w || isRepeatedTitleVariant(t, w) {
				return true
			}
		}
		return false
	}

	for pass := 0; pass < 4; pass++ {
		// Force sync so we see the full post-write surface before deciding what to delete.
		if err := syncEngine.Sync(true); err != nil {
			return err
		}
		var candidates []*models.Reminder
		for _, r := range syncEngine.GetReminders(true) {
			if r == nil || r.Completed {
				continue
			}
			if r.ListRef == nil || *r.ListRef != listID {
				continue
			}
			if r.ParentRef != nil && strings.TrimSpace(*r.ParentRef) != "" {
				continue
			}
			if !isWanted(r.Title) {
				continue
			}
			candidates = append(candidates, r)
		}
		if len(candidates) <= 1 {
			return nil
		}

		keep := chooseCanonicalCloudMatch(candidates, keepCloudID)
		if keep == nil {
			keep = candidates[0]
		}
		for _, r := range candidates {
			if r == nil || sameReminderID(r.ID, keep.ID) {
				continue
			}
			if err := deleteCloudRecordStrict(r.ID); err != nil {
				return err
			}
		}
		// Small settle period after deletes.
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("failed to converge single reminder for %q in %s", expectedTitle, listID)
}

func recreateQueueReminderTree(oldCloudID, listID string, spec queue.Spec, priorityLabel string, children map[string]queue.ChildStateItem) (string, string, map[string]queue.ChildStateItem, error) {
	res, err := w.AddReminder(spec.Title, listID, deref(spec.Due), priorityLabel, deref(spec.Notes), "")
	if err != nil {
		return "", "", nil, err
	}
	if errMsg, ok := res["error"].(string); ok && errMsg != "" {
		return "", "", nil, fmt.Errorf("%s", errMsg)
	}
	newCloudID, _ := res["id"].(string)
	if strings.TrimSpace(newCloudID) == "" {
		return "", "", nil, fmt.Errorf("queue reminder recreate did not return a new reminder id")
	}
	newAppleID := "x-apple-reminder://" + shortReminderID(newCloudID)

	if spec.Flagged != nil && *spec.Flagged {
		if _, err := w.EditReminder(newCloudID, writer.ReminderChanges{Flagged: spec.Flagged}); err != nil {
			return "", "", nil, err
		}
	}

	newChildren := map[string]queue.ChildStateItem{}
	if len(children) > 0 {
		for childKey, child := range children {
			title := strings.TrimSpace(child.Title)
			if title == "" {
				continue
			}
			childRes, err := w.AddReminder(title, listID, deref(child.Due), priorityLabelFromValue(child.Priority), "", newCloudID)
			if err != nil {
				return "", "", nil, err
			}
			if errMsg, ok := childRes["error"].(string); ok && errMsg != "" {
				return "", "", nil, fmt.Errorf("%s", errMsg)
			}
			childCloudID, _ := childRes["id"].(string)
			if strings.TrimSpace(childCloudID) == "" {
				return "", "", nil, fmt.Errorf("queue reminder recreate child did not return a new reminder id")
			}
			if child.Flagged {
				flagged := true
				if _, err := w.EditReminder(childCloudID, writer.ReminderChanges{Flagged: &flagged}); err != nil {
					return "", "", nil, err
				}
			}
			child.AppleID = "x-apple-reminder://" + shortReminderID(childCloudID)
			child.CloudID = childCloudID
			newChildren[childKey] = child
		}
	}

	// Resolve and delete the old parent reminder last, after the new subtree exists.
	if strings.TrimSpace(oldCloudID) != "" {
		if err := deleteCloudChildrenForParent(oldCloudID); err != nil {
			return "", "", nil, err
		}
		if err := deleteCloudIDIfPresent(oldCloudID); err != nil {
			return "", "", nil, err
		}
	}
	return newAppleID, newCloudID, newChildren, nil
}

func deleteCloudChildrenForParent(parentIDHint string) error {
	if strings.TrimSpace(parentIDHint) == "" {
		return nil
	}
	parentID := syncEngine.FindReminderByID(shortReminderID(parentIDHint))
	if parentID == "" {
		parentID = parentIDHint
	}
	for _, r := range syncEngine.GetReminders(true) {
		if r == nil || r.ParentRef == nil || strings.TrimSpace(*r.ParentRef) == "" {
			continue
		}
		if !sameReminderID(*r.ParentRef, parentID) {
			continue
		}
		if err := deleteCloudRecordStrict(r.ID); err != nil {
			return err
		}
	}
	return nil
}

func deleteCloudIDIfPresent(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	candidate := syncEngine.FindReminderByID(shortReminderID(id))
	if candidate == "" {
		candidate = id
	}
	result, err := w.DeleteReminder(candidate)
	if err != nil {
		return err
	}
	if errMsg, ok := result["error"].(string); ok && errMsg != "" {
		return fmt.Errorf("%s", errMsg)
	}
	return nil
}

func deleteCloudRecordStrict(id string) error {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		result, err := w.DeleteReminder(id)
		if err != nil {
			return err
		}
		if v, ok := result["error"]; ok {
			msg := fmt.Sprintf("%v", v)
			lastErr = fmt.Errorf("%s", msg)
			// CloudKit can transiently refuse deletes while records are locked/settling.
			if strings.Contains(msg, "OP_LOCK_FAILURE") || strings.Contains(strings.ToLower(msg), "oplock") {
				_ = syncEngine.Sync(false)
				time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
				continue
			}
			return lastErr
		}
		if missing, ok := result["missing"].(bool); ok && missing {
			return fmt.Errorf("delete reported missing for live id %s", id)
		}
		return nil
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("delete failed for %s", id)
}

func isRepeatedTitleVariant(title, expected string) bool {
	if expected == "" {
		return false
	}
	for n := 2; n <= 4; n++ {
		if title == strings.Repeat(expected, n) {
			return true
		}
	}
	return false
}

func chooseCanonicalCloudMatch(matches []*models.Reminder, preferredCloudID string) *models.Reminder {
	for _, r := range matches {
		if r == nil {
			continue
		}
		if preferredCloudID != "" && sameReminderID(r.ID, preferredCloudID) {
			return r
		}
	}
	if len(matches) == 0 {
		return nil
	}
	best := matches[0]
	bestTS := int64(0)
	if best != nil && best.ModifiedTS != nil {
		bestTS = *best.ModifiedTS
	}
	for _, r := range matches[1:] {
		if r == nil || r.ModifiedTS == nil {
			continue
		}
		if *r.ModifiedTS > bestTS {
			best = r
			bestTS = *r.ModifiedTS
		}
	}
	return best
}

func sameReminderID(a, b string) bool {
	return strings.EqualFold(shortReminderID(a), shortReminderID(b))
}
