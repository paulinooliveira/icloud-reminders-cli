package writer

import (
	"fmt"
	"strings"
	"time"

	"icloud-reminders/internal/cache"
	"icloud-reminders/internal/cloudkit"
	"icloud-reminders/internal/sections"
	"icloud-reminders/internal/utils"
)

func (w *Writer) AssignReminderToSection(reminderHint, listHint, sectionHint string) (map[string]interface{}, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	reminderID, err := w.resolveReminderRef(reminderHint)
	if err != nil {
		return errResult(err), nil
	}

	listID, err := w.resolveListRef(listHint, reminderID)
	if err != nil {
		return errResult(err), nil
	}

	sectionID, sectionName, err := w.resolveSectionRef(ownerID, listID, sectionHint)
	if err != nil {
		return errResult(err), nil
	}

	if err := w.applySectionMembership(ownerID, listID, sectionID, []string{shortID(reminderID)}, false); err != nil {
		return errResult(err), nil
	}

	return map[string]interface{}{
		"id":         reminderID,
		"list_id":    listID,
		"section":    sectionName,
		"section_id": sections.ListSectionRecordName(sectionID),
	}, nil
}

func (w *Writer) ClearReminderSection(reminderHint, listHint string) (map[string]interface{}, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}

	reminderID, err := w.resolveReminderRef(reminderHint)
	if err != nil {
		return errResult(err), nil
	}
	listID, err := w.resolveListRef(listHint, reminderID)
	if err != nil {
		return errResult(err), nil
	}

	if err := w.applySectionMembership(ownerID, listID, "", []string{shortID(reminderID)}, true); err != nil {
		return errResult(err), nil
	}

	return map[string]interface{}{
		"id":      reminderID,
		"list_id": listID,
		"cleared": true,
	}, nil
}

func (w *Writer) AssignReminderIDsToSection(listID, sectionHint string, reminderIDs []string) error {
	if len(reminderIDs) == 0 {
		return nil
	}
	ownerID, err := w.ownerID()
	if err != nil {
		return err
	}
	sectionID, _, err := w.resolveSectionRef(ownerID, listID, sectionHint)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(reminderIDs))
	for _, reminderID := range reminderIDs {
		ids = append(ids, shortID(reminderID))
	}
	return w.applySectionMembership(ownerID, listID, sectionID, ids, false)
}

func (w *Writer) EnsureSection(listHint, name string) (map[string]interface{}, error) {
	if strings.TrimSpace(name) == "" {
		return errResult(fmt.Errorf("section name cannot be empty")), nil
	}
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}
	listID, err := w.resolveListRef(listHint, "")
	if err != nil {
		return errResult(err), nil
	}

	if sectionID, sectionName, err := w.resolveSectionRef(ownerID, listID, name); err == nil {
		return map[string]interface{}{
			"existing":   true,
			"section_id": sections.ListSectionRecordName(sectionID),
			"list_id":    listID,
			"name":       sectionName,
		}, nil
	}

	sectionName := strings.TrimSpace(name)
	recordName := sections.ListSectionRecordName(strings.ToUpper(utils.NewUUIDString()))
	op := buildCreateSectionOp(ownerID, listID, recordName, sectionName)
	result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{op})
	if err != nil {
		return errResult(err), nil
	}
	canonical := sections.CanonicalName(sectionName)
	w.Sync.Cache.Sections[recordName] = &cache.SectionData{
		Name:          sectionName,
		CanonicalName: &canonical,
		ListRef:       &listID,
	}
	if err := w.Sync.Cache.Save(); err != nil {
		// non-fatal
	}
	return map[string]interface{}{
		"section_id": recordName,
		"list_id":    listID,
		"name":       sectionName,
		"records":    result["records"],
	}, nil
}

func (w *Writer) RenameSection(listHint, sectionHint, newName string) (map[string]interface{}, error) {
	if strings.TrimSpace(newName) == "" {
		return errResult(fmt.Errorf("new section name cannot be empty")), nil
	}
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}
	listID, err := w.resolveListRef(listHint, "")
	if err != nil {
		return errResult(err), nil
	}

	recordName, record, err := w.lookupSectionRecord(ownerID, listID, sectionHint)
	if err != nil {
		return errResult(err), nil
	}
	changeTag, _ := record["recordChangeTag"].(string)
	if changeTag == "" {
		return errResult(fmt.Errorf("section %s missing change tag", recordName)), nil
	}

	fields, _ := record["fields"].(map[string]interface{})
	currentName := resolveSectionName(fields, shortID(recordName))
	op := buildRenameSectionOp(recordName, changeTag, strings.TrimSpace(newName))
	result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{op})
	if err != nil {
		return errResult(err), nil
	}
	canonical := sections.CanonicalName(strings.TrimSpace(newName))
	updatedChangeTag := updatedRecordChangeTag(result)
	w.Sync.Cache.Sections[recordName] = &cache.SectionData{
		Name:          strings.TrimSpace(newName),
		CanonicalName: &canonical,
		ListRef:       &listID,
		ChangeTag:     updatedChangeTag,
	}
	if err := w.Sync.Cache.Save(); err != nil {
		// non-fatal
	}
	return map[string]interface{}{
		"section_id": recordName,
		"list_id":    listID,
		"old_name":   currentName,
		"name":       strings.TrimSpace(newName),
		"records":    result["records"],
	}, nil
}

func (w *Writer) DeleteSection(listHint, sectionHint string, force bool) (map[string]interface{}, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return errResult(err), nil
	}
	listID, err := w.resolveListRef(listHint, "")
	if err != nil {
		return errResult(err), nil
	}

	recordName, record, err := w.lookupSectionRecord(ownerID, listID, sectionHint)
	if err != nil {
		if force && (looksLikeUUID(sectionHint) || strings.HasPrefix(sectionHint, "ListSection/")) {
			sectionID := shortID(sectionHint)
			cleared, clearErr := w.clearOrphanSectionMemberships(ownerID, listID, sectionID)
			if clearErr != nil {
				return errResult(clearErr), nil
			}
			return map[string]interface{}{
				"section_id":    sections.ListSectionRecordName(sectionID),
				"list_id":       listID,
				"cleared_count": len(cleared),
				"orphaned":      true,
			}, nil
		}
		return errResult(err), nil
	}
	sectionID := shortID(recordName)
	fields, _ := record["fields"].(map[string]interface{})
	sectionName := resolveSectionName(fields, sectionID)

	memberIDs, err := w.memberIDsForSection(ownerID, listID, sectionID)
	if err != nil {
		return errResult(err), nil
	}
	if len(memberIDs) > 0 && !force {
		return errResult(fmt.Errorf("section %s is not empty (%d reminders); pass --force to clear memberships first", sectionName, len(memberIDs))), nil
	}
	if len(memberIDs) > 0 {
		if err := w.applySectionMembership(ownerID, listID, "", memberIDs, true); err != nil {
			return errResult(err), nil
		}
	}

	changeTag, _ := record["recordChangeTag"].(string)
	if changeTag == "" {
		return errResult(fmt.Errorf("section %s missing change tag", recordName)), nil
	}
	result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{{
		"operationType": "delete",
		"record": map[string]interface{}{
			"recordName":      recordName,
			"recordChangeTag": changeTag,
		},
	}})
	if err != nil {
		return errResult(err), nil
	}
	delete(w.Sync.Cache.Sections, recordName)
	if err := w.Sync.Cache.Save(); err != nil {
		// non-fatal
	}
	return map[string]interface{}{
		"section_id":     recordName,
		"list_id":        listID,
		"name":           sectionName,
		"cleared_count":  len(memberIDs),
		"records":        result["records"],
	}, nil
}

func (w *Writer) applySectionMembership(ownerID, listID, sectionID string, reminderIDs []string, clearOnly bool) error {
	listRecord, err := lookupRecord(w.CK, ownerID, listID)
	if err != nil {
		return err
	}

	fields, _ := listRecord["fields"].(map[string]interface{})
	assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
	if assetURL == "" {
		return fmt.Errorf("list %s has no section metadata asset", listID)
	}

	rawAsset, err := w.CK.DownloadAsset(assetURL)
	if err != nil {
		return fmt.Errorf("download memberships asset: %w", err)
	}
	membershipFile, err := sections.DecodeMembershipFile(rawAsset)
	if err != nil {
		return fmt.Errorf("decode memberships asset: %w", err)
	}

	if clearOnly {
		sections.RemoveMemberships(membershipFile, reminderIDs)
	} else {
		sections.UpsertMemberships(membershipFile, sectionID, reminderIDs, appleReferenceSeconds())
	}

	encoded, err := sections.EncodeMembershipFile(membershipFile)
	if err != nil {
		return fmt.Errorf("encode memberships asset: %w", err)
	}

	tokens, err := w.CK.RequestAssetUploadTokens(ownerID, []cloudkit.AssetUploadTokenRequest{{
		RecordType: "List",
		RecordName: listID,
		FieldName:  "MembershipsOfRemindersInSectionsAsData",
	}})
	if err != nil {
		return fmt.Errorf("request asset upload token: %w", err)
	}
	if len(tokens) == 0 {
		return fmt.Errorf("no asset upload token returned")
	}
	uploadURL, _ := tokens[0]["url"].(string)
	if uploadURL == "" {
		return fmt.Errorf("asset upload token missing url")
	}

	assetValue, err := w.CK.UploadAsset(uploadURL, encoded)
	if err != nil {
		return fmt.Errorf("upload memberships asset: %w", err)
	}

	changeTag, _ := listRecord["recordChangeTag"].(string)
	if changeTag == "" {
		return fmt.Errorf("list %s missing change tag", listID)
	}

	result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{{
		"operationType": "update",
		"record": map[string]interface{}{
			"recordType":      "List",
			"recordName":      listID,
			"recordChangeTag": changeTag,
			"fields": map[string]interface{}{
				"MembershipsOfRemindersInSectionsAsData": map[string]interface{}{
					"value": assetValue,
				},
			},
		},
	}})
	if err != nil {
		return err
	}
	if recErr := checkRecordErrors(result); recErr != nil {
		return recErr
	}
	return nil
}

func (w *Writer) clearOrphanSectionMemberships(ownerID, listID, sectionID string) ([]string, error) {
	listRecord, err := lookupRecord(w.CK, ownerID, listID)
	if err != nil {
		return nil, err
	}
	fields, _ := listRecord["fields"].(map[string]interface{})
	assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
	if assetURL == "" {
		return nil, nil
	}
	rawAsset, err := w.CK.DownloadAsset(assetURL)
	if err != nil {
		return nil, fmt.Errorf("download memberships asset: %w", err)
	}
	membershipFile, err := sections.DecodeMembershipFile(rawAsset)
	if err != nil {
		return nil, fmt.Errorf("decode memberships asset: %w", err)
	}

	removed := sections.RemoveSection(membershipFile, sectionID)
	if len(removed) == 0 {
		return nil, nil
	}

	encoded, err := sections.EncodeMembershipFile(membershipFile)
	if err != nil {
		return nil, fmt.Errorf("encode memberships asset: %w", err)
	}
	tokens, err := w.CK.RequestAssetUploadTokens(ownerID, []cloudkit.AssetUploadTokenRequest{{
		RecordType: "List",
		RecordName: listID,
		FieldName:  "MembershipsOfRemindersInSectionsAsData",
	}})
	if err != nil {
		return nil, fmt.Errorf("request asset upload token: %w", err)
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("no asset upload token returned")
	}
	uploadURL, _ := tokens[0]["url"].(string)
	if uploadURL == "" {
		return nil, fmt.Errorf("asset upload token missing url")
	}

	assetValue, err := w.CK.UploadAsset(uploadURL, encoded)
	if err != nil {
		return nil, fmt.Errorf("upload memberships asset: %w", err)
	}
	changeTag, _ := listRecord["recordChangeTag"].(string)
	if changeTag == "" {
		return nil, fmt.Errorf("list %s missing change tag", listID)
	}
	result, err := w.modifyRecordsWithRetry(ownerID, []map[string]interface{}{{
		"operationType": "update",
		"record": map[string]interface{}{
			"recordType":      "List",
			"recordName":      listID,
			"recordChangeTag": changeTag,
			"fields": map[string]interface{}{
				"MembershipsOfRemindersInSectionsAsData": map[string]interface{}{
					"value": assetValue,
				},
			},
		},
	}})
	if err != nil {
		return nil, err
	}
	if recErr := checkRecordErrors(result); recErr != nil {
		return nil, recErr
	}
	return removed, nil
}

func (w *Writer) lookupSectionRecord(ownerID, listID, sectionHint string) (string, map[string]interface{}, error) {
	sectionID, _, err := w.resolveSectionRef(ownerID, listID, sectionHint)
	if err != nil {
		return "", nil, err
	}
	recordName := sections.ListSectionRecordName(sectionID)
	record, err := lookupRecord(w.CK, ownerID, recordName)
	if err != nil {
		return "", nil, err
	}
	return recordName, record, nil
}

func (w *Writer) memberIDsForSection(ownerID, listID, sectionID string) ([]string, error) {
	listRecord, err := lookupRecord(w.CK, ownerID, listID)
	if err != nil {
		return nil, err
	}
	fields, _ := listRecord["fields"].(map[string]interface{})
	assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
	if assetURL == "" {
		return nil, nil
	}
	rawAsset, err := w.CK.DownloadAsset(assetURL)
	if err != nil {
		return nil, err
	}
	membershipFile, err := sections.DecodeMembershipFile(rawAsset)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, membership := range membershipFile.Memberships {
		if membership.GroupID == sectionID {
			ids = append(ids, membership.MemberID)
		}
	}
	return ids, nil
}

func (w *Writer) resolveReminderRef(reminderHint string) (string, error) {
	if strings.HasPrefix(reminderHint, "Reminder/") {
		return reminderHint, nil
	}
	if rid := w.Sync.FindReminderByID(reminderHint); rid != "" {
		return rid, nil
	}
	return "", fmt.Errorf("reminder %q not found", reminderHint)
}

func (w *Writer) resolveListRef(listHint, reminderID string) (string, error) {
	if listHint != "" {
		if strings.HasPrefix(listHint, "List/") {
			return listHint, nil
		}
		if looksLikeUUID(listHint) {
			return "List/" + strings.ToUpper(listHint), nil
		}
		if listID := w.Sync.FindListByName(listHint); listID != "" {
			return listID, nil
		}
		return "", fmt.Errorf("list %q not found", listHint)
	}
	if reminderID != "" {
		if rd := w.Sync.Cache.Reminders[reminderID]; rd != nil && rd.ListRef != nil && *rd.ListRef != "" {
			return *rd.ListRef, nil
		}
	}
	return "", fmt.Errorf("list is required")
}

func (w *Writer) resolveSectionRef(ownerID, listID, sectionHint string) (string, string, error) {
	if strings.HasPrefix(sectionHint, "ListSection/") {
		record, err := lookupRecord(w.CK, ownerID, sectionHint)
		if err != nil {
			return "", "", err
		}
		fields, _ := record["fields"].(map[string]interface{})
		return shortID(sectionHint), resolveSectionName(fields, shortID(sectionHint)), nil
	}
	if looksLikeUUID(sectionHint) {
		recordName := sections.ListSectionRecordName(strings.ToUpper(sectionHint))
		record, err := lookupRecord(w.CK, ownerID, recordName)
		if err != nil {
			return "", "", err
		}
		fields, _ := record["fields"].(map[string]interface{})
		return shortID(recordName), resolveSectionName(fields, shortID(recordName)), nil
	}

	target := strings.ToLower(sectionHint)
	if sectionID, sectionName, ok := w.findSectionInCache(listID, target); ok {
		return sectionID, sectionName, nil
	}
	if sectionID, sectionName, ok := w.findSectionViaMemberships(ownerID, listID, target); ok {
		return sectionID, sectionName, nil
	}

	token := ""
	const maxPages = 16
	for page := 1; page <= maxPages; page++ {
		data, err := w.CK.ChangesZone(ownerID, token)
		if err != nil {
			return "", "", err
		}
		zones, _ := data["zones"].([]interface{})
		if len(zones) == 0 {
			break
		}
		zone, _ := zones[0].(map[string]interface{})
		records, _ := zone["records"].([]interface{})
		for _, raw := range records {
			record, _ := raw.(map[string]interface{})
			recordType, _ := record["recordType"].(string)
			if recordType != "ListSection" {
				continue
			}
			fields, _ := record["fields"].(map[string]interface{})
			if getReferenceRecordName(fields, "List") != listID {
				continue
			}
			recordName, _ := record["recordName"].(string)
			name := resolveSectionName(fields, shortID(recordName))
			if strings.ToLower(name) == target || strings.ToLower(shortID(recordName)) == target {
				return shortID(recordName), name, nil
			}
		}
		moreComing, _ := zone["moreComing"].(bool)
		if !moreComing {
			break
		}
		token, _ = zone["syncToken"].(string)
		if token == "" {
			break
		}
	}
	return "", "", fmt.Errorf("section %q not found in %s", sectionHint, listID)
}

func (w *Writer) findSectionInCache(listID, target string) (string, string, bool) {
	for recordName, section := range w.Sync.Cache.Sections {
		if section == nil || section.ListRef == nil || *section.ListRef != listID {
			continue
		}
		if strings.ToLower(section.Name) == target || strings.ToLower(shortID(recordName)) == target {
			return shortID(recordName), section.Name, true
		}
		if section.CanonicalName != nil && strings.ToLower(*section.CanonicalName) == target {
			return shortID(recordName), section.Name, true
		}
	}
	return "", "", false
}

func (w *Writer) findSectionViaMemberships(ownerID, listID, target string) (string, string, bool) {
	listRecord, err := lookupRecord(w.CK, ownerID, listID)
	if err != nil {
		return "", "", false
	}
	fields, _ := listRecord["fields"].(map[string]interface{})
	assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
	if assetURL == "" {
		return "", "", false
	}
	rawAsset, err := w.CK.DownloadAsset(assetURL)
	if err != nil {
		return "", "", false
	}
	membershipFile, err := sections.DecodeMembershipFile(rawAsset)
	if err != nil {
		return "", "", false
	}

	seen := make(map[string]struct{})
	recordNames := make([]string, 0, len(membershipFile.Memberships))
	for _, membership := range membershipFile.Memberships {
		if _, ok := seen[membership.GroupID]; ok {
			continue
		}
		seen[membership.GroupID] = struct{}{}
		recordNames = append(recordNames, sections.ListSectionRecordName(membership.GroupID))
	}
	if len(recordNames) == 0 {
		return "", "", false
	}

	records, err := w.CK.LookupRecords(ownerID, recordNames)
	if err != nil {
		return "", "", false
	}
	for _, record := range records {
		if code, _ := record["serverErrorCode"].(string); code != "" {
			continue
		}
		recordName, _ := record["recordName"].(string)
		recordFields, _ := record["fields"].(map[string]interface{})
		name := resolveSectionName(recordFields, shortID(recordName))
		if strings.ToLower(name) == target || strings.ToLower(shortID(recordName)) == target {
			return shortID(recordName), name, true
		}
	}
	return "", "", false
}

func lookupRecord(ck *cloudkit.Client, ownerID, recordName string) (map[string]interface{}, error) {
	records, err := ck.LookupRecords(ownerID, []string{recordName})
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("record %s not found", recordName)
	}
	if code, _ := records[0]["serverErrorCode"].(string); code != "" {
		reason, _ := records[0]["reason"].(string)
		return nil, fmt.Errorf("record %s lookup failed: %s", recordName, reason)
	}
	return records[0], nil
}

func resolveSectionName(fields map[string]interface{}, fallback string) string {
	name := getFieldStringValue(fields, "DisplayName")
	if name == "" {
		name = getFieldStringValue(fields, "CanonicalName")
	}
	if name == "" {
		name = fallback
	}
	return name
}

func getFieldStringValue(fields map[string]interface{}, key string) string {
	field, _ := fields[key].(map[string]interface{})
	val, _ := field["value"].(string)
	return val
}

func getReferenceRecordName(fields map[string]interface{}, key string) string {
	field, _ := fields[key].(map[string]interface{})
	val, _ := field["value"].(map[string]interface{})
	recordName, _ := val["recordName"].(string)
	return recordName
}

func getAssetDownloadURL(fields map[string]interface{}, key string) string {
	field, _ := fields[key].(map[string]interface{})
	val, _ := field["value"].(map[string]interface{})
	url, _ := val["downloadURL"].(string)
	return url
}

func appleReferenceSeconds() float64 {
	return float64(time.Now().UnixMilli())/1000 - 978307200
}

func buildCreateSectionOp(ownerID, listID, recordName, name string) map[string]interface{} {
	nowMillis := time.Now().UnixMilli()
	sectionID := shortID(recordName)
	fields := map[string]interface{}{
		"CreationDate": map[string]interface{}{"value": nowMillis, "type": "TIMESTAMP"},
		"DisplayName":  map[string]interface{}{"value": name, "type": "STRING", "isEncrypted": true},
		"CanonicalName": map[string]interface{}{
			"value":       sections.CanonicalName(name),
			"type":        "STRING",
			"isEncrypted": true,
		},
		"ResolutionTokenMap": map[string]interface{}{
			"value": sections.ResolutionTokenMapJSON(nowMillis, sections.DeterministicReplicaID(sectionID), true),
			"type":  "STRING",
		},
		"Imported": map[string]interface{}{"value": 0, "type": "NUMBER_INT64"},
		"List": map[string]interface{}{
			"value": map[string]interface{}{
				"recordName": listID,
				"action":     "VALIDATE",
				"zoneID": map[string]interface{}{
					"zoneName":        cloudkit.Zone,
					"ownerRecordName": ownerID,
					"zoneType":        "REGULAR_CUSTOM_ZONE",
				},
			},
			"type": "REFERENCE",
		},
		"Deleted": map[string]interface{}{"value": 0, "type": "NUMBER_INT64"},
	}
	return map[string]interface{}{
		"operationType": "create",
		"record": map[string]interface{}{
			"recordName": recordName,
			"recordType": "ListSection",
			"parent": map[string]interface{}{
				"recordName": listID,
			},
			"fields": fields,
		},
	}
}

func buildRenameSectionOp(recordName, changeTag, newName string) map[string]interface{} {
	return map[string]interface{}{
		"operationType": "update",
		"record": map[string]interface{}{
			"recordName":      recordName,
			"recordType":      "ListSection",
			"recordChangeTag": changeTag,
			"fields": map[string]interface{}{
				"DisplayName": map[string]interface{}{
					"value":       newName,
					"type":        "STRING",
					"isEncrypted": true,
				},
				"CanonicalName": map[string]interface{}{
					"value":       sections.CanonicalName(newName),
					"type":        "STRING",
					"isEncrypted": true,
				},
			},
		},
	}
}

func looksLikeUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

func shortID(id string) string {
	for i := len(id) - 1; i >= 0; i-- {
		if id[i] == '/' {
			return id[i+1:]
		}
	}
	return id
}

func updatedRecordChangeTag(result map[string]interface{}) *string {
	records, ok := result["records"].([]interface{})
	if !ok || len(records) == 0 {
		return nil
	}
	record, ok := records[0].(map[string]interface{})
	if !ok {
		return nil
	}
	changeTag, _ := record["recordChangeTag"].(string)
	if changeTag == "" {
		return nil
	}
	return &changeTag
}
