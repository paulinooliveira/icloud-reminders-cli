package reminders

import "strings"

func equalFold(left, right string) bool { return strings.EqualFold(left, right) }
func prefixFold(value, prefix string) bool {
	return strings.HasPrefix(strings.ToLower(value), strings.ToLower(prefix))
}
