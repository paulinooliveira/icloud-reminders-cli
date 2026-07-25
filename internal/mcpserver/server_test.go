package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"icloud-reminders/internal/reminders"
	"icloud-reminders/internal/remotepolicy"
	"icloud-reminders/pkg/models"
)

type fakeService struct {
	lists       []*models.ReminderList
	items       map[string]*models.Reminder
	added       int
	completed   int
	completedID string
}

func newFakeService() *fakeService {
	work := "List/work"
	private := "List/private"
	return &fakeService{
		lists: []*models.ReminderList{{ID: work, Name: "Work"}, {ID: private, Name: "Private"}},
		items: map[string]*models.Reminder{
			"work-1":    {ID: "work-1", Title: "Allowed", ListName: "Work", ListRef: &work},
			"private-1": {ID: "private-1", Title: "CANARY-SECRET", ListName: "Private", ListRef: &private},
		},
	}
}

func (f *fakeService) Lists(context.Context) ([]*models.ReminderList, error) {
	return append([]*models.ReminderList(nil), f.lists...), nil
}
func (f *fakeService) Show(_ context.Context, in reminders.ShowInput) (reminders.Page, error) {
	var items []*models.Reminder
	for _, item := range f.items {
		if item.ListName == in.List {
			items = append(items, item)
		}
	}
	return reminders.Page{Items: items, TotalCount: len(items), Limit: reminders.DefaultLimit}, nil
}
func (f *fakeService) Find(_ context.Context, list, id string) (*models.Reminder, error) {
	for _, item := range f.items {
		if item.ListName == list && (item.ID == id || strings.HasPrefix(item.ID, id)) {
			return item, nil
		}
	}
	return nil, fmt.Errorf("not found")
}
func (f *fakeService) Add(_ context.Context, in reminders.AddInput) (*models.Reminder, error) {
	f.added++
	return &models.Reminder{ID: "new", Title: in.Title, ListName: in.List}, nil
}
func (f *fakeService) Complete(_ context.Context, id string) (*models.Reminder, error) {
	f.completed++
	f.completedID = id
	item, err := f.Find(context.Background(), "Work", id)
	if err != nil {
		return nil, err
	}
	copy := *item
	copy.Completed = true
	return &copy, nil
}
func (f *fakeService) Status(context.Context) reminders.Status {
	return reminders.Status{Authenticated: true, Backend: "fake"}
}
func (f *fakeService) Close() error { return nil }

func connect(t *testing.T, server *mcp.Server) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(context.Background(), st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func call(t *testing.T, session *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestRemotePolicyFiltersListsAndBlocksWrite(t *testing.T) {
	service := newFakeService()
	policy := remotepolicy.Policy{ID: "reader", Lists: []string{"Work"}, Write: false, Enabled: true}
	session := connect(t, New(service, Access{Remote: true, Policy: policy}, "test"))
	result := call(t, session, "lists", map[string]any{})
	if result.IsError {
		t.Fatalf("lists failed: %v", result.GetError())
	}
	text := fmt.Sprint(result.StructuredContent)
	if !contains(text, "Work") || contains(text, "Private") {
		t.Fatalf("allowlist leak: %s", text)
	}
	result = call(t, session, "show", map[string]any{"list": "Private"})
	if !result.IsError || !resultHasText(result, "list_not_allowed") {
		t.Fatalf("private list was not denied: %#v", result)
	}
	result = call(t, session, "add", map[string]any{"title": "x", "list": "Work"})
	if !result.IsError || service.added != 0 {
		t.Fatalf("read-only key wrote: result=%#v added=%d", result, service.added)
	}
}

func TestRemoteWriterCanAddAndCompleteAllowedList(t *testing.T) {
	service := newFakeService()
	policy := remotepolicy.Policy{ID: "writer", Lists: []string{"Work"}, Write: true, Enabled: true}
	session := connect(t, New(service, Access{Remote: true, Policy: policy}, "test"))
	if result := call(t, session, "add", map[string]any{"title": "x", "list": "Work"}); result.IsError {
		t.Fatalf("add failed: %v", result.GetError())
	}
	if result := call(t, session, "complete", map[string]any{"id": "work-1", "list": "Work"}); result.IsError {
		t.Fatalf("complete failed: %v", result.GetError())
	}
	if service.added != 1 || service.completed != 1 {
		t.Fatalf("mutations not called: add=%d complete=%d", service.added, service.completed)
	}
	if service.completedID != "work-1" {
		t.Fatalf("complete used unvalidated id %q", service.completedID)
	}
}

func TestRemoteCompleteExpandsPrefixToValidatedFullID(t *testing.T) {
	service := newFakeService()
	policy := remotepolicy.Policy{ID: "writer", Lists: []string{"Work"}, Write: true, Enabled: true}
	session := connect(t, New(service, Access{Remote: true, Policy: policy}, "test"))
	if result := call(t, session, "complete", map[string]any{"id": "work", "list": "Work"}); result.IsError {
		t.Fatalf("complete failed: %v", result.GetError())
	}
	if service.completedID != "work-1" {
		t.Fatalf("complete received prefix instead of validated full id: %q", service.completedID)
	}
}

type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (b bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header.Set("Authorization", "Bearer "+b.token)
	return b.base.RoundTrip(copy)
}

func TestHTTPRequiresBearerAndCarriesStreamableMCP(t *testing.T) {
	service := newFakeService()
	path := filepath.Join(t.TempDir(), "keys.json")
	doc := fmt.Sprintf(`{"keys":[{"id":"agent","key_hash":"%s","lists":["Work"],"write":false,"enabled":true}]}`, remotepolicy.HashToken("correct"))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := remotepolicy.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(HTTPHandler(service, store, "test"))
	defer ts.Close()
	for _, token := range []string{"", "wrong"} {
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/mcp", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q got %d", token, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
	httpClient := &http.Client{Transport: bearerRoundTripper{token: "correct", base: http.DefaultTransport}}
	client := mcp.NewClient(&mcp.Implementation{Name: "remote-test", Version: "1"}, nil)
	session, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: httpClient, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result := call(t, session, "show", map[string]any{"list": "Work"})
	if result.IsError {
		t.Fatalf("remote MCP call failed: %v", result.GetError())
	}
}

func TestHTTPAcceptsConfiguredPublicHostAndRejectsOthers(t *testing.T) {
	service := newFakeService()
	path := filepath.Join(t.TempDir(), "keys.json")
	doc := fmt.Sprintf(`{"keys":[{"id":"agent","key_hash":"%s","lists":["Work"],"write":false,"enabled":true}]}`, remotepolicy.HashToken("correct"))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := remotepolicy.NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	handler := HTTPHandler(service, store, "test", "reminders.example.com")
	for host, want := range map[string]int{"reminders.example.com": http.StatusUnsupportedMediaType, "evil.example.com": http.StatusForbidden} {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/mcp", nil)
		req.Host = host
		req.Header.Set("Authorization", "Bearer correct")
		resp := httptest.NewRecorder()
		handler.ServeHTTP(resp, req)
		if resp.Code != want {
			t.Fatalf("host %s got %d want %d: %s", host, resp.Code, want, resp.Body.String())
		}
	}
}

func TestHTTPBlocksNonMCPRoutes(t *testing.T) {
	service := newFakeService()
	path := filepath.Join(t.TempDir(), "keys.json")
	doc := fmt.Sprintf(`{"keys":[{"id":"agent","key_hash":"%s","lists":["Work"],"write":false,"enabled":true}]}`, remotepolicy.HashToken("correct"))
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	store, _ := remotepolicy.NewStore(path)
	ts := httptest.NewServer(HTTPHandler(service, store, "test"))
	defer ts.Close()
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/health", nil)
	req.Header.Set("Authorization", "Bearer correct")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("non-MCP route got %d", resp.StatusCode)
	}
}

func contains(value, want string) bool {
	return len(want) == 0 || (len(value) >= len(want) && index(value, want) >= 0)
}

func resultHasText(result *mcp.CallToolResult, want string) bool {
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok && contains(text.Text, want) {
			return true
		}
	}
	return false
}
func index(value, want string) int {
	for i := 0; i+len(want) <= len(value); i++ {
		if value[i:i+len(want)] == want {
			return i
		}
	}
	return -1
}
