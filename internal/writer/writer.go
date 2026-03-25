// Package writer handles write operations for iCloud Reminders.
package writer

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"icloud-reminders/internal/applebridge"
	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/cloudkit"
	"icloud-reminders/internal/logger"
	"icloud-reminders/internal/sync"
	"icloud-reminders/internal/utils"
	"icloud-reminders/pkg/models"
)

// Writer handles creating and modifying reminders.
type Writer struct {
	CK   *cloudkit.Client
	Sync *sync.Engine
}

// ReminderChanges carries explicit field updates for a reminder.
// Nil means unchanged; a non-nil empty string means clear where supported.
type ReminderChanges struct {
	Title      *string
	DueDate    *string
	Notes      *string
	Priority   *int
	Completed  *bool
	Flagged    *bool
	HashtagIDs *[]string
	ParentRef  *string
}

type timedDueArtifacts struct {
	AlarmID           string
	AlarmRecordName   string
	TriggerID         string
	TriggerRecordName string
	TimeZone          string
}

var deleteVerifyBackoff = []time.Duration{0, 150 * time.Millisecond, 400 * time.Millisecond}

// New creates a new Writer.
func New(ck *cloudkit.Client, engine *sync.Engine) *Writer {
	return &Writer{CK: ck, Sync: engine}
}

// CreateList creates a new reminder list when it does not already exist.
func (w *Writer) CreateList(name string) (map[string]interface{}, error) {
	if name == "" {
		return errResult(fmt.Errorf("list name cannot be empty")), nil
	}
	if existing := w.Sync.FindListByName(name); existing != "" {
		return map[string]interface{}{
			"existing": true,
			"list_id":  existing,
			"name":     name,
		}, nil
	}

	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	op, recordName := buildCreateListOp(name)

	logger.Debugf("create-list: creating record %s", recordName)
	result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{op})
	if err != nil {
		return errResult(err), nil
	}

	w.Sync.Cache.Lists[recordName] = name
	if err := w.Sync.Cache.Save(); err != nil {
		logger.Warnf("cache save failed: %v", err)
	}

	logger.Infof("Created list: %q", name)
	result["list_id"] = recordName
	result["name"] = name
	return result, nil
}

// EnsureParentReminder ensures a top-level reminder exists as a stable anchor.
func (w *Writer) EnsureParentReminder(title, listName, notes string) (map[string]interface{}, error) {
	if title == "" {
		return errResult(fmt.Errorf("parent title cannot be empty")), nil
	}
	listID := ""
	if listName != "" {
		listID = w.Sync.FindListByName(listName)
		if listID == "" {
			return errResult(fmt.Errorf("list '%s' not found", listName)), nil
		}
	}
	if existing := w.Sync.FindReminderByTitle(title, listID, true); existing != "" {
		return map[string]interface{}{
			"existing": true,
			"id":       existing,
			"title":    title,
			"list_id":  listID,
		}, nil
	}
	return w.AddReminder(title, listName, "", "none", notes, "")
}

// ownerID returns the cached or fetched owner ID.
func (w *Writer) ownerID() (string, error) {
	if w.Sync.Cache.OwnerID != nil && *w.Sync.Cache.OwnerID != "" {
		return *w.Sync.Cache.OwnerID, nil
	}
	id, err := w.CK.GetOwnerID()
	if err != nil {
		return "", err
	}
	w.Sync.Cache.OwnerID = &id
	return id, nil
}

// AddReminder adds a single reminder.
func (w *Writer) AddReminder(title, listName, dueDate, priority, notes, parentID string) (map[string]interface{}, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	listID := ""
	if listName != "" {
		listID = w.Sync.FindListByName(listName)
		if listID == "" {
			return errResult(fmt.Errorf("list '%s' not found", listName)), nil
		}
	}

	parentRef := ""
	if parentID != "" {
		parentRef = resolveParentRef(w.Sync, parentID, listID)
		if parentRef == "" {
			return errResult(fmt.Errorf("parent reminder '%s' not found", parentID)), nil
		}
		// Inherit list from parent if not specified
		if listID == "" {
			if pd := w.Sync.Cache.Reminders[parentRef]; pd != nil && pd.ListRef != nil {
				listID = *pd.ListRef
			}
		}
	}

	priorityVal := models.PriorityMap[priority]

	ops, recordName, err := buildCreateOp(ownerID, title, listID, parentRef, dueDate, priorityVal, notes)
	if err != nil {
		return errResult(err), nil
	}

	logger.Debugf("add: creating record %s in list %s", recordName, listID)
	result, err := w.modifyRecordsWithRetry(ownerID, ops)
	if err != nil {
		return errResult(err), nil
	}

	logger.Infof("Created reminder: %q → %s", title, listName)
	// Update cache
	rd := &cache.ReminderData{
		Title:    title,
		Priority: priorityVal,
	}
	if dueDate != "" {
		rd.Due = &dueDate
	}
	if notes != "" {
		rd.Notes = &notes
	}
	if listID != "" {
		rd.ListRef = &listID
	}
	if parentRef != "" {
		rd.ParentRef = &parentRef
	}
	ts := time.Now().UnixMilli()
	rd.ModifiedTS = &ts
	// Extract recordChangeTag from response so the reminder can be
	// immediately completed/deleted without requiring a sync first.
	if records, ok := result["records"].([]interface{}); ok && len(records) > 0 {
		if rec, ok := records[0].(map[string]interface{}); ok {
			if ct, ok := rec["recordChangeTag"].(string); ok && ct != "" {
				rd.ChangeTag = &ct
			}
		}
	}
	w.Sync.Cache.Reminders[recordName] = rd
	if err := w.Sync.Cache.Save(); err != nil {
		logger.Warnf("cache save failed: %v", err)
	}

	result["id"] = recordName
	return result, nil
}

// AddRemindersBatch adds multiple reminders in a single CloudKit request.
func (w *Writer) AddRemindersBatch(titles []string, listName, parentID string) (map[string]interface{}, error) {
	if len(titles) == 0 {
		return errResult(fmt.Errorf("no titles provided")), nil
	}

	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	listID := ""
	if listName != "" {
		listID = w.Sync.FindListByName(listName)
		if listID == "" {
			return errResult(fmt.Errorf("list '%s' not found", listName)), nil
		}
	}

	parentRef := ""
	if parentID != "" {
		parentRef = resolveParentRef(w.Sync, parentID, listID)
		if parentRef == "" {
			return errResult(fmt.Errorf("parent reminder '%s' not found", parentID)), nil
		}
		if listID == "" {
			if pd := w.Sync.Cache.Reminders[parentRef]; pd != nil && pd.ListRef != nil {
				listID = *pd.ListRef
			}
		}
	}

	type created struct {
		recordName string
		title      string
	}
	var ops []map[string]interface{}
	var createdList []created

	for _, title := range titles {
		createOps, recordName, err := buildCreateOp(ownerID, title, listID, parentRef, "", 0, "")
		if err != nil {
			return errResult(err), nil
		}
		ops = append(ops, createOps...)
		createdList = append(createdList, created{recordName, title})
	}

	logger.Debugf("add-batch: creating %d records in list %s", len(ops), listID)
	result, err := w.modifyRecordsWithRetry(ownerID, ops)
	if err != nil {
		return errResult(err), nil
	}

	logger.Infof("Created %d reminders in %q", len(createdList), listName)
	now := time.Now().UnixMilli()
	for _, c := range createdList {
		rd := &cache.ReminderData{
			Title:      c.title,
			ModifiedTS: &now,
		}
		if listID != "" {
			rd.ListRef = &listID
		}
		if parentRef != "" {
			rd.ParentRef = &parentRef
		}
		w.Sync.Cache.Reminders[c.recordName] = rd
	}
	if err := w.Sync.Cache.Save(); err != nil {
		logger.Warnf("cache save failed: %v", err)
	}
	var titleList []string
	var idList []string
	for _, c := range createdList {
		titleList = append(titleList, c.title)
		idList = append(idList, c.recordName)
	}
	result["created_count"] = len(createdList)
	result["titles"] = titleList
	result["ids"] = idList

	return result, nil
}

// CompleteReminder marks a reminder as complete.
func (w *Writer) CompleteReminder(reminderID string) (map[string]interface{}, error) {
	completed := true
	return w.EditReminder(reminderID, ReminderChanges{Completed: &completed})
}

// DeleteReminder deletes a reminder.
func (w *Writer) DeleteReminder(reminderID string) (map[string]interface{}, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	fullID, rd, err := w.resolveReminderRecord(reminderID)
	if err != nil {
		// Deletion should be idempotent for ID-like hints: if the record is already
		// gone, converge by cleaning any local cache aliases and returning success.
		if isReminderIDHint(reminderID) {
			w.deleteReminderCacheAliases(reminderID)
			if err := w.Sync.Cache.Save(); err != nil {
				logger.Warnf("cache save failed: %v", err)
			}
			return map[string]interface{}{"missing": true, "id": reminderID}, nil
		}
		return errResult(fmt.Errorf("reminder '%s' not found", reminderID)), nil
	}
	if rd == nil || rd.ChangeTag == nil || *rd.ChangeTag == "" {
		return errResult(fmt.Errorf("missing change tag for '%s' — try running 'sync' first", reminderID)), nil
	}

	record, err := lookupRecord(w.CK, ownerID, fullID)
	if err != nil {
		// If CloudKit already says it is missing, treat this as a successful
		// idempotent delete and converge local cache state.
		if isMissingLookupError(err) {
			for _, alias := range cache.ReminderAliases(fullID) {
				delete(w.Sync.Cache.Reminders, alias)
			}
			if err := w.Sync.Cache.Save(); err != nil {
				logger.Warnf("cache save failed: %v", err)
			}
			return map[string]interface{}{"missing": true, "id": fullID}, nil
		}
		return errResult(err), nil
	}
	if ct, _ := record["recordChangeTag"].(string); ct != "" {
		rd.ChangeTag = &ct
	}
	fields, _ := record["fields"].(map[string]interface{})

	seenChildren := make(map[string]struct{})
	if err := w.deleteChildRecordsRecursive(ownerID, fields, seenChildren); err != nil {
		return errResult(err), nil
	}

	title := ""
	if rd != nil {
		title = rd.Title
	}
	currentSectionID := ""
	if rd != nil && rd.ListRef != nil && *rd.ListRef != "" {
		if sectionID, sectionErr := w.sectionIDForReminder(ownerID, *rd.ListRef, shortID(fullID)); sectionErr == nil {
			currentSectionID = sectionID
		}
	}
	for attempt := 0; attempt < 8; attempt++ {
		op := map[string]interface{}{
			"operationType": "delete",
			"record": map[string]interface{}{
				"recordName":      fullID,
				"recordChangeTag": *rd.ChangeTag,
			},
		}

		logger.Debugf("delete: removing record %s", fullID)
		result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{op})
		if err == nil {
			if verifyErr := w.verifyReminderDeleted(ownerID, fullID); verifyErr != nil {
				return errResult(verifyErr), nil
			}
			if rd.ListRef != nil && *rd.ListRef != "" {
				if clearErr := w.applySectionMembership(ownerID, *rd.ListRef, "", []string{shortID(fullID)}, true); clearErr != nil {
					logger.Warnf("delete: failed to clear section membership for %s: %v", fullID, clearErr)
				}
				if currentSectionID != "" {
					if cleanupErr := w.deleteSectionIfEmpty(ownerID, *rd.ListRef, currentSectionID); cleanupErr != nil {
						logger.Warnf("delete: failed to delete empty section %s after removing %s: %v", currentSectionID, fullID, cleanupErr)
					}
				}
			}
			for _, alias := range cache.ReminderAliases(fullID) {
				delete(w.Sync.Cache.Reminders, alias)
			}
			if err := w.Sync.Cache.Save(); err != nil {
				logger.Warnf("cache save failed: %v", err)
			}
			logger.Infof("Deleted reminder: %q (%s)", title, reminderID)
			return result, nil
		}

		blockingRecord := extractBlockingRecordName(err)
		if blockingRecord == "" {
			return errResult(err), nil
		}
		if _, alreadyTried := seenChildren[blockingRecord]; alreadyTried {
			return errResult(err), nil
		}
		logger.Debugf("delete: resolving validating reference via %s", blockingRecord)
		if err := w.deleteRecordRecursiveByName(ownerID, blockingRecord, seenChildren); err != nil {
			return errResult(err), nil
		}
	}
	return errResult(fmt.Errorf("delete reminder %s exceeded child cleanup retries", reminderID)), nil
}

func isReminderIDHint(hint string) bool {
	if hint == "" {
		return false
	}
	if strings.HasPrefix(hint, "Reminder/") {
		return true
	}
	return looksLikeUUID(hint)
}

func isMissingLookupError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") || strings.Contains(msg, "unknown item")
}

func (w *Writer) deleteReminderCacheAliases(hint string) {
	// Only scrub exact ID-derived aliases; never guess by title here.
	for _, alias := range cache.ReminderAliases(hint) {
		delete(w.Sync.Cache.Reminders, alias)
	}
	// Also scrub upper-cased variants because CloudKit record names commonly
	// normalize UUID casing.
	if looksLikeUUID(hint) {
		upper := strings.ToUpper(hint)
		for _, alias := range cache.ReminderAliases(upper) {
			delete(w.Sync.Cache.Reminders, alias)
		}
	}
}

func (w *Writer) verifyReminderDeleted(ownerID, recordName string) error {
	var lastErr error
	for attempt, delay := range deleteVerifyBackoff {
		if delay > 0 {
			time.Sleep(delay)
		}
		records, err := w.CK.LookupRecords(ownerID, []string{recordName})
		if err != nil {
			lastErr = err
			if attempt == len(deleteVerifyBackoff)-1 {
				return fmt.Errorf("delete verification lookup failed for %s: %w", recordName, err)
			}
			continue
		}
		if len(records) == 0 {
			return nil
		}
		record := records[0]
		if code, _ := record["serverErrorCode"].(string); code != "" {
			if isMissingRecordCode(code) {
				return nil
			}
			reason, _ := record["reason"].(string)
			lastErr = fmt.Errorf("CloudKit error %s: %s", code, reason)
			if attempt == len(deleteVerifyBackoff)-1 {
				return fmt.Errorf("delete verification lookup failed for %s: %w", recordName, lastErr)
			}
			continue
		}
		if deleted, _ := record["deleted"].(bool); deleted {
			return nil
		}
		fields, _ := record["fields"].(map[string]interface{})
		if getFieldIntValue(fields, "Deleted") != 0 {
			return nil
		}
		if attempt == len(deleteVerifyBackoff)-1 {
			return fmt.Errorf("delete verification failed for %s: record still present after delete", recordName)
		}
	}
	if lastErr != nil {
		return fmt.Errorf("delete verification lookup failed for %s: %w", recordName, lastErr)
	}
	return nil
}

func isMissingRecordCode(code string) bool {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "NOT_FOUND", "UNKNOWN_ITEM":
		return true
	default:
		return false
	}
}

var childReferencePrefixes = map[string]string{
	"AlarmIDs":          "Alarm/",
	"AssignmentIDs":     "Assignment/",
	"AttachmentIDs":     "Attachment/",
	"RecurrenceRuleIDs": "RecurrenceRule/",
	"HashtagIDs":        "Hashtag/",
}

var singleChildReferencePrefixes = map[string]string{
	"TriggerID": "AlarmTrigger/",
}

func referencedChildRecordNames(fields map[string]interface{}) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0)
	for fieldName, prefix := range childReferencePrefixes {
		ids, ok := getStringListField(fields, fieldName)
		if !ok {
			continue
		}
		for _, id := range ids {
			recordName := prefix + shortID(id)
			if _, exists := seen[recordName]; exists {
				continue
			}
			seen[recordName] = struct{}{}
			out = append(out, recordName)
		}
	}
	for fieldName, prefix := range singleChildReferencePrefixes {
		id := shortID(getFieldStringValue(fields, fieldName))
		if id == "" {
			continue
		}
		recordName := prefix + id
		if _, exists := seen[recordName]; exists {
			continue
		}
		seen[recordName] = struct{}{}
		out = append(out, recordName)
	}
	return out
}

func (w *Writer) deleteChildRecordsRecursive(ownerID string, fields map[string]interface{}, seen map[string]struct{}) error {
	for _, recordName := range referencedChildRecordNames(fields) {
		if err := w.deleteRecordRecursiveByName(ownerID, recordName, seen); err != nil {
			return err
		}
	}
	return nil
}

func (w *Writer) cleanupDeletedChildCacheEntries(recordNames []string) {
	for _, recordName := range recordNames {
		switch {
		case strings.HasPrefix(recordName, "Hashtag/"):
			delete(w.Sync.Cache.Hashtags, recordName)
		}
	}
}

func (w *Writer) deleteRecordRecursiveByName(ownerID, recordName string, seen map[string]struct{}) error {
	if _, exists := seen[recordName]; exists {
		return nil
	}
	seen[recordName] = struct{}{}

	record, err := lookupRecord(w.CK, ownerID, recordName)
	if err != nil {
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "not found") || strings.Contains(lower, "unknown item") {
			return nil
		}
		return err
	}

	recordFields, _ := record["fields"].(map[string]interface{})
	if err := w.deleteChildRecordsRecursive(ownerID, recordFields, seen); err != nil {
		return err
	}

	changeTag, _ := record["recordChangeTag"].(string)
	if changeTag == "" {
		return nil
	}
	logger.Debugf("delete: removing child record %s", recordName)
	result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{{
		"operationType": "delete",
		"record": map[string]interface{}{
			"recordName":      recordName,
			"recordChangeTag": changeTag,
		},
	}})
	if err != nil {
		return err
	}
	if _, hasErr := result["error"]; hasErr {
		return fmt.Errorf("delete child record %s failed", recordName)
	}
	w.cleanupDeletedChildCacheEntries([]string{recordName})
	return nil
}

func extractBlockingRecordName(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	idx := strings.Index(msg, "recordID=")
	if idx == -1 {
		return ""
	}
	rest := msg[idx+len("recordID="):]
	end := strings.Index(rest, ",")
	if end == -1 {
		end = len(rest)
	}
	return strings.TrimSpace(rest[:end])
}

// EditReminder updates one or more fields on an existing reminder.
// Pass non-empty values only for fields you want to change.
func (w *Writer) EditReminder(reminderID string, changes ReminderChanges) (map[string]interface{}, error) {
	return w.editReminderInternal(reminderID, changes, true)
}

// EditReminderNoVisibleRepair edits a reminder via CloudKit but skips the Apple-bridge
// "visible text repair" step. This avoids unsafe CRDT mutation paths when running
// in headless or non-Mac environments.
func (w *Writer) EditReminderNoVisibleRepair(reminderID string, changes ReminderChanges) (map[string]interface{}, error) {
	return w.editReminderInternal(reminderID, changes, false)
}

func (w *Writer) editReminderInternal(reminderID string, changes ReminderChanges, repairVisible bool) (map[string]interface{}, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	fullID, rd, err := w.resolveReminderRecord(reminderID)
	if err != nil {
		return errResult(fmt.Errorf("reminder '%s' not found", reminderID)), nil
	}
	if rd == nil || rd.ChangeTag == nil || *rd.ChangeTag == "" {
		return errResult(fmt.Errorf("missing change tag for '%s' — try running 'sync' first", reminderID)), nil
	}

	if changes.Title == nil && changes.DueDate == nil && changes.Notes == nil && changes.Priority == nil && changes.Completed == nil && changes.Flagged == nil && changes.HashtagIDs == nil && changes.ParentRef == nil {
		return errResult(fmt.Errorf("no changes specified — use --title, --due, --notes, --priority, --flagged, --unflagged, --parent, or tag changes")), nil
	}

	record, err := lookupRecord(w.CK, ownerID, fullID)
	if err != nil {
		return errResult(err), nil
	}
	liveReminder := reminderDataFromRecord(record)
	if liveReminder == nil {
		return errResult(fmt.Errorf("failed to decode live reminder %s", fullID)), nil
	}
	rd = liveReminder
	w.Sync.Cache.Reminders[fullID] = rd
	recordFields, _ := record["fields"].(map[string]interface{})

	ops, cleanup, err := buildReminderEditPlan(ownerID, fullID, rd, changes, recordFields)
	if err != nil {
		return errResult(err), nil
	}

	logger.Debugf("edit: updating record %s", fullID)
	result, err := w.modifyRecordsWithRetry(ownerID, ops)
	if err != nil {
		return errResult(err), nil
	}

	seenCleanup := map[string]struct{}{}
	for _, recordName := range cleanup {
		if recordName == "" {
			continue
		}
		if _, ok := seenCleanup[recordName]; ok {
			continue
		}
		seenCleanup[recordName] = struct{}{}
		if err := w.deleteRecordRecursiveByName(ownerID, recordName, map[string]struct{}{}); err != nil {
			logger.Warnf("edit: failed to clean up child record %s: %v", recordName, err)
		}
	}

	// Update local cache
	applyReminderChanges(rd, changes)
	// Update change tag from response
	if records, ok := result["records"].([]interface{}); ok && len(records) > 0 {
		if rec, ok := records[0].(map[string]interface{}); ok {
			if ct, ok := rec["recordChangeTag"].(string); ok && ct != "" {
				rd.ChangeTag = &ct
			}
		}
	}
	now := time.Now().UnixMilli()
	rd.ModifiedTS = &now
	if err := w.Sync.Cache.Save(); err != nil {
		logger.Warnf("cache save failed: %v", err)
	}
	if repairVisible {
		if err := w.repairVisibleTextState(fullID, rd, changes); err != nil {
			logger.Warnf("edit: visible-state repair failed for %s: %v", fullID, err)
		}
	}
	logger.Infof("Edited reminder: %q (%s)", rd.Title, reminderID)
	return result, nil
}

func (w *Writer) SafeTextEditReminder(reminderID string, title, notes *string, bridge *applebridge.Bridge) (map[string]interface{}, error) {
	if bridge == nil {
		return errResult(fmt.Errorf("apple bridge is required for safe text edits")), nil
	}
	if title == nil && notes == nil {
		return errResult(fmt.Errorf("no text changes specified")), nil
	}

	fullID, rd, err := w.resolveReminderRecord(reminderID)
	if err != nil {
		return errResult(fmt.Errorf("reminder '%s' not found", reminderID)), nil
	}
	if rd == nil {
		return errResult(fmt.Errorf("failed to resolve reminder '%s'", reminderID)), nil
	}

	appleID := "x-apple-reminder://" + shortID(fullID)
	if err := bridge.UpdateReminder(appleID, title, notes, nil); err != nil {
		return errResult(err), nil
	}

	live, err := bridge.GetReminder(appleID)
	if err != nil {
		return errResult(err), nil
	}
	if live == nil {
		return errResult(fmt.Errorf("apple bridge returned no reminder for %s", appleID)), nil
	}
	if title != nil && live.Title != *title {
		return errResult(fmt.Errorf("safe text edit verification failed for %s: title mismatch", reminderID)), nil
	}
	if notes != nil && live.Body != *notes {
		return errResult(fmt.Errorf("safe text edit verification failed for %s: notes mismatch", reminderID)), nil
	}

	applyReminderChanges(rd, ReminderChanges{Title: title, Notes: notes})
	now := time.Now().UnixMilli()
	rd.ModifiedTS = &now
	for _, alias := range cache.ReminderAliases(fullID) {
		w.Sync.Cache.Reminders[alias] = rd
	}
	if err := w.Sync.Cache.Save(); err != nil {
		logger.Warnf("cache save failed: %v", err)
	}

	return map[string]interface{}{
		"id":       fullID,
		"apple_id": appleID,
	}, nil
}

func (w *Writer) repairVisibleTextState(fullID string, rd *cache.ReminderData, changes ReminderChanges) error {
	if rd == nil || (changes.Title == nil && changes.Notes == nil) {
		return nil
	}
	cfg := applebridge.LoadValidatorConfigFromEnv()
	if cfg == nil {
		return nil
	}
	bridge := applebridge.New(cfg)
	appleID := "x-apple-reminder://" + shortID(fullID)

	var titlePtr *string
	if changes.Title != nil {
		titleCopy := rd.Title
		titlePtr = &titleCopy
	}
	var notesPtr *string
	if changes.Notes != nil {
		notesCopy := ""
		if rd.Notes != nil {
			notesCopy = *rd.Notes
		}
		notesPtr = &notesCopy
	}
	if titlePtr == nil && notesPtr == nil {
		return nil
	}
	if err := bridge.UpdateReminder(appleID, titlePtr, notesPtr, nil); err != nil {
		return err
	}
	live, err := bridge.GetReminder(appleID)
	if err != nil {
		return err
	}
	if live == nil {
		return fmt.Errorf("validator returned no reminder for %s", appleID)
	}
	if titlePtr != nil && live.Title != *titlePtr {
		return fmt.Errorf("title mismatch after repair: got %q want %q", live.Title, *titlePtr)
	}
	if notesPtr != nil && live.Body != *notesPtr {
		return fmt.Errorf("notes mismatch after repair: got %q want %q", live.Body, *notesPtr)
	}
	return nil
}

// ReopenReminder marks a reminder as to-do again and clears its completion date.
func (w *Writer) ReopenReminder(reminderID string) (map[string]interface{}, error) {
	incomplete := false
	return w.EditReminder(reminderID, ReminderChanges{Completed: &incomplete})
}

// buildCreateOp builds CloudKit create operations for a new reminder.
func buildCreateOp(ownerID, title, listID, parentRef, dueDate string, priority int, notes string) ([]map[string]interface{}, string, error) {
	encoded, err := utils.EncodeTitle(title)
	if err != nil {
		return nil, "", fmt.Errorf("encode title: %w", err)
	}

	recordName := utils.NewUUIDString()
	fields := map[string]interface{}{
		"TitleDocument": map[string]interface{}{"value": encoded},
		"Completed":     map[string]interface{}{"value": 0},
	}

	if listID != "" {
		fields["List"] = map[string]interface{}{
			"value": map[string]interface{}{
				"recordName": listID,
				"action":     "NONE",
			},
		}
	}

	if parentRef != "" {
		fields["ParentReminder"] = map[string]interface{}{
			"value": map[string]interface{}{
				"recordName": parentRef,
				"action":     "NONE",
			},
		}
	}

	if dueDate != "" {
		spec, err := utils.ParseDue(dueDate)
		if err != nil {
			return nil, "", fmt.Errorf("invalid due date %q: %w", dueDate, err)
		}
		fields["DueDate"] = map[string]interface{}{"value": spec.Timestamp}
		if spec.HasTime {
			artifacts, childOps, err := buildTimedDueChildOps(ownerID, recordName, spec)
			if err != nil {
				return nil, "", err
			}
			fields["TimeZone"] = map[string]interface{}{"value": artifacts.TimeZone}
			fields["AlarmIDs"] = stringListField([]string{artifacts.AlarmID})
			reminderOp := map[string]interface{}{
				"operationType": "create",
				"record": map[string]interface{}{
					"recordType": "Reminder",
					"recordName": recordName,
					"fields":     fields,
				},
			}
			return append([]map[string]interface{}{reminderOp}, childOps...), recordName, nil
		}
		fields["AlarmIDs"] = emptyListField()
	}

	if priority != 0 {
		fields["Priority"] = map[string]interface{}{"value": priority}
	}

	if notes != "" {
		encodedNotes, err := utils.EncodeTitle(notes)
		if err != nil {
			return nil, "", fmt.Errorf("encode notes: %w", err)
		}
		fields["NotesDocument"] = map[string]interface{}{"value": encodedNotes}
	}

	op := map[string]interface{}{
		"operationType": "create",
		"record": map[string]interface{}{
			"recordType": "Reminder",
			"recordName": recordName,
			"fields":     fields,
		},
	}
	return []map[string]interface{}{op}, recordName, nil
}

func buildTimedDueChildOps(ownerID, reminderRecordName string, spec utils.DueSpec) (timedDueArtifacts, []map[string]interface{}, error) {
	alarmID := utils.NewUUIDString()
	alarmRecordName := "Alarm/" + alarmID
	triggerID := utils.NewUUIDString()
	triggerRecordName := "AlarmTrigger/" + triggerID

	dateComponents, err := utils.EncodeDateComponents(spec)
	if err != nil {
		return timedDueArtifacts{}, nil, fmt.Errorf("encode date components: %w", err)
	}

	alarmOp := map[string]interface{}{
		"operationType": "create",
		"record": map[string]interface{}{
			"recordType": "Alarm",
			"recordName": alarmRecordName,
			"fields": map[string]interface{}{
				"AlarmUID":                      map[string]interface{}{"value": alarmID},
				"DueDateResolutionTokenAsNonce": map[string]interface{}{"value": float64(time.Now().UnixNano()) / 1e7, "type": "NUMBER_DOUBLE"},
				"Reminder":                      validateRecordRef(reminderRecordName, ownerID),
				"TriggerID":                     map[string]interface{}{"value": triggerID},
				"Imported":                      map[string]interface{}{"value": 0},
				"Deleted":                       map[string]interface{}{"value": 0},
			},
		},
	}

	triggerOp := map[string]interface{}{
		"operationType": "create",
		"record": map[string]interface{}{
			"recordType": "AlarmTrigger",
			"recordName": triggerRecordName,
			"fields": map[string]interface{}{
				"DateComponentsData": map[string]interface{}{"value": dateComponents, "type": "BYTES"},
				"Type":               map[string]interface{}{"value": "Date"},
				"Alarm":              validateRecordRef(alarmRecordName, ownerID),
				"Imported":           map[string]interface{}{"value": 0},
				"Deleted":            map[string]interface{}{"value": 0},
			},
		},
	}

	return timedDueArtifacts{
		AlarmID:           alarmID,
		AlarmRecordName:   alarmRecordName,
		TriggerID:         triggerID,
		TriggerRecordName: triggerRecordName,
		TimeZone:          spec.TimeZone,
	}, []map[string]interface{}{alarmOp, triggerOp}, nil
}

func buildCreateListOp(name string) (map[string]interface{}, string) {
	recordName := "List/" + utils.NewUUIDString()
	fields := map[string]interface{}{
		"Name": map[string]interface{}{"value": name},
	}
	op := map[string]interface{}{
		"operationType": "create",
		"record": map[string]interface{}{
			"recordType": "List",
			"recordName": recordName,
			"fields":     fields,
		},
	}
	return op, recordName
}

func resolveParentRef(engine *sync.Engine, parentHint string, listID string) string {
	if parentHint == "" {
		return ""
	}
	if rid := normalizeReminderID(parentHint); rid != "" {
		if _, ok := engine.Cache.Reminders[rid]; ok {
			return rid
		}
	}
	return ""
}

func (w *Writer) resolveReminderRecord(reminderHint string) (string, *cache.ReminderData, error) {
	if reminderID := normalizeReminderID(reminderHint); reminderID != "" {
		if _, ok := w.Sync.Cache.Reminders[reminderID]; ok {
			return w.canonicalizeReminderRecord(reminderID, w.Sync.Cache.Reminders[reminderID])
		}
		if fullID, rd, ok := w.lookupReminderRecordByID(reminderID); ok && fullID != "" {
			return w.canonicalizeReminderRecord(fullID, rd)
		}
	}
	return "", nil, fmt.Errorf("reminder %q not found", reminderHint)
}

func (w *Writer) lookupReminderRecordByID(recordName string) (string, *cache.ReminderData, bool) {
	if recordName == "" {
		return "", nil, false
	}
	ownerID, err := w.ownerID()
	if err != nil {
		return "", nil, false
	}
	record, err := lookupRecord(w.CK, ownerID, recordName)
	if err != nil {
		return "", nil, false
	}
	fullID, _ := record["recordName"].(string)
	if fullID == "" {
		fullID = recordName
	}
	rd := reminderDataFromRecord(record)
	w.Sync.Cache.Reminders[fullID] = rd
	return fullID, rd, true
}

func normalizeReminderID(hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" {
		return ""
	}
	hintUpper := strings.ToUpper(hint)
	if strings.HasPrefix(hintUpper, "REMINDER/") {
		uuid := shortID(hintUpper)
		if !looksLikeUUID(uuid) {
			return ""
		}
		return "Reminder/" + uuid
	}
	if looksLikeUUID(hint) {
		return "Reminder/" + strings.ToUpper(hint)
	}
	return ""
}

func (w *Writer) canonicalizeReminderRecord(recordName string, rd *cache.ReminderData) (string, *cache.ReminderData, error) {
	if strings.Contains(recordName, "/") || !looksLikeUUID(recordName) {
		return recordName, rd, nil
	}

	ownerID, err := w.ownerID()
	if err != nil {
		return recordName, rd, err
	}
	for _, candidate := range reminderLookupCandidates(recordName) {
		record, err := lookupRecord(w.CK, ownerID, candidate)
		if err != nil {
			continue
		}
		fullID, _ := record["recordName"].(string)
		if fullID == "" {
			fullID = candidate
		}
		live := reminderDataFromRecord(record)
		if rd != nil {
			if live.ChangeTag == nil || *live.ChangeTag == "" {
				live.ChangeTag = rd.ChangeTag
			}
			if live.ListRef == nil {
				live.ListRef = rd.ListRef
			}
			if live.ParentRef == nil {
				live.ParentRef = rd.ParentRef
			}
			if live.Notes == nil {
				live.Notes = rd.Notes
			}
			if len(live.HashtagIDs) == 0 {
				live.HashtagIDs = append([]string(nil), rd.HashtagIDs...)
			}
			if live.Due == nil {
				live.Due = rd.Due
			}
			if live.Priority == 0 {
				live.Priority = rd.Priority
			}
		}
		w.Sync.Cache.Reminders[fullID] = live
		return fullID, live, nil
	}
	return recordName, rd, nil
}

func reminderLookupCandidates(hint string) []string {
	candidates := make([]string, 0, 2)
	seen := map[string]struct{}{}
	add := func(recordName string) {
		if recordName == "" {
			return
		}
		if _, ok := seen[recordName]; ok {
			return
		}
		seen[recordName] = struct{}{}
		candidates = append(candidates, recordName)
	}

	if strings.HasPrefix(hint, "Reminder/") {
		add(hint)
		add(shortID(hint))
		return candidates
	}
	if looksLikeUUID(hint) {
		add("Reminder/" + strings.ToUpper(hint))
		add(strings.ToUpper(hint))
	}
	return candidates
}

func reminderDataFromRecord(record map[string]interface{}) *cache.ReminderData {
	fields, _ := record["fields"].(map[string]interface{})
	if fields == nil {
		fields = map[string]interface{}{}
	}

	title := utils.ExtractTitle(getFieldStringValue(fields, "TitleDocument"))
	if title == "" {
		title = "(untitled)"
	}

	var dueStr, completionStr *string
	if due := getFieldInt64Value(fields, "DueDate"); due != 0 {
		s := utils.TsToStr(due)
		dueStr = &s
	}
	if cd := getFieldInt64Value(fields, "CompletionDate"); cd != 0 {
		s := utils.TsToStr(cd)
		completionStr = &s
	}

	listRef := getReferenceRecordName(fields, "List")
	parentRef := getReferenceRecordName(fields, "ParentReminder")
	hashtagIDs, _ := getStringListField(fields, "HashtagIDs")
	notes := utils.ExtractTitle(getFieldStringValue(fields, "NotesDocument"))
	priority := getFieldIntValue(fields, "Priority")
	changeTag, _ := record["recordChangeTag"].(string)

	modified, _ := record["modified"].(map[string]interface{})
	var modTS *int64
	if ts, ok := modified["timestamp"].(float64); ok {
		v := int64(ts)
		modTS = &v
	}

	rd := &cache.ReminderData{
		Title:          title,
		Completed:      getFieldIntValue(fields, "Completed") != 0,
		Flagged:        getFieldIntValue(fields, "Flagged") != 0,
		CompletionDate: completionStr,
		Due:            dueStr,
		Priority:       priority,
		ModifiedTS:     modTS,
	}
	if notes != "" {
		rd.Notes = &notes
	}
	if len(hashtagIDs) > 0 {
		rd.HashtagIDs = append([]string(nil), hashtagIDs...)
	}
	if listRef != "" {
		rd.ListRef = &listRef
	}
	if parentRef != "" {
		rd.ParentRef = &parentRef
	}
	if changeTag != "" {
		rd.ChangeTag = &changeTag
	}
	return rd
}

func errResult(err error) map[string]interface{} {
	return map[string]interface{}{"error": err.Error()}
}

func buildReminderEditPlan(ownerID, fullID string, current *cache.ReminderData, changes ReminderChanges, liveFields map[string]interface{}) ([]map[string]interface{}, []string, error) {
	fields, err := buildReminderFields(current, changes, liveFields)
	if err != nil {
		return nil, nil, err
	}

	existingAlarmIDs, _ := getStringListField(liveFields, "AlarmIDs")
	existingTimeZone := getFieldStringValue(liveFields, "TimeZone")
	cleanup := make([]string, 0)

	finalDue := current.Due
	if changes.DueDate != nil {
		if *changes.DueDate == "" {
			finalDue = nil
		} else {
			finalDue = changes.DueDate
		}
	}

	var extraOps []map[string]interface{}
	if finalDue != nil && *finalDue != "" {
		spec, err := utils.ParseDue(*finalDue)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid due date %q (expected YYYY-MM-DD, YYYY-MM-DDTHH:MM, or RFC3339): %w", *finalDue, err)
		}
		fields["DueDate"] = map[string]interface{}{"value": spec.Timestamp}
		if spec.HasTime {
			if changes.DueDate == nil && len(existingAlarmIDs) > 0 {
				fields["AlarmIDs"] = stringListField(existingAlarmIDs)
				if existingTimeZone != "" {
					fields["TimeZone"] = map[string]interface{}{"value": existingTimeZone}
				}
			} else {
				artifacts, childOps, err := buildTimedDueChildOps(ownerID, fullID, spec)
				if err != nil {
					return nil, nil, err
				}
				fields["AlarmIDs"] = stringListField([]string{artifacts.AlarmID})
				fields["TimeZone"] = map[string]interface{}{"value": artifacts.TimeZone}
				extraOps = append(extraOps, childOps...)
				cleanup = append(cleanup, referencedChildRecordNames(liveFields)...)
			}
		} else {
			fields["AlarmIDs"] = emptyListField()
			delete(fields, "TimeZone")
			cleanup = append(cleanup, referencedChildRecordNames(liveFields)...)
		}
	} else {
		delete(fields, "DueDate")
		fields["AlarmIDs"] = emptyListField()
		delete(fields, "TimeZone")
		cleanup = append(cleanup, referencedChildRecordNames(liveFields)...)
	}

	op := map[string]interface{}{
		"operationType": "replace",
		"record": map[string]interface{}{
			"recordType":      "Reminder",
			"recordName":      fullID,
			"recordChangeTag": *current.ChangeTag,
			"fields":          fields,
		},
	}

	return append([]map[string]interface{}{op}, extraOps...), cleanup, nil
}

func buildReminderFields(current *cache.ReminderData, changes ReminderChanges, liveFields ...map[string]interface{}) (map[string]interface{}, error) {
	title := current.Title
	touchedKeys := make([]string, 0, 7)
	if changes.Title != nil {
		title = *changes.Title
		touchedKeys = append(touchedKeys, "titleDocument")
	}
	if title == "" {
		return nil, fmt.Errorf("title cannot be empty")
	}
	titleDocument := ""
	if changes.Title == nil && len(liveFields) > 0 && liveFields[0] != nil {
		titleDocument = getFieldStringValue(liveFields[0], "TitleDocument")
	}
	if titleDocument == "" {
		titleUUID := liveTextDocumentUUID(liveFields, "TitleDocument")
		encodedTitle, err := utils.EncodeTextDocument(title, titleUUID)
		if err != nil {
			return nil, fmt.Errorf("encode title: %w", err)
		}
		titleDocument = encodedTitle
	}

	completed := current.Completed
	if changes.Completed != nil {
		completed = *changes.Completed
		touchedKeys = append(touchedKeys, "completed", "completionDate")
	}

	flagged := current.Flagged
	if changes.Flagged != nil {
		flagged = *changes.Flagged
		touchedKeys = append(touchedKeys, "flagged")
	}

	priority := current.Priority
	if changes.Priority != nil {
		priority = *changes.Priority
		touchedKeys = append(touchedKeys, "priority")
	}

	fields := map[string]interface{}{
		"TitleDocument": map[string]interface{}{"value": titleDocument},
		"Completed":     map[string]interface{}{"value": boolToInt(completed)},
		"Flagged":       map[string]interface{}{"value": boolToInt(flagged)},
		"Priority":      map[string]interface{}{"value": priority},
	}

	if current.ListRef != nil && *current.ListRef != "" {
		fields["List"] = recordRef(*current.ListRef)
	}
	if current.ParentRef != nil && *current.ParentRef != "" {
		fields["ParentReminder"] = recordRef(*current.ParentRef)
	}
	if changes.ParentRef != nil {
		touchedKeys = append(touchedKeys, "parentReminder")
		if *changes.ParentRef == "" {
			delete(fields, "ParentReminder")
		} else {
			fields["ParentReminder"] = recordRef(*changes.ParentRef)
		}
	}

	hashtagIDs := append([]string(nil), current.HashtagIDs...)
	if changes.HashtagIDs != nil {
		hashtagIDs = append([]string(nil), (*changes.HashtagIDs)...)
	}
	if len(hashtagIDs) == 0 {
		fields["HashtagIDs"] = map[string]interface{}{
			"value": []interface{}{},
			"type":  "EMPTY_LIST",
		}
	} else {
		items := make([]interface{}, 0, len(hashtagIDs))
		for _, id := range hashtagIDs {
			items = append(items, strings.TrimPrefix(id, "Hashtag/"))
		}
		fields["HashtagIDs"] = map[string]interface{}{
			"value": items,
			"type":  "STRING_LIST",
		}
	}

	due := current.Due
	if changes.DueDate != nil {
		touchedKeys = append(touchedKeys, "dueDate", "alarmIDs", "timeZone")
		if *changes.DueDate == "" {
			due = nil
		} else {
			due = changes.DueDate
		}
	}
	if due != nil && *due != "" {
		ts, err := utils.StrToTs(*due)
		if err != nil {
			return nil, fmt.Errorf("invalid due date %q (expected YYYY-MM-DD, YYYY-MM-DDTHH:MM, or RFC3339): %w", *due, err)
		}
		fields["DueDate"] = map[string]interface{}{"value": ts}
	}

	notes := current.Notes
	if changes.Notes != nil {
		touchedKeys = append(touchedKeys, "notesDocument")
		if *changes.Notes == "" {
			notes = nil
		} else {
			notes = changes.Notes
		}
	}
	notesDocument := ""
	if changes.Notes == nil && len(liveFields) > 0 && liveFields[0] != nil {
		notesDocument = getFieldStringValue(liveFields[0], "NotesDocument")
	}
	if notes != nil && *notes != "" {
		if notesDocument == "" {
			notesUUID := liveTextDocumentUUID(liveFields, "NotesDocument")
			encodedNotes, err := utils.EncodeTextDocument(*notes, notesUUID)
			if err != nil {
				return nil, fmt.Errorf("encode notes: %w", err)
			}
			notesDocument = encodedNotes
		}
		fields["NotesDocument"] = map[string]interface{}{"value": notesDocument}
	}

	completionDate := current.CompletionDate
	if changes.Completed != nil && *changes.Completed && (completionDate == nil || strings.TrimSpace(*completionDate) == "") {
		nowStr := utils.TsToStr(time.Now().UnixMilli())
		completionDate = &nowStr
	}
	if changes.Completed != nil && !*changes.Completed {
		completionDate = nil
	}
	if completed && completionDate != nil && *completionDate != "" {
		ts, err := utils.StrToTs(*completionDate)
		if err != nil {
			return nil, fmt.Errorf("invalid completion date %q: %w", *completionDate, err)
		}
		fields["CompletionDate"] = map[string]interface{}{"value": ts}
	}

	if len(liveFields) > 0 && liveFields[0] != nil && len(touchedKeys) > 0 {
		nowMillis := time.Now().UnixMilli()
		fields["LastModifiedDate"] = map[string]interface{}{
			"value": nowMillis,
			"type":  "TIMESTAMP",
		}
		fields["ResolutionTokenMap"] = map[string]interface{}{
			"value": bumpReminderResolutionTokenMap(liveFields[0], touchedKeys, nowMillis),
			"type":  "STRING",
		}
	}

	return fields, nil
}

func liveTextDocumentUUID(liveFields []map[string]interface{}, fieldName string) []byte {
	if len(liveFields) == 0 || liveFields[0] == nil {
		return nil
	}
	uuid, ok := utils.ExtractDocumentUUID(getFieldStringValue(liveFields[0], fieldName))
	if !ok {
		return nil
	}
	return uuid
}

func bumpReminderResolutionTokenMap(fields map[string]interface{}, keys []string, nowMillis int64) string {
	raw := getFieldStringValue(fields, "ResolutionTokenMap")
	payload := map[string]interface{}{}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			payload = map[string]interface{}{}
		}
	}
	m, _ := payload["map"].(map[string]interface{})
	if m == nil {
		m = map[string]interface{}{}
		payload["map"] = m
	}

	replicaID := reminderReplicaID(m)
	seen := map[string]struct{}{}
	for _, key := range append(keys, "lastModifiedDate") {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		entry, _ := m[key].(map[string]interface{})
		if entry == nil {
			entry = map[string]interface{}{}
			m[key] = entry
		}
		counter := 0
		switch v := entry["counter"].(type) {
		case float64:
			counter = int(v)
		case int:
			counter = v
		}
		entry["counter"] = counter + 1
		entry["modificationTime"] = float64(nowMillis)/1000 - 978307200
		if existing, _ := entry["replicaID"].(string); existing != "" {
			continue
		}
		entry["replicaID"] = replicaID
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func reminderReplicaID(m map[string]interface{}) string {
	for _, raw := range m {
		entry, _ := raw.(map[string]interface{})
		if entry == nil {
			continue
		}
		if replicaID, _ := entry["replicaID"].(string); replicaID != "" {
			return replicaID
		}
	}
	return "D25A1933-3DDF-4573-8177-B051D76C2E5B"
}

func applyReminderChanges(current *cache.ReminderData, changes ReminderChanges) {
	if changes.Title != nil {
		current.Title = *changes.Title
	}
	if changes.DueDate != nil {
		if *changes.DueDate == "" {
			current.Due = nil
		} else {
			current.Due = changes.DueDate
		}
	}
	if changes.Notes != nil {
		if *changes.Notes == "" {
			current.Notes = nil
		} else {
			current.Notes = changes.Notes
		}
	}
	if changes.Priority != nil {
		current.Priority = *changes.Priority
	}
	if changes.Flagged != nil {
		current.Flagged = *changes.Flagged
	}
	if changes.HashtagIDs != nil {
		current.HashtagIDs = append([]string(nil), (*changes.HashtagIDs)...)
	}
	if changes.ParentRef != nil {
		if *changes.ParentRef == "" {
			current.ParentRef = nil
		} else {
			current.ParentRef = changes.ParentRef
		}
	}
	if changes.Completed != nil {
		current.Completed = *changes.Completed
		if *changes.Completed {
			if current.CompletionDate == nil {
				nowStr := utils.TsToStr(time.Now().UnixMilli())
				current.CompletionDate = &nowStr
			}
		} else {
			current.CompletionDate = nil
		}
	}
}

func recordRef(recordName string) map[string]interface{} {
	return map[string]interface{}{
		"value": map[string]interface{}{
			"recordName": recordName,
			"action":     "NONE",
		},
	}
}

func validateRecordRef(recordName, ownerID string) map[string]interface{} {
	return map[string]interface{}{
		"value": map[string]interface{}{
			"recordName": recordName,
			"action":     "VALIDATE",
			"zoneID": map[string]interface{}{
				"zoneName":        cloudkit.Zone,
				"ownerRecordName": ownerID,
				"zoneType":        "REGULAR_CUSTOM_ZONE",
			},
		},
		"type": "REFERENCE",
	}
}

func stringListField(ids []string) map[string]interface{} {
	items := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		items = append(items, strings.TrimPrefix(id, "Alarm/"))
	}
	return map[string]interface{}{
		"value": items,
		"type":  "STRING_LIST",
	}
}

func emptyListField() map[string]interface{} {
	return map[string]interface{}{
		"value": []interface{}{},
		"type":  "EMPTY_LIST",
	}
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func (w *Writer) modifyRecordsWithRetry(ownerID string, operations []map[string]interface{}) (map[string]interface{}, error) {
	var lastErr error
	backoffs := []time.Duration{
		0,
		500 * time.Millisecond,
		1500 * time.Millisecond,
		3 * time.Second,
		6 * time.Second,
		10 * time.Second,
	}
	for attempt, delay := range backoffs {
		if delay > 0 {
			time.Sleep(delay)
		}
		result, err := w.CK.ModifyRecords(ownerID, operations)
		if err != nil {
			lastErr = err
			if isZoneBusy(err) && attempt < len(backoffs)-1 {
				continue
			}
			return result, err
		}
		if recErr := checkRecordErrors(result); recErr != nil {
			lastErr = recErr
			if isZoneBusy(recErr) && attempt < len(backoffs)-1 {
				continue
			}
			return result, recErr
		}
		return result, nil
	}
	return nil, lastErr
}

// checkRecordErrors extracts the first record-level error from CloudKit result.
// CloudKit returns errors like {"records": [{"serverErrorCode": "BAD_REQUEST", "reason": "..."}]}
func checkRecordErrors(result map[string]interface{}) error {
	records, ok := result["records"].([]interface{})
	if !ok {
		return nil
	}
	for _, r := range records {
		rec, ok := r.(map[string]interface{})
		if !ok {
			continue
		}
		if code, ok := rec["serverErrorCode"].(string); ok && code != "" {
			reason, _ := rec["reason"].(string)
			return fmt.Errorf("CloudKit error %s: %s", code, reason)
		}
	}
	return nil
}

func isZoneBusy(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "ZONE_BUSY") ||
		strings.Contains(msg, "OP_LOCK_FAILURE") ||
		strings.Contains(msg, "SERVER_OVERLOADED") ||
		cloudkit.Is503(err)
}
