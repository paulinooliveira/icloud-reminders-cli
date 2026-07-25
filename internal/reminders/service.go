// Package reminders exposes a stable service over the authorized remindctl
// EventKit backend. Both MCP transports use this exact service.
package reminders

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strings"
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
	List             string
	IncludeCompleted bool
	Limit            int
	Offset           int
}

type AddInput struct{ Title, List, Due, Priority, Notes, Parent string }

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

type CommandRunner interface {
	Run(context.Context, ...string) ([]byte, []byte, error)
}

type ExecRunner struct {
	Path    string
	Timeout time.Duration
}

func NewService() (*RemindctlService, error) {
	path, err := exec.LookPath("remindctl")
	if err != nil {
		if _, fallbackErr := exec.LookPath("/usr/local/bin/remindctl"); fallbackErr != nil {
			return nil, fmt.Errorf("remindctl not found in PATH")
		}
		path = "/usr/local/bin/remindctl"
	}
	return &RemindctlService{runner: ExecRunner{Path: path, Timeout: DefaultTimeout}}, nil
}

func (r ExecRunner) Run(ctx context.Context, args ...string) ([]byte, []byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(runCtx, r.Path, args...)
	var stdout, stderr strings.Builder
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	if runCtx.Err() == context.DeadlineExceeded {
		return nil, []byte(stderr.String()), fmt.Errorf("remindctl timed out after %s", timeout)
	}
	if err != nil {
		return []byte(stdout.String()), []byte(stderr.String()), fmt.Errorf("remindctl %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return []byte(stdout.String()), []byte(stderr.String()), nil
}

type RemindctlService struct{ runner CommandRunner }

type remindctlList struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}
type remindctlReminder struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Completed    bool   `json:"isCompleted"`
	Due          string `json:"dueDate,omitempty"`
	Priority     string `json:"priority,omitempty"`
	Notes        string `json:"notes,omitempty"`
	ListID       string `json:"listID"`
	ListName     string `json:"listName"`
	LastModified string `json:"lastModifiedDate,omitempty"`
}

func (s *RemindctlService) Lists(ctx context.Context) ([]*models.ReminderList, error) {
	stdout, _, err := s.runner.Run(ctx, "list", "--json", "--no-input")
	if err != nil {
		return nil, err
	}
	var values []remindctlList
	if err := json.Unmarshal(stdout, &values); err != nil {
		return nil, fmt.Errorf("parse remindctl lists: %w", err)
	}
	lists := make([]*models.ReminderList, 0, len(values))
	for _, value := range values {
		lists = append(lists, &models.ReminderList{ID: value.ID, Name: value.Title})
	}
	sort.Slice(lists, func(i, j int) bool { return strings.ToLower(lists[i].Name) < strings.ToLower(lists[j].Name) })
	return lists, nil
}

func (s *RemindctlService) Show(ctx context.Context, input ShowInput) (Page, error) {
	items, err := s.showAll(ctx, input.List, input.IncludeCompleted)
	if err != nil {
		return Page{}, err
	}
	return paginate(items, input.Limit, input.Offset), nil
}

func (s *RemindctlService) showAll(ctx context.Context, list string, includeCompleted bool) ([]*models.Reminder, error) {
	filter := "open"
	if includeCompleted {
		filter = "all"
	}
	args := []string{"show", filter}
	if strings.TrimSpace(list) != "" {
		args = append(args, "--list", list)
	}
	args = append(args, "--json", "--no-input")
	stdout, _, err := s.runner.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var values []remindctlReminder
	if err := json.Unmarshal(stdout, &values); err != nil {
		return nil, fmt.Errorf("parse remindctl reminders: %w", err)
	}
	items := make([]*models.Reminder, 0, len(values))
	for _, value := range values {
		items = append(items, convertReminder(value))
	}
	return items, nil
}

func (s *RemindctlService) Find(ctx context.Context, list, id string) (*models.Reminder, error) {
	if strings.TrimSpace(list) == "" {
		return nil, fmt.Errorf("list is required")
	}
	items, err := s.showAll(ctx, list, true)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		if strings.EqualFold(item.ID, id) || strings.HasPrefix(strings.ToLower(item.ID), strings.ToLower(id)) {
			return item, nil
		}
	}
	return nil, fmt.Errorf("reminder %q not found in list %q", id, list)
}

func (s *RemindctlService) Add(ctx context.Context, input AddInput) (*models.Reminder, error) {
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.List) == "" {
		return nil, fmt.Errorf("title and list are required")
	}
	args := []string{"add", input.Title, "--list", input.List}
	if input.Due != "" {
		args = append(args, "--due", input.Due)
	}
	if input.Priority != "" {
		args = append(args, "--priority", input.Priority)
	}
	if input.Notes != "" {
		args = append(args, "--notes", input.Notes)
	}
	args = append(args, "--json", "--no-input")
	stdout, _, err := s.runner.Run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var value remindctlReminder
	if err := json.Unmarshal(stdout, &value); err != nil {
		return nil, fmt.Errorf("parse remindctl add: %w", err)
	}
	return convertReminder(value), nil
}

func (s *RemindctlService) Complete(ctx context.Context, id string) (*models.Reminder, error) {
	stdout, _, err := s.runner.Run(ctx, "complete", id, "--json", "--no-input")
	if err != nil {
		return nil, err
	}
	value, err := decodeOneReminder(stdout)
	if err != nil {
		return nil, fmt.Errorf("parse remindctl complete: %w", err)
	}
	return convertReminder(value), nil
}

func decodeOneReminder(data []byte) (remindctlReminder, error) {
	var value remindctlReminder
	if err := json.Unmarshal(data, &value); err == nil && value.ID != "" {
		return value, nil
	}
	var values []remindctlReminder
	if err := json.Unmarshal(data, &values); err != nil {
		return remindctlReminder{}, err
	}
	if len(values) != 1 {
		return remindctlReminder{}, fmt.Errorf("expected one reminder, got %d", len(values))
	}
	return values[0], nil
}

func (s *RemindctlService) Status(ctx context.Context) Status {
	stdout, stderr, err := s.runner.Run(ctx, "status")
	if err != nil {
		return Status{Backend: "remindctl/eventkit", Detail: strings.TrimSpace(string(stderr))}
	}
	detail := strings.TrimSpace(string(stdout))
	return Status{Authenticated: strings.Contains(strings.ToLower(detail), "full access"), Backend: "remindctl/eventkit", Detail: detail}
}
func (s *RemindctlService) Close() error { return nil }

func convertReminder(value remindctlReminder) *models.Reminder {
	listRef := value.ListID
	item := &models.Reminder{ID: value.ID, Title: value.Title, Completed: value.Completed, ListName: value.ListName, ListRef: &listRef, Notes: stringPtr(value.Notes)}
	if value.Due != "" {
		item.Due = stringPtr(value.Due)
	}
	item.Priority = models.PriorityMap[strings.ToLower(value.Priority)]
	if parsed, err := time.Parse(time.RFC3339Nano, value.LastModified); err == nil {
		stamp := parsed.Unix()
		item.ModifiedTS = &stamp
	}
	return item
}

func stringPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
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
