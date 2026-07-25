package reminders

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"icloud-reminders/pkg/models"
)

func TestPaginateDeclaresTruncation(t *testing.T) {
	items := make([]*models.Reminder, 100)
	for i := range items {
		items[i] = &models.Reminder{ID: fmt.Sprintf("Reminder/%03d", i)}
	}
	page := paginate(items, 10, 0)
	if len(page.Items) != 10 || page.TotalCount != 100 || !page.HasMore || page.Limit != 10 || page.Offset != 0 {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestPaginateCapsLimitAndOffset(t *testing.T) {
	items := make([]*models.Reminder, MaxLimit+20)
	page := paginate(items, MaxLimit+999, 10)
	if page.Limit != MaxLimit || len(page.Items) != MaxLimit || !page.HasMore {
		t.Fatalf("unexpected capped page: %+v", page)
	}
	page = paginate(items, 1, -10)
	if page.Offset != 0 {
		t.Fatalf("negative offset was not normalized: %+v", page)
	}
}

type fakeRunner struct {
	stdout []byte
	stderr []byte
	err    error
	args   []string
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, []byte, error) {
	f.args = append([]string(nil), args...)
	return f.stdout, f.stderr, f.err
}

func TestShowUsesExplicitListAndDeclaresTruncation(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`[
      {"id":"1","title":"one","listID":"L","listName":"Work","isCompleted":false},
      {"id":"2","title":"two","listID":"L","listName":"Work","isCompleted":false}
    ]`)}
	service := &RemindctlService{runner: runner}
	page, err := service.Show(context.Background(), ShowInput{List: "Work", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(runner.args, " ") != "show open --list Work --json --no-input" {
		t.Fatalf("unexpected args: %v", runner.args)
	}
	if len(page.Items) != 1 || page.TotalCount != 2 || !page.HasMore {
		t.Fatalf("unexpected page: %+v", page)
	}
}

func TestFindSearchesTheWholeExplicitList(t *testing.T) {
	values := make([]string, MaxLimit+1)
	for i := range values {
		values[i] = fmt.Sprintf(`{"id":"%03d","title":"item","listID":"L","listName":"Work","isCompleted":false}`, i)
	}
	runner := &fakeRunner{stdout: []byte("[" + strings.Join(values, ",") + "]")}
	service := &RemindctlService{runner: runner}
	item, err := service.Find(context.Background(), "Work", fmt.Sprint(MaxLimit))
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != fmt.Sprint(MaxLimit) {
		t.Fatalf("unexpected item: %+v", item)
	}
	if strings.Join(runner.args, " ") != "show all --list Work --json --no-input" {
		t.Fatalf("unexpected args: %v", runner.args)
	}
}

func TestCompleteAcceptsRemindctlArrayResponse(t *testing.T) {
	runner := &fakeRunner{stdout: []byte(`[{"id":"abc","title":"done","listID":"L","listName":"Work","isCompleted":true}]`)}
	service := &RemindctlService{runner: runner}
	item, err := service.Complete(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "abc" || !item.Completed {
		t.Fatalf("unexpected item: %+v", item)
	}
}

type blockingRunner struct{}

func (blockingRunner) Run(ctx context.Context, _ ...string) ([]byte, []byte, error) {
	<-ctx.Done()
	return nil, nil, ctx.Err()
}

func TestExecRunnerTimesOut(t *testing.T) {
	runner := ExecRunner{Path: "/bin/sleep", Timeout: 10 * time.Millisecond}
	_, _, err := runner.Run(context.Background(), "1")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout, got %v", err)
	}
}
