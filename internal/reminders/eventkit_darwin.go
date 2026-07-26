//go:build darwin && cgo

package reminders

/*
#cgo CFLAGS: -fblocks
#cgo LDFLAGS: -framework Foundation -framework EventKit
#include "eventkit_bridge.h"
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"unsafe"

	"icloud-reminders/pkg/models"
)

type EventKitService struct {
	mu    sync.Mutex
	store *C.reminders_eventkit_store
}

func NewService() (Service, error) {
	var nativeErr *C.char
	store := C.reminders_eventkit_open(&nativeErr)
	if store == nil {
		return nil, bridgeError(nativeErr)
	}
	return &EventKitService{store: store}, nil
}

func bridgeError(value *C.char) error {
	if value == nil {
		return fmt.Errorf("EventKit operation failed")
	}
	defer C.free(unsafe.Pointer(value))
	return fmt.Errorf("EventKit: %s", C.GoString(value))
}

func decodeBridge(value *C.char, nativeErr *C.char, target any) error {
	if value == nil {
		return bridgeError(nativeErr)
	}
	defer C.free(unsafe.Pointer(value))
	if nativeErr != nil {
		C.free(unsafe.Pointer(nativeErr))
	}
	if err := json.Unmarshal([]byte(C.GoString(value)), target); err != nil {
		return fmt.Errorf("decode EventKit response: %w", err)
	}
	return nil
}

func (s *EventKitService) Lists(context.Context) ([]*models.ReminderList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var nativeErr *C.char
	value := C.reminders_eventkit_lists(s.store, &nativeErr)
	var lists []*models.ReminderList
	if err := decodeBridge(value, nativeErr, &lists); err != nil {
		return nil, err
	}
	sort.Slice(lists, func(i, j int) bool { return strings.ToLower(lists[i].Name) < strings.ToLower(lists[j].Name) })
	return lists, nil
}

func (s *EventKitService) Show(_ context.Context, input ShowInput) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, err := json.Marshal(map[string]any{"list": input.List, "include_completed": input.IncludeCompleted})
	if err != nil {
		return Page{}, err
	}
	cPayload := C.CString(string(payload))
	defer C.free(unsafe.Pointer(cPayload))
	var nativeErr *C.char
	value := C.reminders_eventkit_show(s.store, cPayload, &nativeErr)
	var items []*models.Reminder
	if err := decodeBridge(value, nativeErr, &items); err != nil {
		return Page{}, err
	}
	return paginate(items, input.Limit, input.Offset), nil
}

func (s *EventKitService) Find(ctx context.Context, list, id string) (*models.Reminder, error) {
	if strings.TrimSpace(list) == "" || strings.TrimSpace(id) == "" {
		return nil, fmt.Errorf("list and id are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Fetch the whole explicit list: Find must not inherit the public page cap.
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, _ := json.Marshal(map[string]any{"list": list, "include_completed": true})
	cPayload := C.CString(string(payload))
	defer C.free(unsafe.Pointer(cPayload))
	var nativeErr *C.char
	value := C.reminders_eventkit_show(s.store, cPayload, &nativeErr)
	var items []*models.Reminder
	if err := decodeBridge(value, nativeErr, &items); err != nil {
		return nil, err
	}
	return uniqueReminder(items, list, id)
}

func (s *EventKitService) Add(_ context.Context, input AddInput) (*models.Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	cPayload := C.CString(string(payload))
	defer C.free(unsafe.Pointer(cPayload))
	var nativeErr *C.char
	value := C.reminders_eventkit_add(s.store, cPayload, &nativeErr)
	var item models.Reminder
	if err := decodeBridge(value, nativeErr, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *EventKitService) Complete(_ context.Context, id string) (*models.Reminder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cID))
	var nativeErr *C.char
	value := C.reminders_eventkit_complete(s.store, cID, &nativeErr)
	var item models.Reminder
	if err := decodeBridge(value, nativeErr, &item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *EventKitService) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cID := C.CString(id)
	defer C.free(unsafe.Pointer(cID))
	var nativeErr *C.char
	if C.reminders_eventkit_delete(s.store, cID, &nativeErr) == 0 {
		return bridgeError(nativeErr)
	}
	if nativeErr != nil {
		C.free(unsafe.Pointer(nativeErr))
	}
	return nil
}

func (s *EventKitService) Status(context.Context) Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	var nativeErr *C.char
	value := C.reminders_eventkit_status(s.store, &nativeErr)
	var status Status
	if err := decodeBridge(value, nativeErr, &status); err != nil {
		return Status{Backend: "eventkit/in-process", Detail: err.Error()}
	}
	return status
}

func (s *EventKitService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store != nil {
		C.reminders_eventkit_close(s.store)
		s.store = nil
	}
	return nil
}
