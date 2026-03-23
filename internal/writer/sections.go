package writer

import (
	"fmt"
	"strings"
	"time"

	"icloud-reminders/internal/cloudkit"
	"icloud-reminders/internal/sections"
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
