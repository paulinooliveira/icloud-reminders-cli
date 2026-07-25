// Package reminders exposes a stable service over Apple Reminders.
package reminders

import (
	"context"
	"fmt"
	"time"

	"icloud-reminders/pkg/models"
)

const DefaultLimit = 50
const MaxLimit = 200
const DefaultTimeout = 30 * time.Second

type Service interface {
	Lists(context.Context) ([]*models.ReminderList, error)
	Show(context.Context, ShowInput) (Page, error)
	Find(context.Context, string, string) (*models.Reminder, error)
	Add(context.Context, AddInput) (*models.Reminder, error)
	Complete(context.Context, string) (*models.Reminder, error)
	Status(context.Context) Status
	Close() error
}

type ShowInput struct {
	List             string `json:"list"`
	IncludeCompleted bool   `json:"include_completed,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	Offset           int    `json:"offset,omitempty"`
}

type AddInput struct {
	Title    string `json:"title"`
	List     string `json:"list"`
	Due      string `json:"due,omitempty"`
	Priority string `json:"priority,omitempty"`
	Notes    string `json:"notes,omitempty"`
	Parent   string `json:"parent,omitempty"`
}

type Page struct {
	Items      []*models.Reminder `json:"items"`
	TotalCount int                `json:"total_count"`
	Limit      int                `json:"limit"`
	Offset     int                `json:"offset"`
	HasMore    bool               `json:"has_more"`
}

type Status struct {
	Authenticated bool   `json:"authenticated"`
	Backend       string `json:"backend"`
	Detail        string `json:"detail,omitempty"`
}

func paginate(items []*models.Reminder, limit, offset int) Page {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	total := len(items)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	return Page{Items: items[offset:end], TotalCount: total, Limit: limit, Offset: offset, HasMore: end < total}
}

func uniqueReminder(items []*models.Reminder, list, id string) (*models.Reminder, error) {
	matches := make([]*models.Reminder, 0, 1)
	for _, item := range items {
		if equalFold(item.ID, id) {
			return item, nil
		}
		if prefixFold(item.ID, id) {
			matches = append(matches, item)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		return nil, fmt.Errorf("reminder prefix %q is ambiguous in list %q", id, list)
	}
	return nil, fmt.Errorf("reminder %q not found in list %q", id, list)
}
