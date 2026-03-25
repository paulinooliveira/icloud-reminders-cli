package cmd

import "strings"

func bestEffortSync() error {
	return syncEngine.Sync(false)
}

func shouldProceedWithoutSync(hint string) bool {
	if !allowStaleTarget {
		return false
	}
	bare := hint
	if idx := strings.LastIndexByte(hint, '/'); idx >= 0 {
		bare = hint[idx+1:]
	}
	return looksLikeReminderUUID(bare)
}

func looksLikeReminderUUID(s string) bool {
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
