package cmd

import "strings"

func bestEffortSync() error {
	return syncEngine.Sync(false)
}

func shouldProceedWithoutSync(hint string) bool {
	if strings.HasPrefix(hint, "Reminder/") {
		return true
	}
	return looksLikeReminderUUID(hint)
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
