package writer

import (
	"fmt"
	"strings"
	"time"

	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/logger"
	"icloud-reminders/internal/utils"
)

// SetReminderTags replaces a reminder's native Apple Reminders tags.
func (w *Writer) SetReminderTags(reminderID string, tagNames []string) (map[string]interface{}, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	fullID := w.Sync.FindReminderByID(reminderID)
	if fullID == "" {
		fullID = w.Sync.FindReminderByTitle(reminderID, "", false)
	}
	if fullID == "" {
		return errResult(fmt.Errorf("reminder '%s' not found", reminderID)), nil
	}

	rd := w.Sync.Cache.Reminders[fullID]
	if rd == nil || rd.ChangeTag == nil || *rd.ChangeTag == "" {
		return errResult(fmt.Errorf("missing change tag for '%s' — try running 'sync' first", reminderID)), nil
	}
	if err := w.hydrateReminderTags(ownerID, fullID, rd); err != nil {
		return errResult(err), nil
	}

	desiredNames := normalizeTagNames(tagNames)
	existingByName := make(map[string]string)
	for _, tagID := range rd.HashtagIDs {
		recordName := hashtagRecordName(tagID)
		hd := w.Sync.Cache.Hashtags[recordName]
		if hd == nil || hd.Name == "" {
			continue
		}
		key := normalizeTagName(hd.Name)
		if key == "" {
			continue
		}
		if _, ok := existingByName[key]; !ok {
			existingByName[key] = recordName
		}
	}

	var (
		finalIDs          []string
		createOps         []map[string]interface{}
		deleteOps         []map[string]interface{}
		createdRecordInfo []cache.HashtagData
		createdNames      []string
	)
	keptRecordNames := make(map[string]struct{})

	for _, name := range desiredNames {
		key := normalizeTagName(name)
		if recordName, ok := existingByName[key]; ok {
			keptRecordNames[recordName] = struct{}{}
			finalIDs = append(finalIDs, shortHashtagID(recordName))
			continue
		}

		recordName := hashtagRecordName(utils.NewUUIDString())
		finalIDs = append(finalIDs, shortHashtagID(recordName))
		createOps = append(createOps, buildCreateHashtagOp(recordName, fullID, name))
		createdRecordInfo = append(createdRecordInfo, cache.HashtagData{
			Name:        name,
			ReminderRef: &fullID,
		})
		createdNames = append(createdNames, recordName)
		keptRecordNames[recordName] = struct{}{}
	}

	for _, tagID := range rd.HashtagIDs {
		recordName := hashtagRecordName(tagID)
		if _, keep := keptRecordNames[recordName]; keep {
			continue
		}
		hd := w.Sync.Cache.Hashtags[recordName]
		if hd == nil || hd.ChangeTag == nil || *hd.ChangeTag == "" {
			continue
		}
		deleteOps = append(deleteOps, map[string]interface{}{
			"operationType": "delete",
			"record": map[string]interface{}{
				"recordName":      recordName,
				"recordChangeTag": *hd.ChangeTag,
			},
		})
	}

	fields, err := buildReminderFields(rd, ReminderChanges{HashtagIDs: &finalIDs})
	if err != nil {
		return errResult(err), nil
	}
	fields["LastModifiedDate"] = map[string]interface{}{
		"value": time.Now().UnixMilli(),
		"type":  "TIMESTAMP",
	}

	ops := make([]map[string]interface{}, 0, len(createOps)+1+len(deleteOps))
	ops = append(ops, createOps...)
	ops = append(ops, map[string]interface{}{
		"operationType": "replace",
		"record": map[string]interface{}{
			"recordType":      "Reminder",
			"recordName":      fullID,
			"recordChangeTag": *rd.ChangeTag,
			"fields":          fields,
		},
	})
	ops = append(ops, deleteOps...)

	logger.Debugf("set-tags: updating reminder %s with %d tag(s)", fullID, len(finalIDs))
	result, err := w.modifyRecordsWithRetry(ownerID, ops)
	if err != nil {
		return errResult(err), nil
	}

	applyReminderChanges(rd, ReminderChanges{HashtagIDs: &finalIDs})
	if ct := lookupResultChangeTag(result, fullID); ct != nil {
		rd.ChangeTag = ct
	}
	now := time.Now().UnixMilli()
	rd.ModifiedTS = &now

	for _, op := range deleteOps {
		record, _ := op["record"].(map[string]interface{})
		recordName, _ := record["recordName"].(string)
		delete(w.Sync.Cache.Hashtags, recordName)
	}
	for idx, recordName := range createdNames {
		entry := createdRecordInfo[idx]
		if ct := lookupResultChangeTag(result, recordName); ct != nil {
			entry.ChangeTag = ct
		}
		w.Sync.Cache.Hashtags[recordName] = &entry
	}
	if err := w.Sync.Cache.Save(); err != nil {
		logger.Warnf("cache save failed: %v", err)
	}

	return map[string]interface{}{
		"id":   fullID,
		"tags": desiredNames,
	}, nil
}

func buildCreateHashtagOp(recordName, reminderRecordName, name string) map[string]interface{} {
	now := time.Now().UnixMilli()
	return map[string]interface{}{
		"operationType": "create",
		"record": map[string]interface{}{
			"recordName": recordName,
			"recordType": "Hashtag",
			"fields": map[string]interface{}{
				"CreationDate": map[string]interface{}{
					"value": now,
					"type":  "TIMESTAMP",
				},
				"Type": map[string]interface{}{
					"value": 1,
					"type":  "NUMBER_INT64",
				},
				"Reminder": recordRef(reminderRecordName),
				"Imported": map[string]interface{}{
					"value": 0,
					"type":  "NUMBER_INT64",
				},
				"Deleted": map[string]interface{}{
					"value": 0,
					"type":  "NUMBER_INT64",
				},
				"Name": map[string]interface{}{
					"value":       name,
					"type":        "STRING",
					"isEncrypted": true,
				},
			},
		},
	}
}

func normalizeTagNames(tagNames []string) []string {
	var out []string
	seen := make(map[string]struct{})
	for _, name := range tagNames {
		trimmed := strings.TrimSpace(name)
		trimmed = strings.TrimPrefix(trimmed, "#")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			continue
		}
		key := normalizeTagName(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func normalizeTagName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.TrimPrefix(name, "#")
	name = strings.TrimSpace(name)
	return strings.ToLower(name)
}

func hashtagRecordName(id string) string {
	if strings.HasPrefix(id, "Hashtag/") {
		return id
	}
	return "Hashtag/" + id
}

func shortHashtagID(recordName string) string {
	return strings.TrimPrefix(recordName, "Hashtag/")
}

func lookupResultChangeTag(result map[string]interface{}, recordName string) *string {
	records, _ := result["records"].([]interface{})
	for _, raw := range records {
		rec, _ := raw.(map[string]interface{})
		if rec == nil {
			continue
		}
		if recName, _ := rec["recordName"].(string); recName == recordName {
			if ct, _ := rec["recordChangeTag"].(string); ct != "" {
				return &ct
			}
		}
	}
	return nil
}

func (w *Writer) hydrateReminderTags(ownerID, reminderRecordName string, rd *cache.ReminderData) error {
	record, err := lookupRecord(w.CK, ownerID, reminderRecordName)
	if err != nil {
		return err
	}
	if ct, _ := record["recordChangeTag"].(string); ct != "" {
		rd.ChangeTag = &ct
	}
	fields, _ := record["fields"].(map[string]interface{})
	if liveIDs, ok := getStringListField(fields, "HashtagIDs"); ok {
		rd.HashtagIDs = append([]string(nil), liveIDs...)
	}
	if len(rd.HashtagIDs) == 0 {
		return nil
	}

	recordNames := make([]string, 0, len(rd.HashtagIDs))
	for _, id := range rd.HashtagIDs {
		recordName := hashtagRecordName(id)
		if hd := w.Sync.Cache.Hashtags[recordName]; hd != nil && hd.Name != "" && hd.ChangeTag != nil && *hd.ChangeTag != "" {
			continue
		}
		recordNames = append(recordNames, recordName)
	}
	if len(recordNames) == 0 {
		return nil
	}

	records, err := w.CK.LookupRecords(ownerID, recordNames)
	if err != nil {
		return err
	}
	for _, rec := range records {
		if code, _ := rec["serverErrorCode"].(string); code != "" {
			continue
		}
		recordName, _ := rec["recordName"].(string)
		recordFields, _ := rec["fields"].(map[string]interface{})
		name := getFieldStringValue(recordFields, "Name")
		reminderRef := getReferenceRecordName(recordFields, "Reminder")
		changeTag, _ := rec["recordChangeTag"].(string)
		entry := &cache.HashtagData{Name: name}
		if reminderRef != "" {
			entry.ReminderRef = &reminderRef
		}
		if changeTag != "" {
			entry.ChangeTag = &changeTag
		}
		w.Sync.Cache.Hashtags[recordName] = entry
	}
	return nil
}

func getStringListField(fields map[string]interface{}, key string) ([]string, bool) {
	field, ok := fields[key].(map[string]interface{})
	if !ok {
		return nil, false
	}
	raw, ok := field["value"].([]interface{})
	if !ok {
		return nil, true
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, true
}
