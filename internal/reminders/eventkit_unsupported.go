//go:build !darwin || !cgo

package reminders

import "fmt"

// EventKitService is declared on unsupported builds so callers get a clear,
// compile-time-stable backend contract rather than a silent subprocess fallback.
type EventKitService struct{}

func NewService() (Service, error) {
	return nil, fmt.Errorf("in-process EventKit backend requires macOS with cgo enabled")
}
