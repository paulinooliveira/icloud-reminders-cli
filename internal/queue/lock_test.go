package queue

import (
	"slices"
	"sync"
	"testing"
	"time"
)

func TestWithQueueLockSerializesCallers(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	order := []string{}
	started := make(chan struct{})
	release := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		if err := WithQueueLock(func() error {
			mu.Lock()
			order = append(order, "first-start")
			mu.Unlock()
			close(started)
			<-release
			mu.Lock()
			order = append(order, "first-end")
			mu.Unlock()
			return nil
		}); err != nil {
			t.Errorf("first lock failed: %v", err)
		}
	}()

	<-started

	go func() {
		defer wg.Done()
		if err := WithQueueLock(func() error {
			mu.Lock()
			order = append(order, "second")
			mu.Unlock()
			return nil
		}); err != nil {
			t.Errorf("second lock failed: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	if slices.Contains(order, "second") {
		t.Fatalf("second caller entered lock early: %#v", order)
	}
	mu.Unlock()

	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(order) != 3 || order[0] != "first-start" || order[1] != "first-end" || order[2] != "second" {
		t.Fatalf("unexpected order: %#v", order)
	}
}
