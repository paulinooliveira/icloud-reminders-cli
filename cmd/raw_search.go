package cmd

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"icloud-reminders/internal/utils"
)

var rawSearchList string
var rawSearchIncludeDeleted bool

var rawSearchCmd = &cobra.Command{
	Use:   "raw-search <query>",
	Short: "Search raw CloudKit reminder records by parsed text and CRDT blob text",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ownerID := ""
		if syncEngine != nil && syncEngine.Cache != nil && syncEngine.Cache.OwnerID != nil && *syncEngine.Cache.OwnerID != "" {
			ownerID = *syncEngine.Cache.OwnerID
		}
		if ownerID == "" {
			var err error
			ownerID, err = ckClient.GetOwnerID()
			if err != nil {
				return err
			}
		}

		listID := ""
		if rawSearchList != "" {
			var err error
			listID, err = resolveListRecordName(ownerID, rawSearchList)
			if err != nil {
				return err
			}
		}

		queryLower := toLowerStr(args[0])
		type match struct {
			recordName string
			title      string
			listRef    string
			deleted    bool
			sample     string
		}
		var matches []match

		token := ""
		const maxPages = 32
		for page := 1; page <= maxPages; page++ {
			data, err := ckClient.ChangesZone(ownerID, token)
			if err != nil {
				return err
			}
			zones, _ := data["zones"].([]interface{})
			if len(zones) == 0 {
				break
			}
			zone, _ := zones[0].(map[string]interface{})
			records, _ := zone["records"].([]interface{})
			for _, raw := range records {
				record, _ := raw.(map[string]interface{})
				if record == nil {
					continue
				}
				if recordType, _ := record["recordType"].(string); recordType != "Reminder" {
					continue
				}
				fields, _ := record["fields"].(map[string]interface{})
				if fields == nil {
					fields = map[string]interface{}{}
				}
				deleted, _ := record["deleted"].(bool)
				if getRecordInt(fields, "Deleted") != 0 {
					deleted = true
				}
				if deleted && !rawSearchIncludeDeleted {
					continue
				}
				if listID != "" && getReferenceRecordName(fields, "List") != listID {
					continue
				}

				title := utils.ExtractTitle(getRecordString(fields, "TitleDocument"))
				notes := utils.ExtractTitle(getRecordString(fields, "NotesDocument"))
				rawText := rawPrintableDocument(getRecordString(fields, "TitleDocument")) + "\n" + rawPrintableDocument(getRecordString(fields, "NotesDocument"))
				searchText := toLowerStr(title + "\n" + notes + "\n" + rawText)
				if !strings.Contains(searchText, queryLower) {
					continue
				}

				sample := notes
				if sample == "" {
					sample = rawText
				}
				sample = compactWhitespace(sample)
				if len(sample) > 140 {
					sample = sample[:140] + "..."
				}

				recordName, _ := record["recordName"].(string)
				matches = append(matches, match{
					recordName: recordName,
					title:      title,
					listRef:    getReferenceRecordName(fields, "List"),
					deleted:    deleted,
					sample:     sample,
				})
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

		sort.Slice(matches, func(i, j int) bool {
			return matches[i].recordName < matches[j].recordName
		})

		fmt.Printf("\n🔎 Raw search: %q → %d matches\n", args[0], len(matches))
		for _, m := range matches {
			deleted := ""
			if m.deleted {
				deleted = " [deleted]"
			}
			fmt.Printf("  • %s%s  (%s)  [%s]\n", m.title, deleted, shortID(m.recordName), m.listRef)
			if m.sample != "" {
				fmt.Printf("    %s\n", m.sample)
			}
		}
		return nil
	},
}

func rawPrintableDocument(b64 string) string {
	if b64 == "" {
		return ""
	}
	compressed, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return ""
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return ""
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return ""
	}

	buf := make([]byte, 0, len(data))
	lastSpace := false
	for _, b := range data {
		switch {
		case b == '\n' || b == '\r' || b == '\t':
			buf = append(buf, ' ')
			lastSpace = true
		case b >= 32 && b < 127:
			buf = append(buf, b)
			lastSpace = false
		default:
			if !lastSpace {
				buf = append(buf, ' ')
				lastSpace = true
			}
		}
	}
	return compactWhitespace(string(buf))
}

func compactWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func getRecordInt(fields map[string]interface{}, key string) int {
	field, _ := fields[key].(map[string]interface{})
	switch v := field["value"].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func init() {
	rawSearchCmd.Flags().StringVarP(&rawSearchList, "list", "l", "", "Filter raw search to a list name or ID")
	rawSearchCmd.Flags().BoolVar(&rawSearchIncludeDeleted, "include-deleted", false, "Include deleted/soft-deleted reminder records")
}
