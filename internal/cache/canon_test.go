package cache_test

import (
	"testing"
	"icloud-reminders/internal/cache"
)

func TestCanonicalKeyVariants(t *testing.T) {
	cases := []struct{ in, want string }{
		{"B267CC2E-E156-4CCE-9DED-E1E5576F4911", "Reminder/B267CC2E-E156-4CCE-9DED-E1E5576F4911"},
		{"Reminder/B267CC2E-E156-4CCE-9DED-E1E5576F4911", "Reminder/B267CC2E-E156-4CCE-9DED-E1E5576F4911"},
		{"b267cc2e-e156-4cce-9ded-e1e5576f4911", "Reminder/B267CC2E-E156-4CCE-9DED-E1E5576F4911"},
		{"ABC-123", ""},
		{"", ""},
	}
	for _, tc := range cases {
		got := cache.CanonicalReminderKey(tc.in)
		if got != tc.want {
			t.Errorf("CanonicalReminderKey(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
