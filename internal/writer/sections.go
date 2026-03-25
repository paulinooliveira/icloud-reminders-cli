package writer

import (
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"icloud-reminders/internal/applebridge"
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
	currentSectionID, _ := w.sectionIDForReminder(ownerID, listID, shortID(reminderID))

	sectionID, sectionName, err := w.resolveSectionRef(ownerID, listID, sectionHint)
	if err != nil {
		return errResult(err), nil
	}

	if err := w.applySectionMembership(ownerID, listID, sectionID, []string{shortID(reminderID)}, false); err != nil {
		return errResult(err), nil
	}
	if currentSectionID != "" && currentSectionID != sectionID {
		if err := w.deleteSectionIfEmpty(ownerID, listID, currentSectionID); err != nil {
			return errResult(err), nil
		}
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
	currentSectionID, _ := w.sectionIDForReminder(ownerID, listID, shortID(reminderID))

	if err := w.applySectionMembership(ownerID, listID, "", []string{shortID(reminderID)}, true); err != nil {
		return errResult(err), nil
	}
	if currentSectionID != "" {
		if err := w.deleteSectionIfEmpty(ownerID, listID, currentSectionID); err != nil {
			return errResult(err), nil
		}
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

type SectionInfo struct {
	ID   string
	Name string
}

func (w *Writer) SweepEmptySections(listHint string) ([]string, error) {
	ownerID, err := w.ownerID()
	if err != nil {
		return nil, err
	}
	listID, err := w.resolveListRef(listHint, "")
	if err != nil {
		return nil, err
	}
	listRecord, listFields, membershipFile, err := w.loadMembershipFile(ownerID, listID)
	if err != nil {
		return nil, err
	}
	sectionsForList, err := w.listSections(ownerID, listID)
	if err != nil {
		return nil, err
	}
	activeMemberIDs, err := w.activeSectionMemberIDs(ownerID, listID, resolveListName(listFields, listID), membershipFile)
	if err != nil {
		return nil, err
	}
	if pruneInactiveMemberships(membershipFile, activeMemberIDs) {
		if err := w.persistMembershipFile(ownerID, listID, listRecord, listFields, membershipFile); err != nil {
			return nil, err
		}
	}
	deleted := make([]string, 0)
	for _, section := range sectionsForList {
		if section.ID == "" {
			continue
		}
		if sections.HasMembers(membershipFile, section.ID) {
			continue
		}
		if err := w.deleteSectionRecord(ownerID, listID, section.ID); err != nil {
			return nil, err
		}
		if section.Name != "" {
			deleted = append(deleted, section.Name)
		} else {
			deleted = append(deleted, section.ID)
		}
	}
	if bridge := loadSectionValidatorBridge(); bridge != nil {
		if localDeleted, err := bridge.CleanupEmptySectionsInStore(shortID(listID)); err == nil {
			deleted = append(deleted, localDeleted...)
		}
	}
	sort.Strings(deleted)
	deleted = uniqueSortedStrings(deleted)
	return deleted, nil
}

func (w *Writer) activeSectionMemberIDs(ownerID, listID, listName string, membershipFile *sections.MembershipFile) (map[string]struct{}, error) {
	memberIDs := uniqueMembershipMemberIDs(membershipFile)
	if len(memberIDs) == 0 {
		return map[string]struct{}{}, nil
	}
	if bridge := loadSectionValidatorBridge(); bridge != nil && strings.TrimSpace(listName) != "" {
		if ids, err := bridge.ActiveReminderUUIDsFromStore(listID); err == nil {
			active := make(map[string]struct{}, len(ids))
			for _, id := range ids {
				active[strings.ToUpper(strings.TrimSpace(id))] = struct{}{}
			}
			return active, nil
		}
		items, err := bridge.ListReminders(listName)
		if err == nil {
			active := make(map[string]struct{}, len(items))
			for _, item := range items {
				if item.Completed {
					continue
				}
				active[strings.ToUpper(item.UUID())] = struct{}{}
			}
			return active, nil
		}
	}

	recordNames := make([]string, 0, len(memberIDs)*2)
	seenRecords := make(map[string]struct{}, len(memberIDs)*2)
	for _, memberID := range memberIDs {
		for _, candidate := range []string{memberID, sections.ReminderRecordName(memberID)} {
			if _, ok := seenRecords[candidate]; ok {
				continue
			}
			seenRecords[candidate] = struct{}{}
			recordNames = append(recordNames, candidate)
		}
	}
	records, err := w.CK.LookupRecords(ownerID, recordNames)
	if err != nil {
		return nil, err
	}
	active := make(map[string]struct{}, len(records))
	for _, record := range records {
		if code, _ := record["serverErrorCode"].(string); code != "" {
			continue
		}
		fields, _ := record["fields"].(map[string]interface{})
		if getReferenceRecordName(fields, "List") != listID {
			continue
		}
		if getFieldIntValue(fields, "Completed") != 0 {
			continue
		}
		title := utils.ExtractTitle(getFieldStringValue(fields, "TitleDocument"))
		if strings.TrimSpace(title) == "" {
			continue
		}
		recordName, _ := record["recordName"].(string)
		active[strings.ToUpper(shortID(recordName))] = struct{}{}
	}
	return active, nil
}

func loadSectionValidatorBridge() *applebridge.Bridge {
	cfg := applebridge.LoadValidatorConfigFromEnv()
	if cfg == nil {
		return nil
	}
	return applebridge.New(cfg)
}

func uniqueMembershipMemberIDs(membershipFile *sections.MembershipFile) []string {
	if membershipFile == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(membershipFile.Memberships))
	out := make([]string, 0, len(membershipFile.Memberships))
	for _, membership := range membershipFile.Memberships {
		memberID := strings.ToUpper(strings.TrimSpace(membership.MemberID))
		if memberID == "" {
			continue
		}
		if _, ok := seen[memberID]; ok {
			continue
		}
		seen[memberID] = struct{}{}
		out = append(out, memberID)
	}
	sort.Strings(out)
	return out
}

func pruneInactiveMemberships(membershipFile *sections.MembershipFile, activeMemberIDs map[string]struct{}) bool {
	if membershipFile == nil || len(membershipFile.Memberships) == 0 {
		return false
	}
	filtered := make([]sections.Membership, 0, len(membershipFile.Memberships))
	changed := false
	for _, membership := range membershipFile.Memberships {
		memberID := strings.ToUpper(strings.TrimSpace(membership.MemberID))
		if _, ok := activeMemberIDs[memberID]; !ok {
			changed = true
			continue
		}
		filtered = append(filtered, membership)
	}
	if changed {
		membershipFile.Memberships = filtered
	}
	return changed
}

func uniqueSortedStrings(values []string) []string {
	if len(values) == 0 {
		return values
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
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
		"section_id":    recordName,
		"list_id":       listID,
		"name":          sectionName,
		"cleared_count": len(memberIDs),
		"records":       result["records"],
	}, nil
}

func (w *Writer) applySectionMembership(ownerID, listID, sectionID string, reminderIDs []string, clearOnly bool) error {
	listRecord, fields, membershipFile, err := w.loadMembershipFile(ownerID, listID)
	if err != nil {
		return err
	}

	if clearOnly {
		sections.RemoveMemberships(membershipFile, reminderIDs)
	} else {
		sections.UpsertMemberships(membershipFile, sectionID, reminderIDs, appleReferenceSeconds())
	}

	return w.persistMembershipFile(ownerID, listID, listRecord, fields, membershipFile)
}

func (w *Writer) sectionIDForReminder(ownerID, listID, reminderID string) (string, error) {
	listRecord, err := lookupRecord(w.CK, ownerID, listID)
	if err != nil {
		return "", err
	}
	fields, _ := listRecord["fields"].(map[string]interface{})
	assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
	if assetURL == "" {
		return "", nil
	}
	rawAsset, err := w.CK.DownloadAsset(assetURL)
	if err != nil {
		return "", fmt.Errorf("download memberships asset: %w", err)
	}
	membershipFile, err := sections.DecodeMembershipFile(rawAsset)
	if err != nil {
		return "", fmt.Errorf("decode memberships asset: %w", err)
	}
	return sections.GroupIDForMember(membershipFile, reminderID), nil
}

func (w *Writer) deleteSectionIfEmpty(ownerID, listID, sectionID string) error {
	if sectionID == "" {
		return nil
	}
	memberIDs, err := w.memberIDsForSection(ownerID, listID, sectionID)
	if err != nil {
		return err
	}
	if len(memberIDs) > 0 {
		return nil
	}
	return w.deleteSectionRecord(ownerID, listID, sectionID)
}

func (w *Writer) deleteSectionRecord(ownerID, listID, sectionID string) error {
	recordName, record, err := w.lookupSectionRecord(ownerID, listID, sectionID)
	if err != nil {
		return nil
	}
	changeTag, _ := record["recordChangeTag"].(string)
	if changeTag == "" {
		return nil
	}
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
	if recErr := checkRecordErrors(result); recErr != nil {
		return recErr
	}
	delete(w.Sync.Cache.Sections, recordName)
	if err := w.Sync.Cache.Save(); err != nil {
		// non-fatal
	}
	return nil
}

func (w *Writer) clearOrphanSectionMemberships(ownerID, listID, sectionID string) ([]string, error) {
	listRecord, fields, membershipFile, err := w.loadMembershipFile(ownerID, listID)
	if err != nil {
		return nil, err
	}

	removed := sections.RemoveSection(membershipFile, sectionID)
	if len(removed) == 0 {
		return nil, nil
	}
	if err := w.persistMembershipFile(ownerID, listID, listRecord, fields, membershipFile); err != nil {
		return nil, err
	}
	return removed, nil
}

func (w *Writer) loadMembershipFile(ownerID, listID string) (map[string]interface{}, map[string]interface{}, *sections.MembershipFile, error) {
	listRecord, err := lookupRecord(w.CK, ownerID, listID)
	if err != nil {
		return nil, nil, nil, err
	}
	fields, _ := listRecord["fields"].(map[string]interface{})
	assetURL := getAssetDownloadURL(fields, "MembershipsOfRemindersInSectionsAsData")
	membershipFile := &sections.MembershipFile{}
	if assetURL == "" {
		return listRecord, fields, membershipFile, nil
	}
	rawAsset, err := w.CK.DownloadAsset(assetURL)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("download memberships asset: %w", err)
	}
	membershipFile, err = sections.DecodeMembershipFile(rawAsset)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("decode memberships asset: %w", err)
	}
	return listRecord, fields, membershipFile, nil
}

func (w *Writer) persistMembershipFile(ownerID, listID string, listRecord, fields map[string]interface{}, membershipFile *sections.MembershipFile) error {
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
				"MembershipsOfRemindersInSectionsChecksum": map[string]interface{}{
					"value":       checksumHex(encoded),
					"type":        "STRING",
					"isEncrypted": true,
				},
				"ResolutionTokenMap": map[string]interface{}{
					"value": bumpListResolutionTokenMap(fields, "membershipsOfRemindersInSectionsChecksum"),
					"type":  "STRING",
				},
			},
		},
	}})
	if err != nil {
		return err
	}
	return checkRecordErrors(result)
}

func resolveListName(fields map[string]interface{}, fallback string) string {
	name := strings.TrimSpace(getFieldStringValue(fields, "Name"))
	if name != "" {
		return name
	}
	return shortID(fallback)
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
	_, _, membershipFile, err := w.loadMembershipFile(ownerID, listID)
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

func (w *Writer) visibleSectionMemberIDs(memberIDs []string) []string {
	visible := make([]string, 0, len(memberIDs))
	for _, memberID := range memberIDs {
		reminder := w.Sync.Cache.Reminders[memberID]
		if reminder == nil {
			reminder = w.Sync.Cache.Reminders[sections.ReminderRecordName(memberID)]
		}
		if reminder == nil || reminder.Completed {
			continue
		}
		visible = append(visible, memberID)
	}
	return visible
}

func (w *Writer) resolveReminderRef(reminderHint string) (string, error) {
	if strings.HasPrefix(reminderHint, "Reminder/") {
		return reminderHint, nil
	}
	if rid, _, err := w.resolveReminderRecord(reminderHint); err == nil && rid != "" {
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
	membershipSections, err := w.membershipSections(ownerID, listID)
	if err != nil {
		return "", "", false
	}
	for _, section := range membershipSections {
		if strings.ToLower(section.Name) == target || strings.ToLower(section.ID) == target {
			return section.ID, section.Name, true
		}
	}
	return "", "", false
}

func (w *Writer) listSections(ownerID, listID string) ([]SectionInfo, error) {
	seen := make(map[string]SectionInfo)
	membershipSections, err := w.membershipSections(ownerID, listID)
	if err != nil {
		return nil, err
	}
	for _, section := range membershipSections {
		seen[section.ID] = section
	}
	for recordName, section := range w.Sync.Cache.Sections {
		if section == nil || section.ListRef == nil || *section.ListRef != listID {
			continue
		}
		sectionID := shortID(recordName)
		name := strings.TrimSpace(section.Name)
		if name == "" && section.CanonicalName != nil {
			name = strings.TrimSpace(*section.CanonicalName)
		}
		if name == "" {
			name = sectionID
		}
		seen[sectionID] = SectionInfo{ID: sectionID, Name: name}
	}

	token := ""
	const maxPages = 16
	for page := 1; page <= maxPages; page++ {
		data, err := w.CK.ChangesZone(ownerID, token)
		if err != nil {
			return nil, err
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
			sectionID := shortID(recordName)
			name := resolveSectionName(fields, sectionID)
			seen[sectionID] = SectionInfo{ID: sectionID, Name: name}
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

	out := make([]SectionInfo, 0, len(seen))
	for _, section := range seen {
		out = append(out, section)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (w *Writer) membershipSections(ownerID, listID string) ([]SectionInfo, error) {
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
	return membershipSectionInfosFromFile(w.CK, ownerID, membershipFile)
}

func membershipSectionInfosFromFile(ck *cloudkit.Client, ownerID string, membershipFile *sections.MembershipFile) ([]SectionInfo, error) {
	groupIDs := sections.UniqueGroupIDs(membershipFile)
	if len(groupIDs) == 0 {
		return nil, nil
	}
	recordNames := make([]string, 0, len(groupIDs))
	for _, groupID := range groupIDs {
		recordNames = append(recordNames, sections.ListSectionRecordName(groupID))
	}
	infos := make(map[string]SectionInfo, len(groupIDs))
	for _, groupID := range groupIDs {
		infos[groupID] = SectionInfo{ID: groupID, Name: groupID}
	}
	records, err := ck.LookupRecords(ownerID, recordNames)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		recordName, _ := record["recordName"].(string)
		sectionID := shortID(recordName)
		if sectionID == "" {
			continue
		}
		if code, _ := record["serverErrorCode"].(string); code != "" {
			continue
		}
		fields, _ := record["fields"].(map[string]interface{})
		infos[sectionID] = SectionInfo{ID: sectionID, Name: resolveSectionName(fields, sectionID)}
	}
	out := make([]SectionInfo, 0, len(infos))
	for _, info := range infos {
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].ID < out[j].ID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
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

func getFieldIntValue(fields map[string]interface{}, key string) int {
	field, _ := fields[key].(map[string]interface{})
	switch v := field["value"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return 0
}

func getFieldInt64Value(fields map[string]interface{}, key string) int64 {
	field, _ := fields[key].(map[string]interface{})
	switch v := field["value"].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
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

func checksumHex(raw []byte) string {
	sum := sha512.Sum512(raw)
	return hex.EncodeToString(sum[:])
}

func bumpListResolutionTokenMap(fields map[string]interface{}, key string) string {
	raw := getFieldStringValue(fields, "ResolutionTokenMap")
	if raw == "" {
		return raw
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return raw
	}
	m, _ := payload["map"].(map[string]interface{})
	if m == nil {
		return raw
	}
	entry, _ := m[key].(map[string]interface{})
	if entry == nil {
		entry = map[string]interface{}{}
		m[key] = entry
	}
	entry["modificationTime"] = appleReferenceSeconds()
	counter := 0
	switch v := entry["counter"].(type) {
	case float64:
		counter = int(v)
	case int:
		counter = v
	}
	entry["counter"] = counter + 1
	if _, ok := entry["replicaID"].(string); !ok || entry["replicaID"] == "" {
		entry["replicaID"] = "D25A1933-3DDF-4573-8177-B051D76C2E5B"
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return string(encoded)
}
