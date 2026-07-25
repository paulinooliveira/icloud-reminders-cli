// Package mcpserver exposes iCloud Reminders through the official Go MCP SDK.
package mcpserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"icloud-reminders/internal/reminders"
	"icloud-reminders/internal/remotepolicy"
	"icloud-reminders/pkg/models"
)

type listInput struct{}
type listOutput struct {
	Lists []*models.ReminderList `json:"lists"`
}

type showInput struct {
	List             string `json:"list" jsonschema:"required,Reminder list name or ID"`
	IncludeCompleted bool   `json:"include_completed,omitempty"`
	Limit            int    `json:"limit,omitempty" jsonschema:"Maximum items to return (default 50, max 200)"`
	Offset           int    `json:"offset,omitempty" jsonschema:"Zero-based offset"`
}

type showOutput = reminders.Page
type getInput struct {
	ID   string `json:"id" jsonschema:"required,Reminder ID or unique prefix"`
	List string `json:"list" jsonschema:"required,Reminder list name or ID"`
}
type reminderOutput struct {
	Reminder *models.Reminder `json:"reminder"`
}
type addInput struct {
	Title    string `json:"title" jsonschema:"required,Reminder title"`
	List     string `json:"list" jsonschema:"required,Reminder list name or ID"`
	Due      string `json:"due,omitempty"`
	Priority string `json:"priority,omitempty" jsonschema:"One of none low medium high"`
	Notes    string `json:"notes,omitempty"`
}
type completeInput struct {
	ID   string `json:"id" jsonschema:"required,Reminder ID or unique prefix"`
	List string `json:"list" jsonschema:"required,Reminder list name or ID"`
}
type statusInput struct{}
type statusOutput = reminders.Status

type Access struct {
	Remote bool
	Policy remotepolicy.Policy
}

func LocalAccess() Access { return Access{} }

func New(service reminders.Service, access Access, version string) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "icloud-reminders", Version: version}, nil)
	openWorld, destructive := true, false

	mcp.AddTool(server, &mcp.Tool{Name: "lists", Description: "List reminder lists available to this client.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ listInput) (*mcp.CallToolResult, listOutput, error) {
		lists, err := service.Lists(ctx)
		if err != nil {
			return nil, listOutput{}, err
		}
		if access.Remote {
			filtered := lists[:0]
			for _, list := range lists {
				if access.Policy.AllowsList(list.Name) {
					filtered = append(filtered, list)
				}
			}
			lists = filtered
		}
		sort.Slice(lists, func(i, j int) bool { return strings.ToLower(lists[i].Name) < strings.ToLower(lists[j].Name) })
		return nil, listOutput{Lists: lists}, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "show", Description: "Show reminders from one explicit list with bounded pagination.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input showInput) (*mcp.CallToolResult, showOutput, error) {
		if err := requireList(access, input.List); err != nil {
			return nil, showOutput{}, err
		}
		return pageResult(service.Show(ctx, reminders.ShowInput{List: input.List, IncludeCompleted: input.IncludeCompleted, Limit: input.Limit, Offset: input.Offset}))
	})

	mcp.AddTool(server, &mcp.Tool{Name: "get", Description: "Get one reminder by ID.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input getInput) (*mcp.CallToolResult, reminderOutput, error) {
		if err := requireList(access, input.List); err != nil {
			return nil, reminderOutput{}, err
		}
		item, err := service.Find(ctx, input.List, input.ID)
		if err != nil {
			return nil, reminderOutput{}, err
		}
		return nil, reminderOutput{Reminder: item}, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "add", Description: "Add a reminder to an allowed list.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input addInput) (*mcp.CallToolResult, reminderOutput, error) {
		if err := requireWrite(access); err != nil {
			return nil, reminderOutput{}, err
		}
		if err := requireList(access, input.List); err != nil {
			return nil, reminderOutput{}, err
		}
		item, err := service.Add(ctx, reminders.AddInput{Title: input.Title, List: input.List, Due: input.Due, Priority: input.Priority, Notes: input.Notes})
		if err != nil {
			return nil, reminderOutput{}, err
		}
		return nil, reminderOutput{Reminder: item}, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "complete", Description: "Mark a reminder complete by ID.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive, IdempotentHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, input completeInput) (*mcp.CallToolResult, reminderOutput, error) {
		if err := requireWrite(access); err != nil {
			return nil, reminderOutput{}, err
		}
		if err := requireList(access, input.List); err != nil {
			return nil, reminderOutput{}, err
		}
		matched, err := service.Find(ctx, input.List, input.ID)
		if err != nil {
			return nil, reminderOutput{}, err
		}
		item, err := service.Complete(ctx, matched.ID)
		if err != nil {
			return nil, reminderOutput{}, err
		}
		return nil, reminderOutput{Reminder: item}, nil
	})

	mcp.AddTool(server, &mcp.Tool{Name: "status", Description: "Report iCloud Reminders authentication/backend status.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: &openWorld}}, func(ctx context.Context, _ *mcp.CallToolRequest, _ statusInput) (*mcp.CallToolResult, statusOutput, error) {
		return nil, service.Status(ctx), nil
	})
	return server
}

func requireWrite(access Access) error {
	if access.Remote && !access.Policy.Write {
		return fmt.Errorf("write_not_allowed")
	}
	return nil
}

func requireList(access Access, list string) error {
	list = strings.TrimSpace(list)
	if list == "" {
		return fmt.Errorf("list is required")
	}
	if access.Remote && !access.Policy.AllowsList(list) {
		return fmt.Errorf("list_not_allowed")
	}
	return nil
}

func pageResult(page reminders.Page, err error) (*mcp.CallToolResult, showOutput, error) {
	if err != nil {
		return nil, showOutput{}, err
	}
	return nil, page, nil
}
